package scheduler

import (
	"math"
	"math/rand/v2"
	"time"

	"github.com/shusingh/switchyard/internal/backend"
)

// estimatedTTFT sends each request to the backend with the lowest predicted
// time to first token. The prediction charges each backend for its queued
// prefill work plus the part of this request's prompt it does not already
// cache, so cache reuse and load are traded in one unit: seconds. See
// docs/engineering/design.md section 9 and docs/adr/0004-route-on-estimated-ttft.md.
//
// When load is badly skewed, an imbalance guard first drops the busiest
// backends from consideration. This bounds the damage when estimates are
// wrong, for example right after a burst the estimator has not yet seen.
type estimatedTTFT struct {
	est *Estimator
	// balanceAbs and balanceRel define skew: the busiest backend has more
	// than balanceAbs more in-flight requests than the idlest, and more than
	// balanceRel times as many.
	balanceAbs int64
	balanceRel float64
	// tieEpsilon treats predictions within this fraction of the best as
	// ties, picked at random, so near-equal backends share traffic instead
	// of one absorbing a burst.
	tieEpsilon float64
}

func (*estimatedTTFT) Name() string { return PolicyEstimatedTTFT }

func (p *estimatedTTFT) Pick(req *Request, candidates []*backend.Backend) (*backend.Backend, error) {
	if len(candidates) == 0 {
		return nil, ErrNoCandidates
	}
	ceiling := p.loadCeiling(candidates)

	// Predictions for the few candidates of a pool fit on the stack.
	var buf [16]time.Duration
	predicted := buf[:0]
	best := time.Duration(math.MaxInt64)
	for _, b := range candidates {
		d := time.Duration(math.MaxInt64)
		if b.InFlight() <= ceiling {
			d = p.est.TTFT(b, p.est.UncachedTokens(req.PromptTokens, req.MatchedOn(b)))
		}
		predicted = append(predicted, d)
		best = min(best, d)
	}

	limit := best + time.Duration(float64(best)*p.tieEpsilon)
	var chosen *backend.Backend
	ties := 0
	for i, b := range candidates {
		if predicted[i] > limit {
			continue
		}
		ties++
		if rand.IntN(ties) == 0 { //nolint:gosec // G404: load balancing, not security
			chosen = b
			req.PredictedTTFT = predicted[i]
		}
	}
	return chosen, nil
}

// loadCeiling returns the highest in-flight count a candidate may have to be
// considered. It is unlimited unless load is skewed.
func (p *estimatedTTFT) loadCeiling(candidates []*backend.Backend) int64 {
	lo, hi := int64(math.MaxInt64), int64(0)
	for _, b := range candidates {
		n := b.InFlight()
		lo, hi = min(lo, n), max(hi, n)
	}
	if hi-lo > p.balanceAbs && float64(hi) > p.balanceRel*float64(lo+1) {
		return lo + p.balanceAbs
	}
	return math.MaxInt64
}
