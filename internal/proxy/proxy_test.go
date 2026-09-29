package proxy

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/shusingh/switchyard/internal/backend"
	"github.com/shusingh/switchyard/internal/config"
)

func testConfig() config.Proxy {
	return config.Proxy{
		DialTimeout:           time.Second,
		ResponseHeaderTimeout: 5 * time.Second,
		StreamIdleTimeout:     5 * time.Second,
		MaxIdleConnsPerHost:   8,
	}
}

func backendFor(t *testing.T, url string) *backend.Backend {
	t.Helper()
	p, err := backend.NewPool([]config.Backend{{ID: "b0", URL: url}})
	if err != nil {
		t.Fatal(err)
	}
	return p.Backends()[0]
}

type forwardOutcome struct {
	res Result
	err error
}

// frontServer runs a server whose handler forwards every request to target
// through p, and reports each Forward outcome on the returned channel.
func frontServer(t *testing.T, p *Proxy, target *backend.Backend) (*httptest.Server, <-chan forwardOutcome) {
	t.Helper()
	outcomes := make(chan forwardOutcome, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		res, err := p.Forward(w, r, target, body, nil)
		outcomes <- forwardOutcome{res, err}
	}))
	t.Cleanup(srv.Close)
	return srv, outcomes
}

func post(t *testing.T, ctx context.Context, url, body string) *http.Response {
	t.Helper()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url+"/v1/chat/completions", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultTransport.RoundTrip(req)
	if err != nil {
		t.Fatalf("request to front server: %v", err)
	}
	return resp
}

func TestForwardStreamIsRelayedByteForByte(t *testing.T) {
	t.Parallel()
	upstream := "data: {\"choices\":[{\"delta\":{\"role\":\"assistant\",\"content\":\"\"}}]}\n\n" +
		": keep-alive\n\n" +
		"data: {\"choices\":[{\"delta\":{\"content\":\"Hi\"}}]}\n\n" +
		"data: {\"choices\":[],\"usage\":{\"prompt_tokens\":5,\"completion_tokens\":1,\"total_tokens\":6}}\n\n" +
		"data: [DONE]\n\n"
	be := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, upstream)
	}))
	t.Cleanup(be.Close)

	front, outcomes := frontServer(t, New(testConfig()), backendFor(t, be.URL))
	resp := post(t, context.Background(), front.URL, `{"model":"m","stream":true}`)
	got, _ := io.ReadAll(resp.Body)
	resp.Body.Close()

	if string(got) != upstream {
		t.Errorf("relayed stream differs from upstream:\n got %q\nwant %q", got, upstream)
	}
	out := <-outcomes
	if out.err != nil {
		t.Fatalf("Forward() error = %v", out.err)
	}
	if !out.res.Streamed || out.res.Events != 5 {
		t.Errorf("Result streamed=%v events=%d, want true, 5", out.res.Streamed, out.res.Events)
	}
	if out.res.FirstToken == 0 {
		t.Error("Result.FirstToken = 0, want the time of the first content chunk")
	}
	if out.res.Usage == nil || out.res.Usage.TotalTokens != 6 {
		t.Errorf("Result.Usage = %+v, want total_tokens 6", out.res.Usage)
	}
}

