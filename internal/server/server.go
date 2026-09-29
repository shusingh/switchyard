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
	"math"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/shusingh/switchyard/internal/admission"
	"github.com/shusingh/switchyard/internal/backend"
	"github.com/shusingh/switchyard/internal/openai"
	"github.com/shusingh/switchyard/internal/prefix"
	"github.com/shusingh/switchyard/internal/proxy"
	"github.com/shusingh/switchyard/internal/scheduler"
)

// statusClientClosedRequest is logged when the client disconnects before the
// response completes. It is never sent; the connection is already gone. The
// value follows the convention nginx established.
const statusClientClosedRequest = 499

// Options holds the server's dependencies.
type Options struct {
	Pool   *backend.Pool
	Policy scheduler.Policy
	Proxy  *proxy.Proxy
	// Keyer and Index track which backends hold which prompt prefixes. They
	// are maintained under every policy, including cache-blind ones, so the
	// per-request overhead is identical when policies are compared.
	Keyer *prefix.Keyer
	Index *prefix.Index
	// Estimator predicts time to first token and learns from outcomes. Like
	// the index, it runs under every policy.
	Estimator       *scheduler.Estimator
	Logger          *slog.Logger
	MaxRequestBytes int64
	// ExplainHeaders adds X-Switchyard-* headers describing each routing
	// decision to responses.
	ExplainHeaders bool
	// Admission decides whether and when each request may run.
	Admission *admission.Controller
	// TrustTenantHeader identifies tenants by the X-Tenant-ID header instead
	// of the bearer token.
	TrustTenantHeader bool
	// MaxRetries is how many times a request that failed transiently, before
	// anything reached the client, may be retried on another backend.
	MaxRetries int
	// RetryBudgetRatio caps retries as a fraction of requests.
	RetryBudgetRatio float64
}

// Server serves the HTTP API. It is safe for concurrent use.
type Server struct {
	pool            *backend.Pool
	policy          scheduler.Policy
	proxy           *proxy.Proxy
	keyer           *prefix.Keyer
	index           *prefix.Index
	estimator       *scheduler.Estimator
	logger          *slog.Logger
	maxRequestBytes int64
	explainHeaders  bool
	admission       *admission.Controller
	trustTenantHdr  bool
	maxRetries      int
	retries         *retryBudget
}

