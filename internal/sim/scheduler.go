package sim

import (
	"context"
	"sync"
	"sync/atomic"
	"time"
)

// CostModel sets the simulated engine's capacity and speed.
//
// The defaults approximate one of four Qwen2.5-1.5B replicas sharing an
// RTX 4090 (docs/adr/0009-benchmark-replica-configuration.md) and are
// recalibrated against the real replicas in Phase 7.
type CostModel struct {
	// BlockTokens is the number of tokens per KV cache block.
	BlockTokens int
	// CapacityBlocks is the KV cache size in blocks.
	CapacityBlocks int
	// PrefillTokensPerSecond is prompt-processing throughput.
	PrefillTokensPerSecond float64
	// StepOverhead is the fixed duration of one engine step; with a single
	// running sequence it is the time between output tokens.
	StepOverhead time.Duration
	// DecodeCostPerSequence is added to a step for every decoding sequence,
	// so tokens slow down as the batch grows.
	DecodeCostPerSequence time.Duration
	// MaxBatchTokens is the prefill token budget of one step. Long prompts
	// are prefilled in chunks across steps, sharing them with decoding.
	MaxBatchTokens int
	// MaxRunning is the maximum number of concurrently running sequences.
	MaxRunning int
	// MaxModelLen bounds prompt plus output tokens for one request.
	MaxModelLen int
}

// DefaultCostModel returns the cost model described on CostModel.
func DefaultCostModel() CostModel {
	return CostModel{
		BlockTokens:            16,
		CapacityBlocks:         2340,
		PrefillTokensPerSecond: 14000,
		StepOverhead:           7 * time.Millisecond,
		DecodeCostPerSequence:  100 * time.Microsecond,
		MaxBatchTokens:         2048,
		MaxRunning:             128,
		MaxModelLen:            16384,
	}
}

type seqEvent int8

const (
	eventToken seqEvent = iota
	eventDone
	eventAborted // the engine shut down
	// eventCancelled is never sent by the scheduler; the HTTP handler uses
	// it to report that the client went away.
	eventCancelled
)

// sequence is one request inside the engine.
type sequence struct {
	prompt       prompt
	outputTokens int

	// Owned by the scheduler loop.
	alloc        allocation
	cachedTokens int
	prefilled    int
	generated    int

	// events receives one eventToken per output token, then eventDone. It is
	// buffered for all of them, so the scheduler never blocks on a slow
	// client.
	events    chan seqEvent
	cancelled atomic.Bool
}

func newSequence(p prompt, outputTokens int) *sequence {
	return &sequence{prompt: p, outputTokens: outputTokens, events: make(chan seqEvent, outputTokens+1)}
}

func (s *sequence) prefilling() bool { return s.prefilled < s.prompt.tokens }

// privateBlocks returns how many unindexed blocks the sequence needs while
// running: its partial final prompt block plus room for its output.
func (s *sequence) privateBlocks(blockTokens int) int {
	tail := s.prompt.tokens - len(s.prompt.blocks)*blockTokens
	return ceilDiv(tail+s.outputTokens, blockTokens)
}

// scheduler runs the engine loop: admit waiting sequences into free cache
// space, execute one batched step, emit tokens, repeat.
type scheduler struct {
	model CostModel
	cache *blockCache

	mu       sync.Mutex
	incoming []*sequence // guarded by mu
	stopped  bool        // guarded by mu
	wake     chan struct{}

	resetRequested atomic.Bool

	// Loop-owned queues.
	waiting []*sequence
	running []*sequence

	// Published by the loop for metrics.
	numRunning   atomic.Int64
	numWaiting   atomic.Int64
	pinnedBlocks atomic.Int64
	queryTokens  atomic.Int64
	hitTokens    atomic.Int64
}

func newScheduler(model CostModel) *scheduler {
	return &scheduler{
		model: model,
		cache: newBlockCache(model.CapacityBlocks),
		wake:  make(chan struct{}, 1),
	}
}

// submit queues a sequence. If the engine has stopped, the sequence is
// aborted immediately.
func (s *scheduler) submit(seq *sequence) {
	s.mu.Lock()
	if s.stopped {
		s.mu.Unlock()
		seq.events <- eventAborted
		return
	}
	s.incoming = append(s.incoming, seq)
	s.mu.Unlock()
	s.notify()
}

