// Package sim implements a simulated OpenAI-compatible model server. It
// speaks the same wire format as vLLM, including the empty role chunk that
// opens a chat stream, and models the behavior that matters for routing: a
// fixed-size KV cache with automatic prefix caching, chunked prefill, and
// continuous batching. Latency is not scripted; it emerges from queueing and
// batching under the cost model.
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

// maxRequestBytes bounds request bodies the engine accepts.
const maxRequestBytes = 64 << 20

// Options configures an Engine.
type Options struct {
	// Model is the model name the engine serves and accepts.
	Model string
	// Cost sets capacity and speed. Zero fields take DefaultCostModel values.
	Cost CostModel
	// DefaultOutputTokens is generated when a request sets no output limit.
	DefaultOutputTokens int
}

// Stats counts requests and cache use.
type Stats struct {
	Running   int64
	Waiting   int64
	Completed int64
	Cancelled int64
	// PromptTokens and CachedPromptTokens mirror vLLM's
	// prefix_cache_queries and prefix_cache_hits counters.
	PromptTokens       int64
	CachedPromptTokens int64
}

// Engine is a simulated model server. Create it with NewEngine, serve its
// Handler, and run its scheduler with Run.
type Engine struct {
	opts      Options
	sched     *scheduler
	tokenizer *tokenizer
	completed atomic.Int64
	cancelled atomic.Int64
}

// NewEngine returns an engine with the given options.
func NewEngine(opts Options) *Engine {
	opts.Cost = withDefaults(opts.Cost)
	if opts.DefaultOutputTokens <= 0 {
		opts.DefaultOutputTokens = 16
	}
	return &Engine{
		opts:      opts,
		sched:     newScheduler(opts.Cost),
		tokenizer: newTokenizer(opts.Cost.BlockTokens),
	}
}

func withDefaults(m CostModel) CostModel {
	d := DefaultCostModel()
	setIfZero(&m.BlockTokens, d.BlockTokens)
	setIfZero(&m.CapacityBlocks, d.CapacityBlocks)
	setIfZero(&m.PrefillTokensPerSecond, d.PrefillTokensPerSecond)
	setIfZero(&m.StepOverhead, d.StepOverhead)
	setIfZero(&m.DecodeCostPerSequence, d.DecodeCostPerSequence)
	setIfZero(&m.MaxBatchTokens, d.MaxBatchTokens)
	setIfZero(&m.MaxRunning, d.MaxRunning)
	setIfZero(&m.MaxModelLen, d.MaxModelLen)
	return m
}

func setIfZero[T comparable](field *T, value T) {
	var zero T
	if *field == zero {
		*field = value
	}
}

// Run executes the engine's scheduler until ctx is cancelled. Requests still
// in flight at that point are aborted.
func (e *Engine) Run(ctx context.Context) { e.sched.run(ctx) }

// Stats returns a snapshot of the engine's counters.
func (e *Engine) Stats() Stats {
	return Stats{
		Running:            e.sched.numRunning.Load(),
		Waiting:            e.sched.numWaiting.Load(),
		Completed:          e.completed.Load(),
		Cancelled:          e.cancelled.Load(),
		PromptTokens:       e.sched.queryTokens.Load(),
		CachedPromptTokens: e.sched.hitTokens.Load(),
	}
}

// Handler returns the engine's HTTP API.
func (e *Engine) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST "+openai.PathChatCompletions, func(w http.ResponseWriter, r *http.Request) { e.serve(w, r, true) })
	mux.HandleFunc("POST "+openai.PathCompletions, func(w http.ResponseWriter, r *http.Request) { e.serve(w, r, false) })
	mux.HandleFunc("GET "+openai.PathModels, e.handleModels)
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	mux.HandleFunc("GET /metrics", e.handleMetrics)
	mux.HandleFunc("POST /reset_prefix_cache", func(w http.ResponseWriter, _ *http.Request) {
		e.sched.requestReset()
		w.WriteHeader(http.StatusOK)
	})
	return mux
}

