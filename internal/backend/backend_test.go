package backend

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/shusingh/switchyard/internal/config"
)

func discardLogger() *slog.Logger { return slog.New(slog.DiscardHandler) }

// flakyServer serves /health with a status the test can change.
type flakyServer struct {
	*httptest.Server
	status atomic.Int32
}

func newFlakyServer(t *testing.T) *flakyServer {
	t.Helper()
	fs := &flakyServer{}
	fs.status.Store(http.StatusOK)
	fs.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(int(fs.status.Load()))
	}))
	t.Cleanup(fs.Close)
	return fs
}

func newTestPool(t *testing.T, urls ...string) *Pool {
	t.Helper()
	cfgs := make([]config.Backend, len(urls))
	for i, u := range urls {
		cfgs[i] = config.Backend{ID: string(rune('a' + i)), URL: u}
	}
	p, err := NewPool(cfgs, BreakerConfig{})
	if err != nil {
		t.Fatalf("NewPool() error = %v", err)
	}
	return p
}

func testHealthConfig() config.Health {
	return config.Health{
		Path:               "/health",
		Interval:           time.Hour, // tests drive rounds with CheckAll
		Timeout:            time.Second,
		UnhealthyThreshold: 2,
		HealthyThreshold:   2,
	}
}

func TestNewPoolStartsUnhealthy(t *testing.T) {
	t.Parallel()
	p := newTestPool(t, "http://localhost:1", "http://localhost:2")
	if p.AnyHealthy() {
		t.Error("AnyHealthy() = true before any health check, want false")
	}
	if got := len(p.AppendAvailable(nil)); got != 0 {
		t.Errorf("AppendAvailable() returned %d backends, want 0", got)
	}
}

func TestHealthCheckerTransitions(t *testing.T) {
	t.Parallel()
	srv := newFlakyServer(t)
	p := newTestPool(t, srv.URL)
	b := p.Backends()[0]
	hc := NewHealthChecker(p, testHealthConfig(), discardLogger())
	ctx := context.Background()

	// The first successful probe marks the backend healthy immediately.
	hc.CheckAll(ctx)
	if !b.Healthy() || b.Generation() != 1 {
		t.Fatalf("after first probe: healthy=%v generation=%d, want true, 1", b.Healthy(), b.Generation())
	}

	// One failure is below the threshold of two.
	srv.status.Store(http.StatusServiceUnavailable)
	hc.CheckAll(ctx)
	if !b.Healthy() {
		t.Fatal("after 1 failure: healthy=false, want true (threshold is 2)")
	}
	hc.CheckAll(ctx)
	if b.Healthy() {
		t.Fatal("after 2 failures: healthy=true, want false")
	}

	// Recovery needs two consecutive successes and starts a new generation.
	srv.status.Store(http.StatusOK)
	hc.CheckAll(ctx)
	if b.Healthy() {
		t.Fatal("after 1 success: healthy=true, want false (threshold is 2)")
	}
	hc.CheckAll(ctx)
	if !b.Healthy() || b.Generation() != 2 {
		t.Fatalf("after recovery: healthy=%v generation=%d, want true, 2", b.Healthy(), b.Generation())
	}
}

func TestHealthCheckerUnreachableBackend(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.NotFoundHandler())
	url := srv.URL
	srv.Close() // nothing listens at url any more

	p := newTestPool(t, url)
	cfg := testHealthConfig()
	cfg.UnhealthyThreshold = 1
	hc := NewHealthChecker(p, cfg, discardLogger())
	hc.CheckAll(context.Background())
	if p.AnyHealthy() {
		t.Error("unreachable backend is healthy, want unhealthy")
	}
}

func TestHealthCheckerRunStopsOnCancel(t *testing.T) {
	t.Parallel()
	srv := newFlakyServer(t)
	p := newTestPool(t, srv.URL)
	cfg := testHealthConfig()
	cfg.Interval = 10 * time.Millisecond
	cfg.Timeout = 5 * time.Millisecond
	hc := NewHealthChecker(p, cfg, discardLogger())

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		hc.Run(ctx)
		close(done)
	}()
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after context cancellation")
	}
}

func TestTicketLifecycle(t *testing.T) {
	t.Parallel()
	b := newTestPool(t, "http://localhost:1").Backends()[0]

	first := b.Admit(1000)
	second := b.Admit(500)
	if second.QueueTokensAtAdmit != 1000 {
		t.Errorf("second ticket saw %d queued tokens at admission, want 1000", second.QueueTokensAtAdmit)
	}
	if got := b.Load(); got != (Load{InFlight: 2, PendingPrefillTokens: 1500}) {
		t.Fatalf("Load after two admits = %+v", got)
	}

	first.FirstToken()
	first.FirstToken() // idempotent
	if got := b.Load(); got != (Load{InFlight: 2, PendingPrefillTokens: 500, Decoding: 1}) {
		t.Fatalf("Load after first token = %+v", got)
	}

	first.Done()
	second.Done() // done before its first token: its prefill work is released
	second.Done() // idempotent
	second.FirstToken()
	if got := b.Load(); got != (Load{}) {
		t.Errorf("Load after all done = %+v, want zero", got)
	}
}

func TestEndpoint(t *testing.T) {
	t.Parallel()
	b := newTestPool(t, "http://localhost:8001").Backends()[0]
	got := b.Endpoint("/v1/chat/completions", "a=1").String()
	if want := "http://localhost:8001/v1/chat/completions?a=1"; got != want {
		t.Errorf("Endpoint() = %q, want %q", got, want)
	}
}
