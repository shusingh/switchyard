package server

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/shusingh/switchyard/internal/backend"
	"github.com/shusingh/switchyard/internal/config"
	"github.com/shusingh/switchyard/internal/proxy"
	"github.com/shusingh/switchyard/internal/scheduler"
	"github.com/shusingh/switchyard/internal/telemetry"
)

// The overhead benchmarks send identical requests straight to an instant
// backend and through the full router to it. The difference between the two
// is the router's added latency: body handling, admission, keying, index
// match and insert, the policy, and the proxy hop.

const overheadResponse = `{"id":"x","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1000,"completion_tokens":1,"total_tokens":1001}}`

func instantBackend(b *testing.B) *httptest.Server {
	b.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/health" {
			return
		}
		_, _ = io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, overheadResponse)
	}))
	b.Cleanup(srv.Close)
	return srv
}

// overheadBody is a 4 KB chat request, about 1,000 tokens.
var overheadBody = `{"model":"m","max_tokens":1,"messages":[{"role":"system","content":"` +
	strings.Repeat("You follow the operator policy. ", 125) + `"},{"role":"user","content":"hello"}]}`

func benchmarkPost(b *testing.B, url string) {
	transport := &http.Transport{MaxIdleConnsPerHost: 256}
	b.Cleanup(transport.CloseIdleConnections)
	client := &http.Client{Transport: transport}
	b.ReportAllocs()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			req, _ := http.NewRequestWithContext(context.Background(), http.MethodPost,
				url+"/v1/chat/completions", strings.NewReader(overheadBody))
			resp, err := client.Do(req)
			if err != nil {
				b.Error(err)
				return
			}
			_, _ = io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
		}
	})
}

func BenchmarkDirectToBackend(b *testing.B) {
	benchmarkPost(b, instantBackend(b).URL)
}

func BenchmarkThroughRouter(b *testing.B) {
	for _, policy := range []string{scheduler.PolicyRoundRobin, scheduler.PolicyEstimatedTTFT} {
		b.Run(policy, func(b *testing.B) {
			benchmarkPost(b, newOverheadRouter(b, policy))
		})
	}
}

// BenchmarkLatencyAt1000RPS sends requests open-loop at 1,000 per second,
// directly and through the router, and reports latency percentiles of each.
// The design target is under 1 ms of added p99 latency at this rate.
//
// Run it on Linux. Go's monotonic clock on Windows has a resolution of about
// half a millisecond, which rounds most of these sub-millisecond latencies
// to zero.
func BenchmarkLatencyAt1000RPS(b *testing.B) {
	const rate, requests = 1000, 5000
	targets := map[string]string{
		"direct": instantBackend(b).URL,
		"router": newOverheadRouter(b, scheduler.PolicyEstimatedTTFT),
	}
	for name, url := range targets {
		b.Run(name, func(b *testing.B) {
			transport := &http.Transport{MaxIdleConnsPerHost: 256}
			b.Cleanup(transport.CloseIdleConnections)
			client := &http.Client{Transport: transport}
			var p50, p99 time.Duration
			for b.Loop() {
				latencies := make([]time.Duration, requests)
				var wg sync.WaitGroup
				start := time.Now()
				for i := range requests {
					time.Sleep(time.Until(start.Add(time.Duration(i) * time.Second / rate)))
					wg.Go(func() {
						req, _ := http.NewRequestWithContext(context.Background(), http.MethodPost,
							url+"/v1/chat/completions", strings.NewReader(overheadBody))
						sent := time.Now()
						resp, err := client.Do(req)
						if err != nil {
							b.Error(err)
							return
						}
						_, _ = io.Copy(io.Discard, resp.Body)
						resp.Body.Close()
						latencies[i] = time.Since(sent)
					})
				}
				wg.Wait()
				slices.Sort(latencies)
				p50, p99 = latencies[requests/2], latencies[requests*99/100]
			}
			// Reported after the loop: the benchmark framework discards
			// metrics reported while it is still timing.
			b.ReportMetric(float64(p50.Microseconds()), "p50-us")
			b.ReportMetric(float64(p99.Microseconds()), "p99-us")
		})
	}
}

// newOverheadRouter starts a router with the given policy over four instant
// backends and returns its URL.
func newOverheadRouter(b *testing.B, policy string) string {
	b.Helper()
	backends := make([]config.Backend, 4)
	for i := range backends {
		backends[i] = config.Backend{ID: "b" + strconv.Itoa(i), URL: instantBackend(b).URL}
	}
	pool, err := backend.NewPool(backends, backend.BreakerConfig{})
	if err != nil {
		b.Fatal(err)
	}
	checker := backend.NewHealthChecker(pool, config.Health{
		Path: "/health", Interval: time.Hour, Timeout: time.Second, UnhealthyThreshold: 1, HealthyThreshold: 1,
	}, slog.New(slog.DiscardHandler))
	checker.CheckAll(context.Background())
	b.Cleanup(checker.CloseIdleConnections)

	est := testEstimator(len(backends))
	p, err := scheduler.New(policy, scheduler.Options{Estimator: est, BalanceAbs: 16, BalanceRel: 1.5, TieEpsilon: 0.05})
	if err != nil {
		b.Fatal(err)
	}
	px := proxy.New(config.Proxy{
		DialTimeout: time.Second, ResponseHeaderTimeout: 10 * time.Second,
		StreamIdleTimeout: 10 * time.Second, MaxIdleConnsPerHost: 256,
	})
	b.Cleanup(px.CloseIdleConnections)
	router := httptest.NewServer(New(Options{
		Pool: pool, Policy: p, Proxy: px, Keyer: testKeyer(), Index: testIndex(len(backends)),
		Estimator: est, Logger: slog.New(slog.DiscardHandler), MaxRequestBytes: 1 << 20,
		Admission: admissionOrDefault(b, nil), Metrics: telemetry.NewMetrics(),
		RetryBudgetRatio: 0.1,
	}).Handler())
	b.Cleanup(router.Close)
	return router.URL
}
