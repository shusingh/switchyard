package server

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"go.uber.org/goleak"

	"github.com/shusingh/switchyard/internal/backend"
	"github.com/shusingh/switchyard/internal/config"
	"github.com/shusingh/switchyard/internal/openai"
	"github.com/shusingh/switchyard/internal/prefix"
	"github.com/shusingh/switchyard/internal/proxy"
	"github.com/shusingh/switchyard/internal/scheduler"
	"github.com/shusingh/switchyard/internal/sim"
	"github.com/shusingh/switchyard/internal/sim/simtest"
)

func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}

const testModel = "sim-model"

// harness is a router in front of simulated engines, wired exactly as the
// switchyard binary wires it.
type harness struct {
	url     string
	client  *http.Client
	engines []*sim.Engine
}

type harnessOptions struct {
	engines         []sim.Options
	policy          scheduler.Policy
	maxRequestBytes int64
	skipHealthCheck bool
}

func newHarness(t *testing.T, opts harnessOptions) *harness {
	t.Helper()
	h := &harness{}
	var backends []config.Backend
	for i, eo := range opts.engines {
		if eo.Model == "" {
			eo.Model = testModel
		}
		srv := simtest.Start(t, eo)
		h.engines = append(h.engines, srv.Engine)
		backends = append(backends, config.Backend{ID: string(rune('a' + i)), URL: srv.URL})
	}
	pool, err := backend.NewPool(backends)
	if err != nil {
		t.Fatal(err)
	}
	checker := backend.NewHealthChecker(pool, config.Health{
		Path: "/health", Interval: time.Hour, Timeout: time.Second,
		UnhealthyThreshold: 1, HealthyThreshold: 1,
	}, slog.New(slog.DiscardHandler))
	t.Cleanup(checker.CloseIdleConnections)
	if !opts.skipHealthCheck {
		checker.CheckAll(context.Background())
	}

	policy := opts.policy
	if policy == nil {
		policy, _ = scheduler.New(scheduler.PolicyRoundRobin, scheduler.Options{})
	}
	if opts.maxRequestBytes == 0 {
		opts.maxRequestBytes = 1 << 20
	}
	px := proxy.New(config.Proxy{
		DialTimeout: time.Second, ResponseHeaderTimeout: 10 * time.Second,
		StreamIdleTimeout: 10 * time.Second, MaxIdleConnsPerHost: 8,
	})
	t.Cleanup(px.CloseIdleConnections)

	router := httptest.NewServer(New(Options{
		Pool: pool, Policy: policy, Proxy: px, Keyer: testKeyer(), Index: testIndex(len(backends)), Estimator: testEstimator(len(backends)),
		Logger: slog.New(slog.DiscardHandler), MaxRequestBytes: opts.maxRequestBytes,
	}).Handler())
	t.Cleanup(router.Close)

	transport := &http.Transport{}
	t.Cleanup(transport.CloseIdleConnections)
	h.url = router.URL
	h.client = &http.Client{Transport: transport, Timeout: 30 * time.Second}
	return h
}

func testKeyer() *prefix.Keyer { return prefix.NewKeyer(64, 4096) }

func testEstimator(backends int) *scheduler.Estimator {
	return scheduler.NewEstimator(scheduler.EstimatorConfig{
		InitialPrefillTokensPerSecond: 10000,
		Overhead:                      10 * time.Millisecond,
		InitialBytesPerToken:          4,
		BlockBytes:                    64,
	}, backends)
}

func testIndex(backends int) *prefix.Index {
	capacities := make([]int, backends)
	for i := range capacities {
		capacities[i] = 1 << 16
	}
	return prefix.NewIndex(capacities, time.Hour, nil)
}

func (h *harness) post(t *testing.T, ctx context.Context, path, body string) *http.Response {
	t.Helper()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, h.url+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := h.client.Do(req)
	if err != nil {
		t.Fatalf("POST %s: %v", path, err)
	}
	return resp
}

func decodeError(t *testing.T, resp *http.Response) openai.ErrorBody {
	t.Helper()
	var e openai.ErrorResponse
	if err := json.NewDecoder(resp.Body).Decode(&e); err != nil {
		t.Fatalf("decode error body: %v", err)
	}
	return e.Error
}

func fastEngines(n int) []sim.Options {
	opts := make([]sim.Options, n)
	for i := range opts {
		opts[i] = sim.Options{Cost: simtest.FastCost(), DefaultOutputTokens: 4}
	}
	return opts
}