// New returns a Server with the given dependencies.
func New(opts Options) *Server {
	return &Server{
		pool:            opts.Pool,
		policy:          opts.Policy,
		proxy:           opts.Proxy,
		keyer:           opts.Keyer,
		index:           opts.Index,
		estimator:       opts.Estimator,
		logger:          opts.Logger,
		maxRequestBytes: opts.MaxRequestBytes,
		explainHeaders:  opts.ExplainHeaders,
		admission:       opts.Admission,
		trustTenantHdr:  opts.TrustTenantHeader,
		maxRetries:      opts.MaxRetries,
		retries:         newRetryBudget(opts.RetryBudgetRatio),
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
	body, ok := s.readBody(w, r)
	if !ok {
		return
	}
	parsed, err := s.keyer.Parse(body, r.URL.Path == openai.PathChatCompletions)
	if err != nil {
		openai.WriteError(w, http.StatusBadRequest, openai.ErrTypeInvalidRequest, err.Error())
		return
	}

	promptTokens := s.estimator.PromptTokens(parsed.CanonicalBytes)
	grant, ok := s.admit(w, r, promptTokens+outputEstimate(parsed.Fields))
	if !ok {
		return
	}
	queueWait := time.Since(start)
	// Route after admission, so the decision reflects load when the request
	// actually runs rather than when it arrived.
	route := s.newRoute(&parsed, promptTokens)
	a, ok := s.forward(w, r, body, &parsed, route)
	if !ok {
		grant.Release(0)
		openai.WriteError(w, http.StatusServiceUnavailable, openai.ErrTypeUnavailable, "no healthy backend available")
		s.logger.Warn("no healthy backend",
			slog.String("request_id", RequestID(r.Context())),
			slog.String("model", route.Model))
		return
	}
	actualCost := -1.0 // keep the estimate unless usage says otherwise
	if a.res.Usage != nil {
		actualCost = float64(a.res.Usage.PromptTokens + a.res.Usage.CompletionTokens)
	}
	grant.Release(actualCost)

	status := a.res.Status
	switch {
	case a.err == nil:
	case r.Context().Err() != nil:
		status = statusClientClosedRequest
	case !a.res.HeaderWritten:
		status = http.StatusBadGateway
		if errors.Is(a.err, proxy.ErrRetryableStatus) {
			status = a.res.Status // retries exhausted: report what the backend said
		}
		openai.WriteError(w, status, openai.ErrTypeServer, "backend request failed")
	}
	s.logRequest(r, requestLog{
		start: start, queueWait: queueWait, tenant: grant.Tenant(), bodyBytes: len(body),
		parsed: &parsed, route: route, backend: a.backend, uncached: a.uncached, retries: a.retries,
		status: status, res: &a.res, err: a.err,
	})
}

// Explain headers, sent when enabled. See Options.ExplainHeaders.
const (
	HeaderBackend         = "X-Switchyard-Backend"
	HeaderMatchedBlocks   = "X-Switchyard-Matched-Blocks"
	HeaderPromptBlocks    = "X-Switchyard-Prompt-Blocks"
	HeaderPredictedTTFTMS = "X-Switchyard-Predicted-TTFT-Ms"
)

func explain(h http.Header, b *backend.Backend, route *scheduler.Request) {
	h.Set(HeaderBackend, b.ID())
	h.Set(HeaderMatchedBlocks, strconv.Itoa(route.MatchedOn(b)))
	h.Set(HeaderPromptBlocks, strconv.Itoa(route.Blocks))
	h.Set(HeaderPredictedTTFTMS, strconv.FormatFloat(float64(route.PredictedTTFT)/float64(time.Millisecond), 'f', 1, 64))
}

// readBody reads the request body within the size limit, or writes an error
// response and returns false.
func (s *Server) readBody(w http.ResponseWriter, r *http.Request) ([]byte, bool) {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, s.maxRequestBytes))
	if err == nil {
		return body, true
	}
	if maxErr, ok := errors.AsType[*http.MaxBytesError](err); ok {
		openai.WriteError(w, http.StatusRequestEntityTooLarge, openai.ErrTypeInvalidRequest,
			"request body exceeds "+formatBytes(maxErr.Limit))
		return nil, false
	}
	openai.WriteError(w, http.StatusBadRequest, openai.ErrTypeInvalidRequest, "could not read request body")
	return nil, false
}

// attempt is the outcome of forwarding a request, after any retries.
type attempt struct {
	backend  *backend.Backend
	uncached int64
	res      proxy.Result
	err      error
	retries  int
}

// forward sends the request to the best available backend. If that fails
// transiently before anything reaches the client, it retries on a different
// backend, within the retry limit and budget. It returns false only if no
// backend was available for the first attempt; the caller answers 503.
func (s *Server) forward(w http.ResponseWriter, r *http.Request, body []byte, parsed *prefix.Request, route *scheduler.Request) (attempt, bool) {
	s.retries.deposit()
	var a attempt
	var tried []*backend.Backend
	for {
		b, ok := s.pick(route, tried)
		if !ok {
			return a, a.backend != nil // report the last failure, if any
		}
		if !b.Acquire() {
			// The breaker opened, or its one trial slot was taken, since the
			// candidates were listed.
			tried = append(tried, b)
			continue
		}
		// Record the belief before forwarding, so concurrent requests with
		// the same prefix see it immediately.
		s.index.Insert(b.Index(), b.Generation(), parsed.Hashes)
		uncached := s.estimator.UncachedTokens(route.PromptTokens, route.MatchedOn(b))
		if route.PredictedTTFT == 0 {
			// Cache-blind policies do not predict; predict anyway so every
			// policy reports the same prediction-error measure.
			route.PredictedTTFT = s.estimator.TTFT(b, uncached)
		}
		if s.explainHeaders {
			explain(w.Header(), b, route)
		}

		ticket := b.Admit(uncached)
		res, err := s.proxy.Forward(w, r, b, body, proxy.ForwardOptions{
			OnFirstToken:        ticket.FirstToken,
			HoldRetryableStatus: a.retries < s.maxRetries && s.retries.available(),
		})
		ticket.Done()
		b.Report(outcome(r, res, err))
		s.learn(b, ticket, parsed, &res)
		a = attempt{backend: b, uncached: uncached, res: res, err: err, retries: a.retries}

		if !retryable(r, &a) || a.retries >= s.maxRetries || !s.retries.withdraw() {
			return a, true
		}
		a.retries++
		tried = append(tried, b)
		route.PredictedTTFT = 0
	}
}