func TestForwardNonStreamingPreservesStatusAndHeaders(t *testing.T) {
	t.Parallel()
	be := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Accept-Encoding") != "" {
			t.Errorf("upstream saw Accept-Encoding %q, want it removed", r.Header.Get("Accept-Encoding"))
		}
		if r.Header.Get("X-Drop-Me") != "" {
			t.Error("upstream saw a header named in Connection, want it removed")
		}
		if r.Header.Get("X-Keep-Me") != "yes" {
			t.Error("upstream did not receive an end-to-end header")
		}
		if r.Header.Get("Authorization") != "" {
			t.Error("upstream received the client's credentials")
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Backend-Header", "present")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, `{"error":{"message":"bad","type":"invalid_request_error"}}`)
	}))
	t.Cleanup(be.Close)

	front, outcomes := frontServer(t, New(testConfig()), backendFor(t, be.URL))
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, front.URL+"/v1/chat/completions", strings.NewReader(`{"model":"m"}`))
	req.Header.Set("Accept-Encoding", "gzip")
	req.Header.Set("Connection", "X-Drop-Me")
	req.Header.Set("X-Drop-Me", "1")
	req.Header.Set("X-Keep-Me", "yes")
	req.Header.Set("Authorization", "Bearer client-secret")
	resp, err := http.DefaultTransport.RoundTrip(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want %d", resp.StatusCode, http.StatusBadRequest)
	}
	if resp.Header.Get("X-Backend-Header") != "present" {
		t.Error("backend response header was not relayed")
	}
	if !strings.Contains(string(body), "invalid_request_error") {
		t.Errorf("body = %q, want the backend's error body", body)
	}
	if out := <-outcomes; out.err != nil || out.res.Streamed {
		t.Errorf("Forward() = %+v, %v; want non-streamed success", out.res, out.err)
	}
}

func TestForwardClientCancelReachesBackend(t *testing.T) {
	t.Parallel()
	upstreamCancelled := make(chan struct{})
	be := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"a\"}}]}\n\n")
		_ = http.NewResponseController(w).Flush()
		<-r.Context().Done() // a real engine would keep generating until aborted
		close(upstreamCancelled)
	}))
	t.Cleanup(be.Close)

	front, outcomes := frontServer(t, New(testConfig()), backendFor(t, be.URL))
	ctx, cancel := context.WithCancel(context.Background())
	resp := post(t, ctx, front.URL, `{"model":"m","stream":true}`)
	buf := make([]byte, 16)
	if _, err := resp.Body.Read(buf); err != nil {
		t.Fatalf("reading first event: %v", err)
	}
	cancel()
	resp.Body.Close()

	select {
	case <-upstreamCancelled:
	case <-time.After(5 * time.Second):
		t.Fatal("backend request was not cancelled after the client disconnected")
	}
	if out := <-outcomes; out.err == nil {
		t.Error("Forward() error = nil after client disconnect, want an error")
	}
}

func TestForwardUnreachableBackend(t *testing.T) {
	t.Parallel()
	dead := httptest.NewServer(http.NotFoundHandler())
	url := dead.URL
	dead.Close()

	front, outcomes := frontServer(t, New(testConfig()), backendFor(t, url))
	resp := post(t, context.Background(), front.URL, `{"model":"m"}`)
	resp.Body.Close()

	out := <-outcomes
	if !errors.Is(out.err, ErrUpstreamUnavailable) {
		t.Errorf("Forward() error = %v, want %v", out.err, ErrUpstreamUnavailable)
	}
	if out.res.HeaderWritten {
		t.Error("Result.HeaderWritten = true, want false so the caller can send an error")
	}
}

func TestForwardStreamIdleTimeout(t *testing.T) {
	t.Parallel()
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	be := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"a\"}}]}\n\n")
		_ = http.NewResponseController(w).Flush()
		select { // stall without closing the stream
		case <-release:
		case <-r.Context().Done():
		}
	}))
	t.Cleanup(be.Close)

	cfg := testConfig()
	cfg.StreamIdleTimeout = 100 * time.Millisecond
	front, outcomes := frontServer(t, New(cfg), backendFor(t, be.URL))
	resp := post(t, context.Background(), front.URL, `{"model":"m","stream":true}`)
	_, _ = io.ReadAll(resp.Body)
	resp.Body.Close()

	out := <-outcomes
	if !errors.Is(out.err, ErrStreamIdle) {
		t.Errorf("Forward() error = %v, want %v", out.err, ErrStreamIdle)
	}
	if out.res.Events != 1 {
		t.Errorf("Result.Events = %d, want 1 (the event before the stall)", out.res.Events)
	}
}
