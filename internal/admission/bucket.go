package admission

import (
	"math"
	"time"
)

// bucket is a token bucket: it holds up to burst tokens and refills at rate
// tokens per second. A request spends its estimated cost up front; the charge
// is corrected with the actual cost once known, so the bucket tracks real
// consumption over time.
//
// bucket is not safe for concurrent use; the Controller's mutex guards it.
type bucket struct {
	rate   float64 // tokens per second; +Inf means unlimited
	burst  float64
	tokens float64
	last   time.Time
}

func newBucket(rate, burst float64, now time.Time) *bucket {
	if rate <= 0 {
		rate = math.Inf(1)
	}
	return &bucket{rate: rate, burst: burst, tokens: burst, last: now}
}

func (b *bucket) refill(now time.Time) {
	if math.IsInf(b.rate, 1) {
		b.tokens = b.burst
		return
	}
	if elapsed := now.Sub(b.last).Seconds(); elapsed > 0 {
		b.tokens = math.Min(b.burst, b.tokens+elapsed*b.rate)
	}
	b.last = now
}

// take spends cost if the bucket holds enough and returns zero. Otherwise it
// spends nothing and returns how long until enough tokens accumulate. A cost
// above the burst can never be paid in full; it is admitted when the bucket
// is full, and the bucket goes into debt, which delays later requests.
func (b *bucket) take(cost float64, now time.Time) time.Duration {
	b.refill(now)
	need := math.Min(cost, b.burst)
	if b.tokens >= need {
		b.tokens -= cost
		return 0
	}
	return time.Duration((need - b.tokens) / b.rate * float64(time.Second))
}

// adjust corrects an earlier charge by delta tokens: positive charges more,
// negative refunds.
func (b *bucket) adjust(delta float64, now time.Time) {
	b.refill(now)
	b.tokens = math.Min(b.burst, b.tokens-delta)
}