func (e *Engine) serve(w http.ResponseWriter, r *http.Request, chat bool) {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxRequestBytes))
	if err != nil {
		openai.WriteError(w, http.StatusBadRequest, openai.ErrTypeInvalidRequest, "could not read request body")
		return
	}
	var req chatRequest
	if err := json.Unmarshal(body, &req); err != nil {
		openai.WriteError(w, http.StatusBadRequest, openai.ErrTypeInvalidRequest, "malformed request body")
		return
	}
	fields, err := openai.DecodeRoutingFields(body)
	if err != nil {
		openai.WriteError(w, http.StatusBadRequest, openai.ErrTypeInvalidRequest, err.Error())
		return
	}
	if fields.Model != e.opts.Model {
		openai.WriteError(w, http.StatusNotFound, openai.ErrTypeInvalidRequest,
			fmt.Sprintf("model %q does not exist", fields.Model))
		return
	}
	outputTokens := fields.MaxOutputTokens()
	if outputTokens <= 0 {
		outputTokens = e.opts.DefaultOutputTokens
	}
	p := e.tokenizer.tokenize(render(&req, chat))
	p.tokens = max(p.tokens, 1)
	if total := p.tokens + outputTokens; total > e.opts.Cost.MaxModelLen {
		openai.WriteError(w, http.StatusBadRequest, openai.ErrTypeInvalidRequest,
			fmt.Sprintf("prompt (%d tokens) plus output (%d tokens) exceeds the maximum context length of %d tokens",
				p.tokens, outputTokens, e.opts.Cost.MaxModelLen))
		return
	}

	seq := newSequence(p, outputTokens)
	if need := len(p.blocks) + seq.privateBlocks(e.opts.Cost.BlockTokens); need > e.opts.Cost.CapacityBlocks {
		openai.WriteError(w, http.StatusBadRequest, openai.ErrTypeInvalidRequest,
			fmt.Sprintf("request needs %d KV cache blocks; capacity is %d", need, e.opts.Cost.CapacityBlocks))
		return
	}
	e.sched.submit(seq)

	gen := generation{
		id:           "sim-" + strconv.FormatInt(time.Now().UnixNano(), 36),
		model:        e.opts.Model,
		chat:         chat,
		tokens:       outputTokens,
		promptTokens: p.tokens,
	}
	var outcome seqEvent
	if fields.Stream {
		outcome = e.stream(r.Context(), w, seq, gen, fields.WantsUsage())
	} else {
		outcome = e.respond(r.Context(), w, seq, gen)
	}
	switch outcome {
	case eventDone:
		e.completed.Add(1)
	case eventAborted:
		// Engine shutdown; nothing to count.
	default:
		seq.cancelled.Store(true)
		e.sched.notify()
		e.cancelled.Add(1)
	}
}

// await returns the sequence's next event, or eventCancelled if the client
// goes away first.
func await(ctx context.Context, seq *sequence) seqEvent {
	select {
	case ev := <-seq.events:
		return ev
	case <-ctx.Done():
		return eventCancelled
	}
}

// respond waits for the whole generation, then writes one JSON response.
func (e *Engine) respond(ctx context.Context, w http.ResponseWriter, seq *sequence, gen generation) seqEvent {
	for {
		switch ev := await(ctx, seq); ev {
		case eventToken:
			continue
		case eventDone:
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(gen.response())
			return eventDone
		case eventAborted:
			openai.WriteError(w, http.StatusServiceUnavailable, openai.ErrTypeUnavailable, "engine shutting down")
			return ev
		default:
			return ev
		}
	}
}

// stream writes the generation as server-sent events, one token per event.
func (e *Engine) stream(ctx context.Context, w http.ResponseWriter, seq *sequence, gen generation, withUsage bool) seqEvent {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	rc := http.NewResponseController(w)
	send := func(v any) bool {
		data, _ := json.Marshal(v)
		return openai.WriteEvent(w, data) == nil && rc.Flush() == nil
	}

	if gen.chat && !send(gen.roleChunk()) {
		return eventCancelled
	}
	for i := 0; ; i++ {
		switch ev := await(ctx, seq); ev {
		case eventToken:
			if !send(gen.tokenChunk(i)) {
				return eventCancelled
			}
		case eventDone:
			if !send(gen.finishChunk()) || (withUsage && !send(gen.usageChunk())) {
				return eventCancelled
			}
			if openai.WriteEvent(w, []byte("[DONE]")) != nil || rc.Flush() != nil {
				return eventCancelled
			}
			return eventDone
		default:
			return ev
		}
	}
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
