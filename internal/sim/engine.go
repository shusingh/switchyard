// Package sim implements a simulated OpenAI-compatible model server. It speaks
// the same wire format as vLLM, including the empty role chunk that opens a
// chat stream, so the router can be tested end to end without a GPU.
//
// Simulated results are always labeled as simulated; see
// docs/adr/0005-build-a-simulated-engine.md.
package sim

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/shusingh/switchyard/internal/openai"
)

const (
	// maxRequestBytes bounds request bodies the engine accepts.
	maxRequestBytes = 64 << 20
	// bytesPerToken approximates tokenization when reporting prompt usage.
	// English text averages about four bytes per token.
	bytesPerToken = 4
)

// Options configures the engine's behavior.
type Options struct {
	// Model is the model name the engine reports and accepts.
	Model string
	// FirstTokenDelay is the time from request to first generated token.
	FirstTokenDelay time.Duration
	// TokenInterval is the time between generated tokens.
	TokenInterval time.Duration
	// DefaultOutputTokens is used when a request sets no output limit.
	DefaultOutputTokens int
}

// Stats counts requests by outcome.
type Stats struct {
	Active    int64
	Completed int64
	Cancelled int64
}

// Engine is a simulated model server. It is safe for concurrent use.
type Engine struct {
	opts      Options
	active    atomic.Int64
	completed atomic.Int64
	cancelled atomic.Int64
}

// NewEngine returns an engine with the given options.
func NewEngine(opts Options) *Engine {
	if opts.DefaultOutputTokens <= 0 {
		opts.DefaultOutputTokens = 16
	}
	return &Engine{opts: opts}
}

// Stats returns a snapshot of the engine's request counters.
func (e *Engine) Stats() Stats {
	return Stats{Active: e.active.Load(), Completed: e.completed.Load(), Cancelled: e.cancelled.Load()}
}

// Handler returns the engine's HTTP API.
func (e *Engine) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST "+openai.PathChatCompletions, func(w http.ResponseWriter, r *http.Request) { e.serve(w, r, true) })
	mux.HandleFunc("POST "+openai.PathCompletions, func(w http.ResponseWriter, r *http.Request) { e.serve(w, r, false) })
	mux.HandleFunc("GET "+openai.PathModels, e.handleModels)
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	return mux
}

func (e *Engine) serve(w http.ResponseWriter, r *http.Request, chat bool) {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxRequestBytes))
	if err != nil {
		openai.WriteError(w, http.StatusBadRequest, openai.ErrTypeInvalidRequest, "could not read request body")
		return
	}
	var fields openai.RoutingFields
	if err := json.Unmarshal(body, &fields); err != nil {
		openai.WriteError(w, http.StatusBadRequest, openai.ErrTypeInvalidRequest, "malformed request body")
		return
	}
	if fields.Model != e.opts.Model {
		openai.WriteError(w, http.StatusNotFound, openai.ErrTypeInvalidRequest,
			fmt.Sprintf("model %q does not exist", fields.Model))
		return
	}
	tokens := fields.MaxOutputTokens()
	if tokens <= 0 {
		tokens = e.opts.DefaultOutputTokens
	}

	e.active.Add(1)
	defer e.active.Add(-1)

	gen := generation{
		id:           "sim-" + strconv.FormatInt(time.Now().UnixNano(), 36),
		model:        e.opts.Model,
		chat:         chat,
		tokens:       tokens,
		promptTokens: len(body) / bytesPerToken,
	}
	var ok bool
	if fields.Stream {
		ok = e.stream(r.Context(), w, gen, fields.WantsUsage())
	} else {
		ok = e.respond(r.Context(), w, gen)
	}
	if ok {
		e.completed.Add(1)
	} else {
		e.cancelled.Add(1)
	}
}

// respond waits for the whole generation, then writes one JSON response. It
// returns false if the client went away first.
func (e *Engine) respond(ctx context.Context, w http.ResponseWriter, gen generation) bool {
	if !sleep(ctx, e.opts.FirstTokenDelay+time.Duration(gen.tokens-1)*e.opts.TokenInterval) {
		return false
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(gen.response())
	return true
}

// stream writes the generation as server-sent events, one token per event.
// It returns false if the client went away first.
func (e *Engine) stream(ctx context.Context, w http.ResponseWriter, gen generation, withUsage bool) bool {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	rc := http.NewResponseController(w)
	send := func(v any) bool {
		data, _ := json.Marshal(v)
		return openai.WriteEvent(w, data) == nil && rc.Flush() == nil
	}

	if gen.chat && !send(gen.roleChunk()) {
		return false
	}
	delay := e.opts.FirstTokenDelay
	for i := range gen.tokens {
		if !sleep(ctx, delay) || !send(gen.tokenChunk(i)) {
			return false
		}
		delay = e.opts.TokenInterval
	}
	if !send(gen.finishChunk()) {
		return false
	}
	if withUsage && !send(gen.usageChunk()) {
		return false
	}
	return openai.WriteEvent(w, []byte("[DONE]")) == nil && rc.Flush() == nil
}

func (e *Engine) handleModels(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"object": "list",
		"data":   []map[string]any{{"id": e.opts.Model, "object": "model", "owned_by": "switchyard-sim"}},
	})
}

// sleep waits for d or until ctx is done, and reports whether it waited the
// full duration.
func sleep(ctx context.Context, d time.Duration) bool {
	if d <= 0 {
		return ctx.Err() == nil
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return true
	case <-ctx.Done():
		return false
	}
}