func TestRoundRobinSpreadsRequests(t *testing.T) {
	t.Parallel()
	h := newHarness(t, harnessOptions{engines: fastEngines(2)})
	for range 4 {
		resp := h.post(t, context.Background(), openai.PathChatCompletions,
			`{"model":"sim-model","messages":[{"role":"user","content":"hi"}]}`)
		_, _ = io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status = %d, want 200", resp.StatusCode)
		}
	}
	for i, e := range h.engines {
		if got := e.Stats().Completed; got != 2 {
			t.Errorf("engine %d completed %d requests, want 2", i, got)
		}
	}
}

func TestPrefixAffinityKeepsSharedPrefixOnOneBackend(t *testing.T) {
	t.Parallel()
	policy, _ := scheduler.New(scheduler.PolicyPrefixAffinity, scheduler.Options{})
	h := newHarness(t, harnessOptions{engines: fastEngines(4), policy: policy})
	system := strings.Repeat("Follow the operator's compliance policy exactly. ", 60)
	for i := range 8 {
		body := `{"model":"sim-model","max_tokens":2,"messages":[` +
			`{"role":"system","content":"` + system + `"},` +
			`{"role":"user","content":"question ` + strconv.Itoa(i) + `"}]}`
		resp := h.post(t, context.Background(), openai.PathChatCompletions, body)
		_, _ = io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("request %d: status %d", i, resp.StatusCode)
		}
	}
	served, cached := 0, int64(0)
	for _, e := range h.engines {
		st := e.Stats()
		if st.Completed > 0 {
			served++
		}
		cached += st.CachedPromptTokens
	}
	if served != 1 {
		t.Errorf("requests sharing a prefix went to %d backends, want 1", served)
	}
	if cached == 0 {
		t.Error("the engine saw no prefix cache hits")
	}
}

func TestStreamingThroughRouter(t *testing.T) {
	t.Parallel()
	h := newHarness(t, harnessOptions{engines: fastEngines(1)})
	resp := h.post(t, context.Background(), openai.PathChatCompletions,
		`{"model":"sim-model","stream":true,"max_tokens":5,"stream_options":{"include_usage":true}}`)
	defer resp.Body.Close()
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		t.Fatalf("Content-Type = %q, want text/event-stream", ct)
	}
	if resp.Header.Get(HeaderRequestID) == "" {
		t.Error("response has no request ID header")
	}

	events := openai.NewEventReader(resp.Body, 0)
	tokens, done := 0, false
	var usage *openai.Usage
	for {
		ev, err := events.Next()
		if err != nil {
			break
		}
		data := openai.EventData(ev)
		switch {
		case openai.IsDone(data):
			done = true
		case openai.ChunkHasContent(data):
			tokens++
		}
		if u, ok := openai.ChunkUsage(data); ok {
			usage = &u
		}
	}
	if tokens != 5 || !done {
		t.Errorf("stream had %d content events and done=%v, want 5 and true", tokens, done)
	}
	if usage == nil || usage.CompletionTokens != 5 {
		t.Errorf("usage = %+v, want completion_tokens 5", usage)
	}
}

func TestClientDisconnectCancelsBackend(t *testing.T) {
	t.Parallel()
	slow := simtest.FastCost()
	slow.StepOverhead = 50 * time.Millisecond // one token every 50ms
	h := newHarness(t, harnessOptions{engines: []sim.Options{{Cost: slow, DefaultOutputTokens: 1000}}})
	ctx, cancel := context.WithCancel(context.Background())
	resp := h.post(t, ctx, openai.PathChatCompletions, `{"model":"sim-model","stream":true}`)
	defer resp.Body.Close()
	buf := make([]byte, 64)
	if _, err := resp.Body.Read(buf); err != nil {
		t.Fatalf("read first bytes: %v", err)
	}
	cancel()

	deadline := time.Now().Add(5 * time.Second)
	for h.engines[0].Stats().Cancelled == 0 {
		if time.Now().After(deadline) {
			t.Fatalf("engine stats %+v: the backend never saw the cancellation", h.engines[0].Stats())
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestNoHealthyBackends(t *testing.T) {
	t.Parallel()
	h := newHarness(t, harnessOptions{engines: fastEngines(2), skipHealthCheck: true})
	resp := h.post(t, context.Background(), openai.PathChatCompletions, `{"model":"sim-model"}`)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503", resp.StatusCode)
	}
	if e := decodeError(t, resp); e.Type != openai.ErrTypeUnavailable {
		t.Errorf("error type = %q, want %q", e.Type, openai.ErrTypeUnavailable)
	}

	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, h.url+"/readyz", http.NoBody)
	ready, err := h.client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	ready.Body.Close()
	if ready.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("/readyz status = %d, want 503", ready.StatusCode)
	}
}