// retryable reports whether an attempt failed in a way that another backend
// might not, before anything reached the client.
func retryable(r *http.Request, a *attempt) bool {
	return r.Context().Err() == nil && !a.res.HeaderWritten &&
		(errors.Is(a.err, proxy.ErrUpstreamUnavailable) || errors.Is(a.err, proxy.ErrRetryableStatus))
}

// outcome classifies an attempt for the backend's circuit breaker.
func outcome(r *http.Request, res proxy.Result, err error) backend.Outcome {
	switch {
	case r.Context().Err() != nil:
		return backend.OutcomeAbandoned
	case err == nil && res.Status >= 500:
		return backend.OutcomeFailure
	case err == nil:
		return backend.OutcomeSuccess
	case errors.Is(err, proxy.ErrUpstreamUnavailable), errors.Is(err, proxy.ErrRetryableStatus), errors.Is(err, proxy.ErrStreamIdle):
		return backend.OutcomeFailure
	default:
		// Other errors, such as a failed write to the client, say nothing
		// about the backend.
		return backend.OutcomeAbandoned
	}
}

// defaultOutputEstimate is the output charged to a request that sets no
// output limit, until its actual usage corrects the charge.
const defaultOutputEstimate = 256

func outputEstimate(f openai.RoutingFields) int64 {
	if n := f.MaxOutputTokens(); n > 0 {
		return int64(n)
	}
	return defaultOutputEstimate
}

// HeaderTenantID names the tenant when TrustTenantHeader is set.
const HeaderTenantID = "X-Tenant-ID"

// admit identifies the request's tenant and waits for admission. It writes
// the rejection and returns false if the request may not run.
func (s *Server) admit(w http.ResponseWriter, r *http.Request, cost int64) (*admission.Grant, bool) {
	tenant := s.admission.TenantForKey(bearerToken(r.Header))
	if s.trustTenantHdr && r.Header.Get(HeaderTenantID) != "" {
		tenant = r.Header.Get(HeaderTenantID)
	}
	grant, err := s.admission.Admit(r.Context(), tenant, float64(cost))
	if err == nil {
		return grant, true
	}
	if reject, ok := errors.AsType[*admission.RejectError](err); ok {
		seconds := max(1, int(math.Ceil(reject.RetryAfter.Seconds())))
		w.Header().Set("Retry-After", strconv.Itoa(seconds))
		openai.WriteError(w, http.StatusTooManyRequests, openai.ErrTypeRateLimit, reject.Error())
	}
	// Otherwise the client went away while waiting; there is no one to
	// answer.
	return nil, false
}

func bearerToken(h http.Header) string {
	token, ok := strings.CutPrefix(h.Get("Authorization"), "Bearer ")
	if !ok {
		return ""
	}
	return strings.TrimSpace(token)
}

// newRoute describes a parsed request to the scheduler: its size and how
// much of it each backend is believed to cache.
func (s *Server) newRoute(parsed *prefix.Request, promptTokens int64) *scheduler.Request {
	backends := len(s.pool.Backends())
	route := &scheduler.Request{
		Model:        parsed.Fields.Model,
		Blocks:       len(parsed.Hashes),
		Matched:      make([]int, backends),
		PromptTokens: promptTokens,
	}
	s.index.Match(parsed.Hashes, s.pool.Generations(make([]uint64, 0, backends)), route.Matched)
	return route
}

