package scheduler

import (
	"math"
	"sync/atomic"
	"time"

	"github.com/shusingh/switchyard/internal/backend"
)

// EstimatorConfig configures TTFT estimation. See
// docs/engineering/design.md section 9.
type EstimatorConfig struct {
	// InitialPrefillTokensPerSecond is each backend's assumed prefill
	// throughput until observations refine it.
	InitialPrefillTokensPerSecond float64
	// Overhead is the fixed part of time to first token: network, request
	// handling, and at least one engine step.
	Overhead time.Duration
	// DecodePenalty is added per request the backend is decoding, because a
	// larger decode batch slows every engine step, including the steps that
	// prefill a new request.
	DecodePenalty time.Duration
	// InitialBytesPerToken converts canonical request bytes to tokens until
	// usage reports refine it.
	InitialBytesPerToken float64
	// BlockBytes is the prefix block size, for converting matched blocks to
	// tokens.
	BlockBytes int
}

const (
	// rateAlpha weights each new prefill-throughput sample. At 0.1 the
	// estimate follows a change in throughput within about 20 requests.
	rateAlpha = 0.1
	// bytesPerTokenAlpha weights each new bytes-per-token sample. The ratio
	// is a property of the traffic mix and changes slowly.
	bytesPerTokenAlpha = 0.02
	// minRateSampleTokens skips throughput samples with too little prefill
	// work: their TTFT is dominated by fixed overhead and says little about
	// throughput.
	minRateSampleTokens = 256
	// minUsageSampleTokens skips bytes-per-token samples from tiny prompts,
	// where template tokens distort the ratio.
	minUsageSampleTokens = 64
)

// Estimator predicts time to first token per backend and learns from
// outcomes. It is safe for concurrent use.
type Estimator struct {
	cfg           EstimatorConfig
	rates         []atomicFloat // tokens per second, indexed by backend.Index()
	bytesPerToken atomicFloat
}

// NewEstimator returns an estimator for a pool of the given size.
func NewEstimator(cfg EstimatorConfig, backends int) *Estimator {
	e := &Estimator{cfg: cfg, rates: make([]atomicFloat, backends)}
	for i := range e.rates {
		e.rates[i].Store(cfg.InitialPrefillTokensPerSecond)
	}
	e.bytesPerToken.Store(cfg.InitialBytesPerToken)
	return e
}

// PromptTokens estimates a request's prompt tokens from the size of its
// canonical form.
func (e *Estimator) PromptTokens(canonicalBytes int) int64 {
	return int64(math.Ceil(float64(canonicalBytes) / e.bytesPerToken.Load()))
}

// UncachedTokens estimates how many of a request's prompt tokens backend b
// would have to prefill, given the number of leading blocks believed cached
// there.
func (e *Estimator) UncachedTokens(promptTokens int64, matchedBlocks int) int64 {
	cached := int64(float64(matchedBlocks*e.cfg.BlockBytes) / e.bytesPerToken.Load())
	return max(promptTokens-cached, 0)
}

// TTFT predicts the time to first token of a request with uncachedTokens of
// prefill work if it were sent to b now: the fixed overhead, plus the
// backend's queued and new prefill work at its throughput, plus the decode
// penalty for its current batch.
func (e *Estimator) TTFT(b *backend.Backend, uncachedTokens int64) time.Duration {
	load := b.Load()
	work := float64(load.PendingPrefillTokens + uncachedTokens)
	prefill := time.Duration(work / e.rates[b.Index()].Load() * float64(time.Second))
	return e.cfg.Overhead + prefill + time.Duration(load.Decoding)*e.cfg.DecodePenalty
}

// PrefillRate returns backend index b's current throughput estimate in
// tokens per second.
func (e *Estimator) PrefillRate(b int) float64 { return e.rates[b].Load() }

// BytesPerToken returns the current bytes-per-token estimate.
func (e *Estimator) BytesPerToken() float64 { return e.bytesPerToken.Load() }

// ObserveTTFT refines backend index b's throughput from a completed
// request: workTokens of prefill (its own plus the queue ahead of it at
// admission) took ttft.
func (e *Estimator) ObserveTTFT(b int, workTokens int64, ttft time.Duration) {
	if workTokens < minRateSampleTokens {
		return
	}
	busy := max(ttft-e.cfg.Overhead, time.Millisecond)
	e.rates[b].ewma(float64(workTokens)/busy.Seconds(), rateAlpha)
}

// ObserveUsage refines bytes per token from a response's reported prompt
// size.
func (e *Estimator) ObserveUsage(canonicalBytes, promptTokens int) {
	if promptTokens < minUsageSampleTokens {
		return
	}
	e.bytesPerToken.ewma(float64(canonicalBytes)/float64(promptTokens), bytesPerTokenAlpha)
}

// atomicFloat is a float64 with atomic load, store, and EWMA update.
type atomicFloat struct{ bits atomic.Uint64 }

func (f *atomicFloat) Load() float64   { return math.Float64frombits(f.bits.Load()) }
func (f *atomicFloat) Store(v float64) { f.bits.Store(math.Float64bits(v)) }

// ewma moves the value toward sample by weight alpha, retrying if another
// goroutine updated it concurrently.
func (f *atomicFloat) ewma(sample, alpha float64) {
	for {
		old := f.bits.Load()
		v := math.Float64frombits(old)
		if f.bits.CompareAndSwap(old, math.Float64bits(v+alpha*(sample-v))) {
			return
		}
	}
}