func TestRequestValidation(t *testing.T) {
	t.Parallel()
	h := newHarness(t, harnessOptions{engines: fastEngines(1), maxRequestBytes: 256})
	tests := []struct {
		name       string
		path       string
		body       string
		wantStatus int
	}{
		{"malformed json", openai.PathChatCompletions, `{"model":`, http.StatusBadRequest},
		{"missing model", openai.PathChatCompletions, `{"messages":[]}`, http.StatusBadRequest},
		{"body too large", openai.PathCompletions, `{"model":"sim-model","prompt":"` + strings.Repeat("x", 512) + `"}`, http.StatusRequestEntityTooLarge},
		{"unknown path", "/v1/embeddings", `{"model":"sim-model"}`, http.StatusNotFound},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp := h.post(t, context.Background(), tt.path, tt.body)
			defer resp.Body.Close()
			if resp.StatusCode != tt.wantStatus {
				t.Errorf("status = %d, want %d", resp.StatusCode, tt.wantStatus)
			}
			if e := decodeError(t, resp); e.Message == "" {
				t.Error("error body has no message")
			}
		})
	}
}

func TestUnreachableBackendReturnsBadGateway(t *testing.T) {
	t.Parallel()
	// A backend that passed its health check and then died.
	engine := sim.NewEngine(sim.Options{Model: testModel})
	dead := httptest.NewServer(engine.Handler())
	pool, _ := backend.NewPool([]config.Backend{{ID: "a", URL: dead.URL}})
	checker := backend.NewHealthChecker(pool, config.Health{Path: "/health", Interval: time.Hour, Timeout: time.Second, UnhealthyThreshold: 1, HealthyThreshold: 1}, slog.New(slog.DiscardHandler))
	checker.CheckAll(context.Background())
	checker.CloseIdleConnections()
	dead.Close()

	policy, _ := scheduler.New(scheduler.PolicyRoundRobin, scheduler.Options{})
	px := proxy.New(config.Proxy{DialTimeout: time.Second, ResponseHeaderTimeout: time.Second, StreamIdleTimeout: time.Second, MaxIdleConnsPerHost: 1})
	t.Cleanup(px.CloseIdleConnections)
	router := httptest.NewServer(New(Options{
		Pool: pool, Policy: policy, Proxy: px, Keyer: testKeyer(), Index: testIndex(1), Estimator: testEstimator(1),
		Logger: slog.New(slog.DiscardHandler), MaxRequestBytes: 1 << 20,
	}).Handler())
	t.Cleanup(router.Close)

	req, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, router.URL+openai.PathChatCompletions, strings.NewReader(`{"model":"sim-model"}`))
	transport := &http.Transport{}
	t.Cleanup(transport.CloseIdleConnections)
	resp, err := (&http.Client{Transport: transport}).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadGateway {
		t.Errorf("status = %d, want 502", resp.StatusCode)
	}
}

func TestRequestIDHandling(t *testing.T) {
	t.Parallel()
	h := newHarness(t, harnessOptions{engines: fastEngines(1)})
	tests := []struct {
		name     string
		clientID string
		keep     bool
	}{
		{"well-formed id is kept", "trace-123.abc_DEF", true},
		{"id with unsafe characters is replaced", "bad id <script>", false},
		{"overlong id is replaced", strings.Repeat("a", 65), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, h.url+"/healthz", http.NoBody)
			req.Header[HeaderRequestID] = []string{tt.clientID}
			resp, err := h.client.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			resp.Body.Close()
			got := resp.Header.Get(HeaderRequestID)
			if tt.keep && got != tt.clientID {
				t.Errorf("request ID = %q, want the client's %q", got, tt.clientID)
			}
			if !tt.keep && (got == tt.clientID || !validRequestID(got)) {
				t.Errorf("request ID = %q, want a fresh valid ID", got)
			}
		})
	}
}

// panicPolicy simulates a bug in a routing policy.
type panicPolicy struct{}

func (panicPolicy) Name() string { return "panic" }
func (panicPolicy) Pick(*scheduler.Request, []*backend.Backend) (*backend.Backend, error) {
	panic("policy bug")
}

func TestPanicIsRecovered(t *testing.T) {
	t.Parallel()
	h := newHarness(t, harnessOptions{engines: fastEngines(1), policy: panicPolicy{}})
	resp := h.post(t, context.Background(), openai.PathChatCompletions, `{"model":"sim-model"}`)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", resp.StatusCode)
	}
	// The server survives and keeps serving.
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, h.url+"/healthz", http.NoBody)
	live, err := h.client.Do(req)
	if err != nil {
		t.Fatalf("server did not survive the panic: %v", err)
	}
	live.Body.Close()
}

func TestModelsIsProxied(t *testing.T) {
	t.Parallel()
	h := newHarness(t, harnessOptions{engines: fastEngines(1)})
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, h.url+openai.PathModels, http.NoBody)
	resp, err := h.client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), testModel) {
		t.Errorf("GET /v1/models = %d %s, want 200 listing %s", resp.StatusCode, body, testModel)
	}
}