// notify wakes an idle loop. It never blocks.
func (s *scheduler) notify() {
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

// requestReset asks the loop to drop every unreferenced cached block before
// its next step.
func (s *scheduler) requestReset() {
	s.resetRequested.Store(true)
	s.notify()
}

// run executes the engine loop until ctx is cancelled, then aborts every
// sequence still queued or running.
func (s *scheduler) run(ctx context.Context) {
	defer s.stop()
	for {
		s.takeIncoming()
		if s.resetRequested.Swap(false) {
			s.cache.reset()
		}
		s.dropCancelled()
		s.admit()
		s.publish()

		if len(s.running) == 0 {
			select {
			case <-ctx.Done():
				return
			case <-s.wake:
				continue
			}
		}
		chunks, duration := s.plan()
		if !sleep(ctx, duration) {
			return
		}
		s.execute(chunks)
	}
}

func (s *scheduler) takeIncoming() {
	s.mu.Lock()
	s.waiting = append(s.waiting, s.incoming...)
	s.incoming = s.incoming[:0]
	s.mu.Unlock()
}

func (s *scheduler) dropCancelled() {
	s.waiting = removeIf(s.waiting, func(seq *sequence) bool { return seq.cancelled.Load() })
	s.running = removeIf(s.running, func(seq *sequence) bool {
		if seq.cancelled.Load() {
			s.cache.release(seq.alloc)
			return true
		}
		return false
	})
}

// admit moves waiting sequences into the running set in arrival order while
// there is room, as vLLM's first-come-first-served scheduler does. A sequence
// that does not fit blocks the ones behind it.
func (s *scheduler) admit() {
	admitted := 0
	for _, seq := range s.waiting {
		if len(s.running) >= s.model.MaxRunning {
			break
		}
		alloc, hitBlocks, ok := s.cache.allocate(seq.prompt.blocks, seq.privateBlocks(s.model.BlockTokens))
		if !ok {
			break
		}
		seq.alloc = alloc
		seq.cachedTokens = hitBlocks * s.model.BlockTokens
		// The engine always computes at least the last prompt token, because
		// it needs that token's output to start generating.
		seq.prefilled = min(seq.cachedTokens, seq.prompt.tokens-1)
		s.queryTokens.Add(int64(seq.prompt.tokens))
		s.hitTokens.Add(int64(seq.cachedTokens))
		s.running = append(s.running, seq)
		admitted++
	}
	s.waiting = s.waiting[admitted:]
}

// plan decides how many prompt tokens each prefilling sequence processes in
// the next step and how long the step takes. Decoding sequences each produce
// one token.
func (s *scheduler) plan() (chunks []int, duration time.Duration) {
	chunks = make([]int, len(s.running))
	budget := s.model.MaxBatchTokens
	prefillTokens, decoding := 0, 0
	for i, seq := range s.running {
		if !seq.prefilling() {
			decoding++
			continue
		}
		chunk := min(seq.prompt.tokens-seq.prefilled, budget)
		chunks[i] = chunk
		budget -= chunk
		prefillTokens += chunk
	}
	duration = s.model.StepOverhead +
		time.Duration(float64(prefillTokens)/s.model.PrefillTokensPerSecond*float64(time.Second)) +
		time.Duration(decoding)*s.model.DecodeCostPerSequence
	return chunks, duration
}

// execute applies a planned step: advance prefills, emit tokens, and retire
// finished sequences.
func (s *scheduler) execute(chunks []int) {
	s.running = removeIfIndexed(s.running, func(i int, seq *sequence) bool {
		if seq.prefilling() {
			seq.prefilled += chunks[i]
			if seq.prefilling() {
				return false // more prompt chunks to go
			}
		}
		// The step that finishes a prompt also produces its first token.
		seq.generated++
		seq.events <- eventToken
		if seq.generated < seq.outputTokens {
			return false
		}
		seq.events <- eventDone
		s.cache.release(seq.alloc)
		return true
	})
}

// publish exposes queue sizes and cache use for metrics.
func (s *scheduler) publish() {
	s.numRunning.Store(int64(len(s.running)))
	s.numWaiting.Store(int64(len(s.waiting)))
	s.pinnedBlocks.Store(int64(s.cache.pinned))
}

// stop marks the scheduler stopped and aborts every sequence it holds.
func (s *scheduler) stop() {
	s.mu.Lock()
	s.stopped = true
	pending := s.incoming
	s.incoming = nil
	s.mu.Unlock()
	for _, group := range [][]*sequence{pending, s.waiting, s.running} {
		for _, seq := range group {
			select {
			case seq.events <- eventAborted:
			default: // the buffer is full only once eventDone was sent
			}
		}
	}
	s.waiting, s.running = nil, nil
	s.publish()
}

func removeIf(seqs []*sequence, drop func(*sequence) bool) []*sequence {
	return removeIfIndexed(seqs, func(_ int, seq *sequence) bool { return drop(seq) })
}

// removeIfIndexed filters seqs in place, preserving order. drop sees each
// element's original index.
func removeIfIndexed(seqs []*sequence, drop func(int, *sequence) bool) []*sequence {
	kept := seqs[:0]
	for i, seq := range seqs {
		if !drop(i, seq) {
			kept = append(kept, seq)
		}
	}
	clear(seqs[len(kept):])
	return kept
}

func ceilDiv(a, b int) int { return (a + b - 1) / b }