// learn feeds a finished request's measurements back into the estimator.
func (s *Server) learn(b *backend.Backend, ticket *backend.Ticket, parsed *prefix.Request, res *proxy.Result) {
	if res.FirstToken > 0 {
		s.estimator.ObserveTTFT(b.Index(), ticket.QueueTokensAtAdmit+ticket.PrefillTokens(), res.FirstToken)
	}
	if res.Usage != nil {
		s.estimator.ObserveUsage(parsed.CanonicalBytes, res.Usage.PromptTokens)
	}
}

// requestLog gathers what the access log line reports about one request.
type requestLog struct {
	retries   int
	start     time.Time
	queueWait time.Duration
	tenant    string
	bodyBytes int
	parsed    *prefix.Request
	route     *scheduler.Request
	backend   *backend.Backend
	uncached  int64
	status    int
	res       *proxy.Result
	err       error
}

// logRequest writes one access log line. It never includes prompt or
// completion content.
func (s *Server) logRequest(r *http.Request, l requestLog) {
	attrs := []slog.Attr{
		slog.String("request_id", RequestID(r.Context())),
		slog.String("path", r.URL.Path),
		slog.String("tenant", l.tenant),
		slog.String("model", l.route.Model),
		slog.Bool("stream", l.parsed.Fields.Stream),
		slog.String("policy", s.policy.Name()),
		slog.String("backend", l.backend.ID()),
		slog.Int("status", l.status),
		slog.Int("prefix_blocks", l.route.Blocks),
		slog.Int("matched_blocks", l.route.MatchedOn(l.backend)),
		slog.Int64("prompt_tokens_estimate", l.route.PromptTokens),
		slog.Int64("uncached_tokens_estimate", l.uncached),
		slog.Duration("predicted_ttft", l.route.PredictedTTFT),
		slog.Int("request_bytes", l.bodyBytes),
		slog.Int64("response_bytes", l.res.BytesWritten),
		slog.Duration("duration", time.Since(l.start)),
		slog.Duration("queue_wait", l.queueWait),
		slog.Int("retries", l.retries),
		slog.Duration("first_byte", l.res.FirstByte),
	}
	if l.res.Streamed {
		attrs = append(attrs, slog.Duration("first_token", l.res.FirstToken), slog.Int("events", l.res.Events))
	}
	if l.res.Usage != nil {
		attrs = append(attrs,
			slog.Int("prompt_tokens", l.res.Usage.PromptTokens),
			slog.Int("completion_tokens", l.res.Usage.CompletionTokens))
	}
	level := slog.LevelInfo
	if l.err != nil && l.status != statusClientClosedRequest {
		level = slog.LevelWarn
		attrs = append(attrs, slog.String("error", l.err.Error()))
	}
	s.logger.LogAttrs(r.Context(), level, "request", attrs...)
}

// handleModels proxies the model list from one healthy backend. Every
// backend serves the same model, so any of them can answer.
func (s *Server) handleModels(w http.ResponseWriter, r *http.Request) {
	b, ok := s.pick(&scheduler.Request{}, nil)
	if !ok {
		openai.WriteError(w, http.StatusServiceUnavailable, openai.ErrTypeUnavailable, "no healthy backend available")
		return
	}
	ticket := b.Admit(0)
	defer ticket.Done()
	if res, err := s.proxy.Forward(w, r, b, nil, proxy.ForwardOptions{}); err != nil && !res.HeaderWritten && r.Context().Err() == nil {
		openai.WriteError(w, http.StatusBadGateway, openai.ErrTypeServer, "backend request failed")
	}
}

// pick chooses an available backend not in exclude, and reports false if
// there is none.
func (s *Server) pick(req *scheduler.Request, exclude []*backend.Backend) (*backend.Backend, bool) {
	candidates := s.pool.AppendAvailable(make([]*backend.Backend, 0, 8))
	candidates = slices.DeleteFunc(candidates, func(b *backend.Backend) bool { return slices.Contains(exclude, b) })
	b, err := s.policy.Pick(req, candidates)
	return b, err == nil
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
