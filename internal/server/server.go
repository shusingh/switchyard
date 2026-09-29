// Package server implements Switchyard's client-facing HTTP API: the
// OpenAI-compatible inference endpoints, model listing, and health probes.
//
// An inference request flows through the handler in order: read and bound the
// body, decode the routing fields, pick a healthy backend with the configured
// policy, and forward. See docs/engineering/design.md section 5.
package server

import (
	"errors"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/shusingh/switchyard/internal/backend"
	"github.com/shusingh/switchyard/internal/openai"
	"github.com/shusingh/switchyard/internal/proxy"
	"github.com/shusingh/switchyard/internal/scheduler"
)

// statusClientClosedRequest is logged when the client disconnects before the
// response completes. It is never sent; the connection is already gone. The
// value follows the convention nginx established.
const statusClientClosedRequest = 499

// Options holds the server's dependencies.
type Options struct {
	Pool            *backend.Pool
	Policy          scheduler.Policy
	Proxy           *proxy.Proxy
	Logger          *slog.Logger
	MaxRequestBytes int64
}

// Server serves the HTTP API. It is safe for concurrent use.
type Server struct {
	pool            *backend.Pool
	policy          scheduler.Policy
	proxy           *proxy.Proxy
	logger          *slog.Logger
	maxRequestBytes int64
}

// New returns a Server with the given dependencies.
func New(opts Options) *Server {
	return &Server{
		pool:            opts.Pool,
		policy:          opts.Policy,
		proxy:           opts.Proxy,
		logger:          opts.Logger,
		maxRequestBytes: opts.MaxRequestBytes,
	}
}

// Handler returns the root HTTP handler with all routes and middleware.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST "+openai.PathChatCompletions, s.handleInference)
	mux.HandleFunc("POST "+openai.PathCompletions, s.handleInference)
	mux.HandleFunc("GET "+openai.PathModels, s.handleModels)
	mux.HandleFunc("GET /healthz", handleLiveness)
	mux.HandleFunc("GET /readyz", s.handleReadiness)
	mux.HandleFunc("/", handleNotFound)
	return withRequestID(withRecovery(s.logger, mux))
}

// handleInference routes one completion request to a backend.
func (s *Server) handleInference(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	reqLog := s.logger.With(
		slog.String("request_id", RequestID(r.Context())),
		slog.String("path", r.URL.Path),
	)

	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, s.maxRequestBytes))
	if err != nil {
		if maxErr, ok := errors.AsType[*http.MaxBytesError](err); ok {
			openai.WriteError(w, http.StatusRequestEntityTooLarge, openai.ErrTypeInvalidRequest,
				"request body exceeds "+formatBytes(maxErr.Limit))
			return
		}
		openai.WriteError(w, http.StatusBadRequest, openai.ErrTypeInvalidRequest, "could not read request body")
		return
	}
	fields, err := openai.DecodeRoutingFields(body)
	if err != nil {
		openai.WriteError(w, http.StatusBadRequest, openai.ErrTypeInvalidRequest, err.Error())
		return
	}

	b, ok := s.pick(w, &scheduler.Request{Model: fields.Model})
	if !ok {
		reqLog.Warn("no healthy backend", slog.String("model", fields.Model))
		return
	}

	b.BeginRequest()
	res, err := s.proxy.Forward(w, r, b, body)
	b.EndRequest()

	status := res.Status
	switch {
	case err == nil:
	case r.Context().Err() != nil:
		status = statusClientClosedRequest
	case !res.HeaderWritten:
		status = http.StatusBadGateway
		openai.WriteError(w, status, openai.ErrTypeServer, "backend request failed")
	}

	attrs := []slog.Attr{
		slog.String("model", fields.Model),
		slog.Bool("stream", fields.Stream),
		slog.String("policy", s.policy.Name()),
		slog.String("backend", b.ID()),
		slog.Int("status", status),
		slog.Int("request_bytes", len(body)),
		slog.Int64("response_bytes", res.BytesWritten),
		slog.Duration("duration", time.Since(start)),
		slog.Duration("first_byte", res.FirstByte),
	}
	if res.Streamed {
		attrs = append(attrs, slog.Duration("first_token", res.FirstToken), slog.Int("events", res.Events))
	}
	if res.Usage != nil {
		attrs = append(attrs,
			slog.Int("prompt_tokens", res.Usage.PromptTokens),
			slog.Int("completion_tokens", res.Usage.CompletionTokens))
	}
	level := slog.LevelInfo
	if err != nil && status != statusClientClosedRequest {
		level = slog.LevelWarn
		attrs = append(attrs, slog.String("error", err.Error()))
	}
	reqLog.LogAttrs(r.Context(), level, "request", attrs...)
}

// handleModels proxies the model list from one healthy backend. Every
// backend serves the same model, so any of them can answer.
func (s *Server) handleModels(w http.ResponseWriter, r *http.Request) {
	b, ok := s.pick(w, &scheduler.Request{})
	if !ok {
		return
	}
	b.BeginRequest()
	defer b.EndRequest()
	if res, err := s.proxy.Forward(w, r, b, nil); err != nil && !res.HeaderWritten && r.Context().Err() == nil {
		openai.WriteError(w, http.StatusBadGateway, openai.ErrTypeServer, "backend request failed")
	}
}

// pick chooses a healthy backend, or writes a 503 and returns false.
func (s *Server) pick(w http.ResponseWriter, req *scheduler.Request) (*backend.Backend, bool) {
	candidates := s.pool.AppendHealthy(make([]*backend.Backend, 0, 8))
	b, err := s.policy.Pick(req, candidates)
	if err != nil {
		openai.WriteError(w, http.StatusServiceUnavailable, openai.ErrTypeUnavailable, "no healthy backend available")
		return nil, false
	}
	return b, true
}

// handleLiveness reports that the process is up. It never checks backends:
// a router with no healthy backends should not be restarted.
func handleLiveness(w http.ResponseWriter, _ *http.Request) {
	w.WriteHeader(http.StatusOK)
}

// handleReadiness reports whether the router can serve traffic.
func (s *Server) handleReadiness(w http.ResponseWriter, _ *http.Request) {
	if !s.pool.AnyHealthy() {
		w.WriteHeader(http.StatusServiceUnavailable)
		return
	}
	w.WriteHeader(http.StatusOK)
}

func handleNotFound(w http.ResponseWriter, r *http.Request) {
	openai.WriteError(w, http.StatusNotFound, openai.ErrTypeInvalidRequest, "unknown path "+r.URL.Path)
}
