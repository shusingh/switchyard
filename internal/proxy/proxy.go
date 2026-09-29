// Package proxy forwards a request to one backend and relays the response to
// the client. Streaming responses are relayed one server-sent event at a time
// and flushed immediately, so tokens reach the client as they are generated.
//
// See docs/engineering/design.md section 11.
package proxy

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/shusingh/switchyard/internal/backend"
	"github.com/shusingh/switchyard/internal/config"
	"github.com/shusingh/switchyard/internal/openai"
)

var (
	// ErrUpstreamUnavailable means no response was received from the backend:
	// the connection failed or response headers never arrived. Nothing has
	// been written to the client.
	ErrUpstreamUnavailable = errors.New("upstream unavailable")

	// ErrStreamIdle means a streaming response produced no event within the
	// idle timeout and was aborted.
	ErrStreamIdle = errors.New("upstream stream idle timeout")

	// ErrRetryableStatus means the backend answered 502, 503, or 504 and
	// ForwardOptions.HoldRetryableStatus was set, so nothing was written to
	// the client and the request may be retried elsewhere.
	ErrRetryableStatus = errors.New("upstream returned a retryable status")
)

// ForwardOptions adjusts one Forward call.
type ForwardOptions struct {
	// OnFirstToken, if not nil, is called once, as soon as the first event
	// carrying generated text arrives on a streaming response.
	OnFirstToken func()
	// HoldRetryableStatus makes a 502, 503, or 504 response return
	// ErrRetryableStatus instead of being relayed, so the caller can retry.
	HoldRetryableStatus bool
}

// IsRetryableStatus reports whether a backend status indicates a transient
// failure worth retrying on another backend.
func IsRetryableStatus(status int) bool {
	return status == http.StatusBadGateway || status == http.StatusServiceUnavailable || status == http.StatusGatewayTimeout
}

// hopByHopHeaders are connection-scoped and must not be forwarded
// (RFC 9110 section 7.6.1). Keys are in canonical form.
var hopByHopHeaders = map[string]bool{
	"Connection": true, "Keep-Alive": true, "Proxy-Authenticate": true,
	"Proxy-Authorization": true, "Proxy-Connection": true, "Te": true,
	"Trailer": true, "Transfer-Encoding": true, "Upgrade": true,
}

// Proxy forwards requests to backends. It is safe for concurrent use.
type Proxy struct {
	client            *http.Client
	streamIdleTimeout time.Duration
}

