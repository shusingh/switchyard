package scheduler

import (
	"math"
	"testing"
	"time"
)

func testEstimator() *Estimator {
	return NewEstimator(EstimatorConfig{
		InitialPrefillTokensPerSecond: 10000,
		Overhead:                      10 * time.Millisecond,
		DecodePenalty:                 time.Millisecond,
		InitialBytesPerToken:          4,
		BlockBytes:                    64, // 16 tokens per block at 4 bytes per token
	}, 4)
}

func TestEstimatorTTFT(t *testing.T) {
	t.Parallel()
	e := testEstimator()
	b := newCandidates(t, 1)[0]
	queued := b.Admit(5000)
	decoding := b.Admit(0)
	decoding.FirstToken()

	// 10ms overhead + (5,000 queued + 1,000 new) / 10,000 tok/s + 1 decoding * 1ms.
	if got, want := e.TTFT(b, 1000), 10*time.Millisecond+600*time.Millisecond+time.Millisecond; got != want {
		t.Errorf("TTFT = %v, want %v", got, want)
	}
	queued.Done()
	decoding.Done()
}

func TestEstimatorTokenMath(t *testing.T) {
	t.Parallel()
	e := testEstimator()
	if got := e.PromptTokens(4001); got != 1001 {
		t.Errorf("PromptTokens(4001 bytes) = %d, want 1001", got)
	}
	// 10 matched blocks of 64 bytes are 160 tokens.
	if got := e.UncachedTokens(1000, 10); got != 840 {
		t.Errorf("UncachedTokens(1000, 10 blocks) = %d, want 840", got)
	}
	if got := e.UncachedTokens(100, 10); got != 0 {
		t.Errorf("UncachedTokens never goes negative; got %d", got)
	}
}

func TestEstimatorLearnsThroughput(t *testing.T) {
	t.Parallel()
	e := testEstimator()
	// The backend really prefills 20,000 tokens per second.
	for range 100 {
		e.ObserveTTFT(0, 4000, 10*time.Millisecond+200*time.Millisecond)
	}
	if got := e.PrefillRate(0); math.Abs(got-20000) > 200 {
		t.Errorf("learned rate = %.0f tok/s, want about 20,000", got)
	}
	if got := e.PrefillRate(1); got != 10000 {
		t.Errorf("an unobserved backend's rate changed to %.0f", got)
	}
	// Samples with little work are dominated by overhead and ignored.
	before := e.PrefillRate(2)
	e.ObserveTTFT(2, 10, time.Second)
	if e.PrefillRate(2) != before {
		t.Error("a tiny sample changed the throughput estimate")
	}
}

func TestEstimatorLearnsBytesPerToken(t *testing.T) {
	t.Parallel()
	e := testEstimator()
	for range 500 {
		e.ObserveUsage(5500, 1000)
	}
	if got := e.BytesPerToken(); math.Abs(got-5.5) > 0.05 {
		t.Errorf("learned bytes per token = %.3f, want about 5.5", got)
	}
}

func TestEstimatedTTFTPrefersCachedBackendWhenLoadIsEven(t *testing.T) {
	t.Parallel()
	candidates := newCandidates(t, 3)
	p, _ := New(PolicyEstimatedTTFT, Options{Estimator: testEstimator(), BalanceAbs: 16, BalanceRel: 1.5})
	req := &Request{PromptTokens: 4000, Matched: []int{0, 200, 50}}
	b, err := p.Pick(req, candidates)
	if err != nil {
		t.Fatal(err)
	}
	if b != candidates[1] {
		t.Errorf("picked %s, want b1, which caches 3,200 of 4,000 tokens", b.ID())
	}
	if req.PredictedTTFT <= 0 {
		t.Error("PredictedTTFT was not set")
	}
}

func TestEstimatedTTFTLeavesAHotBackend(t *testing.T) {
	t.Parallel()
	candidates := newCandidates(t, 2)
	// b0 holds the prefix but has 30,000 tokens of prefill queued: 3 seconds.
	// Recomputing the whole 4,000-token prompt on idle b1 takes 0.4 seconds.
	ticket := candidates[0].Admit(30000)
	defer ticket.Done()
	p, _ := New(PolicyEstimatedTTFT, Options{Estimator: testEstimator(), BalanceAbs: 16, BalanceRel: 1.5})
	b, _ := p.Pick(&Request{PromptTokens: 4000, Matched: []int{250, 0}}, candidates)
	if b != candidates[1] {
		t.Errorf("picked %s, want b1: the cache is not worth a 3-second queue", b.ID())
	}
}

func TestEstimatedTTFTImbalanceGuard(t *testing.T) {
	t.Parallel()
	candidates := newCandidates(t, 2)
	// b0 caches everything and its queue looks empty, but it has 40 requests
	// in flight to b1's none: skewed beyond BalanceAbs and BalanceRel.
	for range 40 {
		ticket := candidates[0].Admit(0)
		defer ticket.Done()
	}
	p, _ := New(PolicyEstimatedTTFT, Options{Estimator: testEstimator(), BalanceAbs: 16, BalanceRel: 1.5})
	b, _ := p.Pick(&Request{PromptTokens: 4000, Matched: []int{250, 0}}, candidates)
	if b != candidates[1] {
		t.Errorf("picked %s, want b1: the guard must exclude the overloaded backend", b.ID())
	}
}

func TestEstimatedTTFTSpreadsNearTies(t *testing.T) {
	t.Parallel()
	candidates := newCandidates(t, 3)
	p, _ := New(PolicyEstimatedTTFT, Options{Estimator: testEstimator(), BalanceAbs: 16, BalanceRel: 1.5, TieEpsilon: 0.05})
	seen := map[string]bool{}
	for range 300 {
		b, _ := p.Pick(&Request{PromptTokens: 1000, Matched: []int{0, 0, 0}}, candidates)
		seen[b.ID()] = true
	}
	if len(seen) != 3 {
		t.Errorf("identical backends received traffic on %d of 3; ties should be spread", len(seen))
	}
}

func TestNewEstimatedTTFTRequiresEstimator(t *testing.T) {
	t.Parallel()
	if _, err := New(PolicyEstimatedTTFT, Options{}); err == nil {
		t.Error("New(estimated_ttft) without an estimator error = nil, want an error")
	}
}
