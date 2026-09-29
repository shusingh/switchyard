package loadgen

import (
	"context"
	"math"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"go.uber.org/goleak"

	"github.com/shusingh/switchyard/internal/sim"
	"github.com/shusingh/switchyard/internal/sim/simtest"
	"github.com/shusingh/switchyard/internal/workload"
)

func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}

func TestRunAgainstSimulatedEngine(t *testing.T) {
	t.Parallel()
	srv := simtest.Start(t, sim.Options{Model: "m", Cost: simtest.FastCost()})
	cfg := workload.DefaultAgentConfig()
	cfg.Duration = 2 * time.Second
	cfg.SessionsPerSecond = 5
	cfg.SystemWords, cfg.TaskWords = 200, 20
	cfg.MinTurns, cfg.MaxTurns = 2, 3
	cfg.MinToolResultWords, cfg.MaxToolResultWords = 10, 30
	cfg.OutputTokens = 5
	cfg.MedianThinkTime, cfg.LongPauseProbability = 10*time.Millisecond, 0
	w, err := workload.Agent(cfg)
	if err != nil {
		t.Fatal(err)
	}

	var mu sync.Mutex
	var records []Record
	err = Run(context.Background(), Config{BaseURL: srv.URL, Model: "m"}, w, func(r Record) {
		mu.Lock()
		defer mu.Unlock()
		records = append(records, r)
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if len(records) != w.Requests() {
		t.Fatalf("got %d records, want %d", len(records), w.Requests())
	}
	for _, r := range records {
		if !r.OK() {
			t.Fatalf("request failed: %+v", r)
		}
		if r.CompletionTokens != 5 || r.TTFTMS <= 0 || r.E2EMS < r.TTFTMS || r.SentMS < r.ScheduledMS-1 {
			t.Errorf("implausible record %+v", r)
		}
	}
	// Sessions of the same app reuse the system prompt, so later requests
	// hit the engine's prefix cache.
	if st := srv.Engine.Stats(); st.CachedPromptTokens == 0 {
		t.Error("engine saw no prefix cache hits for a shared-prefix workload")
	}
}

func TestRunRecordsHTTPErrors(t *testing.T) {
	t.Parallel()
	srv := simtest.Start(t, sim.Options{Model: "m", Cost: simtest.FastCost()})
	w := &workload.Workload{Sessions: []workload.Session{{ID: "s", Turns: []workload.Turn{
		{Prompt: []workload.Segment{{Seed: 1, Words: 5}}, OutputTokens: 2},
		{Prompt: []workload.Segment{{Seed: 1, Words: 5}}, OutputTokens: 2},
	}}}}
	var records []Record
	err := Run(context.Background(), Config{BaseURL: srv.URL, Model: "unknown-model"}, w, func(r Record) { records = append(records, r) })
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 {
		t.Fatalf("got %d records, want 1: a session stops after a failed turn", len(records))
	}
	if records[0].Status != http.StatusNotFound || records[0].OK() {
		t.Errorf("record = %+v, want a failed 404", records[0])
	}
}

func TestSummarize(t *testing.T) {
	t.Parallel()
	var records []Record
	for i := 1; i <= 100; i++ {
		records = append(records, Record{
			SentMS: float64(i * 10), TTFTMS: float64(i), E2EMS: float64(i) + 100,
			TPOTMS: 5, CompletionTokens: 10, Status: http.StatusOK,
		})
	}
	records = append(records, Record{SentMS: 5, Status: http.StatusServiceUnavailable, Error: "status 503"})

	s := Summarize(records, 50*time.Millisecond)
	if s.Requests != 101 || s.Succeeded != 100 || s.Failed != 1 {
		t.Errorf("counts = %d/%d/%d, want 101/100/1", s.Requests, s.Succeeded, s.Failed)
	}
	if math.Abs(s.TTFT.P50-50.5) > 1e-9 || s.TTFT.Max != 100 {
		t.Errorf("TTFT = %+v, want p50 50.5 and max 100", s.TTFT)
	}
	// 50 of 101 requests succeeded with TTFT <= 50ms.
	if want := 50.0 / 101; math.Abs(s.Goodput-want) > 1e-9 {
		t.Errorf("goodput = %v, want %v", s.Goodput, want)
	}
	if s.OutputTokensPerSec <= 0 {
		t.Errorf("output tokens per second = %v, want positive", s.OutputTokensPerSec)
	}
}

func TestQuantile(t *testing.T) {
	t.Parallel()
	v := []float64{10, 20, 30, 40, 50}
	for _, tt := range []struct{ q, want float64 }{{0, 10}, {0.5, 30}, {1, 50}, {0.9, 46}} {
		if got := quantile(v, tt.q); math.Abs(got-tt.want) > 1e-9 {
			t.Errorf("quantile(%v) = %v, want %v", tt.q, got, tt.want)
		}
	}
}

func TestParseCacheCounters(t *testing.T) {
	t.Parallel()
	text := `# TYPE vllm:prefix_cache_queries_total counter
vllm:prefix_cache_queries_total{engine="0",model_name="m"} 1000.0
vllm:prefix_cache_hits_total{engine="0",model_name="m"} 250.0
vllm:prefix_cache_hits_total_created{engine="0"} 1.7e9
vllm:num_requests_running{engine="0"} 3
`
	c, err := parseCacheCounters(strings.NewReader(text))
	if err != nil {
		t.Fatal(err)
	}
	// The _created series shares the prefix of the hits counter; it must not
	// be added in. Prometheus names it with a suffix after "_total".
	if c.Queries != 1000 || c.Hits != 250 {
		t.Errorf("counters = %+v, want queries 1000 and hits 250", c)
	}
	if got := HitRate(CacheCounters{Queries: 1000, Hits: 250}, CacheCounters{Queries: 3000, Hits: 1250}); got != 0.5 {
		t.Errorf("HitRate = %v, want 0.5", got)
	}
}
