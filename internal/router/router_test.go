package router

import (
	"context"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"go.uber.org/goleak"

	"github.com/shusingh/switchyard/internal/config"
	"github.com/shusingh/switchyard/internal/openai"
	"github.com/shusingh/switchyard/internal/sim"
	"github.com/shusingh/switchyard/internal/sim/simtest"
)

func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}

// start runs a router over one simulated engine with the given engine cost
// and returns its URL, the cancel function that begins shutdown, and a wait
// function that returns Serve's result. wait may be called any number of
// times.
func start(t *testing.T, cost sim.CostModel, shutdownTimeout time.Duration) (string, context.CancelFunc, func() error) {
	t.Helper()
	engine := simtest.Start(t, sim.Options{Model: "sim-model", Cost: cost, DefaultOutputTokens: 4})
	cfg, err := config.Parse([]byte("backends:\n  - id: sim\n    url: " + engine.URL + "\n"))
	if err != nil {
		t.Fatal(err)
	}
	cfg.Server.ShutdownTimeout = shutdownTimeout
	r, err := New(cfg, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	ln, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	var serveErr error
	finished := make(chan struct{})
	go func() {
		serveErr = r.Serve(ctx, ln)
		close(finished)
	}()
	wait := func() error {
		<-finished
		return serveErr
	}
	t.Cleanup(func() {
		cancel()
		_ = wait()
	})
	return "http://" + ln.Addr().String(), cancel, wait
}

func stream(url string, maxTokens int) (*http.Response, error) {
	body := `{"model":"sim-model","stream":true,"max_tokens":` + strconv.Itoa(maxTokens) + `,"messages":[{"role":"user","content":"hi"}]}`
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, url+openai.PathChatCompletions, strings.NewReader(body))
	if err != nil {
		return nil, err
	}
	return (&http.Client{Transport: &http.Transport{DisableKeepAlives: true}}).Do(req)
}

func TestShutdownDrainsInFlightStreams(t *testing.T) {
	t.Parallel()
	slow := simtest.FastCost()
	slow.StepOverhead = 20 * time.Millisecond // 20 tokens take about 400ms
	url, cancel, wait := start(t, slow, 10*time.Second)

	resp, err := stream(url, 20)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	first := make([]byte, 1)
	if _, err := resp.Body.Read(first); err != nil {
		t.Fatalf("stream did not start: %v", err)
	}

	cancel() // begin shutdown with the stream in flight

	rest, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("in-flight stream was cut off during drain: %v", err)
	}
	if !strings.Contains(string(rest), "[DONE]") {
		t.Error("in-flight stream did not run to completion during drain")
	}
	if err := wait(); err != nil {
		t.Errorf("Serve() = %v, want nil after a clean drain", err)
	}
	if late, err := stream(url, 1); err == nil {
		late.Body.Close()
		t.Error("router accepted a new request after shutdown")
	}
}

func TestShutdownTimeoutCutsOffLongStreams(t *testing.T) {
	t.Parallel()
	slow := simtest.FastCost()
	slow.StepOverhead = 50 * time.Millisecond // 99 tokens take about 5s
	url, cancel, wait := start(t, slow, 100*time.Millisecond)

	resp, err := stream(url, 99)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if _, err := resp.Body.Read(make([]byte, 1)); err != nil {
		t.Fatalf("stream did not start: %v", err)
	}
	began := time.Now()
	cancel()
	_, _ = io.ReadAll(resp.Body)
	if err := wait(); err != nil {
		t.Errorf("Serve() = %v, want nil", err)
	}
	if elapsed := time.Since(began); elapsed > 3*time.Second {
		t.Errorf("shutdown took %v; the 100ms drain timeout should cut the stream off", elapsed)
	}
}

func TestMetricsEndpoint(t *testing.T) {
	t.Parallel()
	url, _, _ := start(t, simtest.FastCost(), time.Second)
	resp, err := stream(url, 3)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	resp.Body.Close()

	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, url+"/metrics", http.NoBody)
	metrics, err := (&http.Client{Transport: &http.Transport{DisableKeepAlives: true}}).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer metrics.Body.Close()
	body, _ := io.ReadAll(metrics.Body)
	for _, want := range []string{
		`switchyard_requests_total{backend="sim",code="200",policy="prefix_affinity"} 1`,
		"switchyard_ttft_seconds_count",
		"switchyard_route_decision_seconds_count",
		`switchyard_backend_healthy{backend="sim"} 1`,
		`switchyard_backend_in_flight{backend="sim"} 0`,
		"switchyard_prefix_index_entries",
		"switchyard_admission_queued 0",
		"go_goroutines",
	} {
		if !strings.Contains(string(body), want) {
			t.Errorf("/metrics lacks %q", want)
		}
	}
}