// New returns a Proxy configured by cfg.
func New(cfg config.Proxy) *Proxy {
	transport := &http.Transport{
		// Model traffic never goes through an environment-configured proxy.
		Proxy: nil,
		DialContext: (&net.Dialer{
			Timeout:   cfg.DialTimeout,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		TLSHandshakeTimeout:   cfg.DialTimeout,
		ResponseHeaderTimeout: cfg.ResponseHeaderTimeout,
		MaxIdleConnsPerHost:   cfg.MaxIdleConnsPerHost,
		IdleConnTimeout:       90 * time.Second,
		// Relay bytes exactly as the backend sent them. Transparent
		// decompression would also buffer server-sent events.
		DisableCompression: true,
	}
	// No client-wide Timeout: streams legitimately run for minutes. Each phase
	// has its own bound (dial, response headers, stream idle).
	return &Proxy{
		client:            &http.Client{Transport: transport},
		streamIdleTimeout: cfg.StreamIdleTimeout,
	}
}

// CloseIdleConnections closes idle upstream connections. It is called on
// shutdown so no connection outlives the proxy.
func (p *Proxy) CloseIdleConnections() { p.client.CloseIdleConnections() }

// Result describes one forwarded request.
type Result struct {
	// Status is the backend's response status, or 0 if none arrived.
	Status int
	// HeaderWritten is true once the response status was sent to the client.
	// After that the request can neither be retried nor answered with an
	// error body.
	HeaderWritten bool
	// Streamed is true for server-sent event responses.
	Streamed bool
	// FirstByte is the time from forwarding to receiving response headers.
	FirstByte time.Duration
	// FirstToken is the time from forwarding to the first event carrying
	// generated text. Zero for non-streaming responses or if no token arrived.
	FirstToken time.Duration
	// Events is the number of events relayed on a streaming response.
	Events int
	// BytesWritten counts response body bytes delivered to the client.
	BytesWritten int64
	// Usage holds the token counts reported by a streaming response's final
	// usage event, if the client requested one.
	Usage *openai.Usage
}

// Forward sends r, with the already-read body, to b and relays the response to
// w. The upstream request is bound to r's context, so a client disconnect
// cancels the backend's work.
//
// When Forward returns an error, Result.HeaderWritten tells the caller whether
// it can still write an error response.
func (p *Proxy) Forward(w http.ResponseWriter, r *http.Request, b *backend.Backend, body []byte, opts ForwardOptions) (Result, error) {
	start := time.Now()
	ctx, cancel := context.WithCancelCause(r.Context())
	defer cancel(nil)

	// The host comes from operator configuration and the path is one of the
	// routes the server registers, so client input cannot redirect the request.
	target := b.Endpoint(r.URL.Path, r.URL.RawQuery).String()
	req, err := http.NewRequestWithContext(ctx, r.Method, target, bytes.NewReader(body)) //nolint:gosec // G704: see above
	if err != nil {
		return Result{}, fmt.Errorf("build upstream request: %w", err)
	}
	copyHeaders(req.Header, r.Header)
	// Compressed event streams cannot be relayed event by event.
	req.Header.Del("Accept-Encoding")
	// Clients authenticate to the router; their credentials never reach a
	// backend, which gets its own key if it needs one.
	req.Header.Del("Authorization")
	if key := b.APIKey(); key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}

	resp, err := p.client.Do(req) //nolint:gosec // G704: target is operator-configured; see above
	if err != nil {
		return Result{}, fmt.Errorf("%w: %s: %w", ErrUpstreamUnavailable, b.ID(), err)
	}
	defer resp.Body.Close()

	res := Result{Status: resp.StatusCode, FirstByte: time.Since(start), Streamed: isEventStream(resp.Header)}
	if opts.HoldRetryableStatus && IsRetryableStatus(resp.StatusCode) {
		// Drain a little so the connection can be reused; the body of an
		// error response is small.
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
		return res, fmt.Errorf("%w: %s: %d", ErrRetryableStatus, b.ID(), resp.StatusCode)
	}
	copyHeaders(w.Header(), resp.Header)
	if res.Streamed {
		// The body length is unknown while streaming.
		w.Header().Del("Content-Length")
	}
	w.WriteHeader(resp.StatusCode)
	res.HeaderWritten = true

	if !res.Streamed {
		n, err := io.Copy(w, resp.Body)
		res.BytesWritten = n
		if err != nil {
			return res, fmt.Errorf("relay response body: %w", err)
		}
		return res, nil
	}
	err = p.relayEvents(ctx, cancel, w, resp.Body, start, opts.OnFirstToken, &res)
	return res, err
}

// relayEvents copies events from body to w, flushing after each one. It
// aborts the upstream request if no event arrives within the idle timeout.
func (p *Proxy) relayEvents(ctx context.Context, cancel context.CancelCauseFunc, w http.ResponseWriter, body io.Reader, start time.Time, onFirstToken func(), res *Result) error {
	rc := http.NewResponseController(w)
	idle := time.AfterFunc(p.streamIdleTimeout, func() { cancel(ErrStreamIdle) })
	defer idle.Stop()

	events := openai.NewEventReader(body, 0)
	for {
		ev, readErr := events.Next()
		idle.Reset(p.streamIdleTimeout)
		if len(ev) > 0 {
			if err := p.relayEvent(rc, w, ev, start, onFirstToken, res); err != nil {
				return err
			}
		}
		switch {
		case readErr == nil:
			continue
		case errors.Is(readErr, io.EOF):
			return nil
		case errors.Is(context.Cause(ctx), ErrStreamIdle):
			return ErrStreamIdle
		default:
			return fmt.Errorf("read upstream stream: %w", readErr)
		}
	}
}

func (p *Proxy) relayEvent(rc *http.ResponseController, w io.Writer, ev []byte, start time.Time, onFirstToken func(), res *Result) error {
	data := openai.EventData(ev)
	if res.FirstToken == 0 && openai.ChunkHasContent(data) {
		res.FirstToken = time.Since(start)
		if onFirstToken != nil {
			onFirstToken()
		}
	}
	if u, ok := openai.ChunkUsage(data); ok {
		res.Usage = &u
	}
	n, err := w.Write(ev)
	res.BytesWritten += int64(n)
	if err != nil {
		return fmt.Errorf("write event to client: %w", err)
	}
	if err := rc.Flush(); err != nil {
		return fmt.Errorf("flush event to client: %w", err)
	}
	res.Events++
	return nil
}

// copyHeaders copies end-to-end headers from src to dst, dropping hop-by-hop
// headers and any header the Connection header names.
func copyHeaders(dst, src http.Header) {
	var named []string // headers listed in Connection; almost always none
	for _, v := range src.Values("Connection") {
		for name := range strings.SplitSeq(v, ",") {
			named = append(named, http.CanonicalHeaderKey(strings.TrimSpace(name)))
		}
	}
	for k, vs := range src {
		if hopByHopHeaders[k] || slices.Contains(named, k) {
			continue
		}
		dst[k] = append(dst[k], vs...)
	}
}

func isEventStream(h http.Header) bool {
	mediaType, _, err := mime.ParseMediaType(h.Get("Content-Type"))
	return err == nil && mediaType == "text/event-stream"
}
