package scheduler

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/shusingh/switchyard/internal/backend"
	"github.com/shusingh/switchyard/internal/config"
)

func newCandidates(t *testing.T, n int) []*backend.Backend {
	t.Helper()
	cfgs := make([]config.Backend, n)
	for i := range n {
		cfgs[i] = config.Backend{ID: fmt.Sprintf("b%d", i), URL: fmt.Sprintf("http://localhost:%d", 8001+i)}
	}
	p, err := backend.NewPool(cfgs, backend.BreakerConfig{})
	if err != nil {
		t.Fatal(err)
	}
	return p.Backends()
}

func TestNew(t *testing.T) {
	t.Parallel()
	for _, name := range Names() {
		p, err := New(name, testOptions())
		if err != nil {
			t.Errorf("New(%q) error = %v", name, err)
			continue
		}
		if p.Name() != name {
			t.Errorf("New(%q).Name() = %q", name, p.Name())
		}
	}
	if _, err := New("nonexistent", testOptions()); err == nil {
		t.Error(`New("nonexistent") error = nil, want an error`)
	}
}

func TestPoliciesRejectEmptyCandidates(t *testing.T) {
	t.Parallel()
	for _, name := range Names() {
		p, _ := New(name, testOptions())
		if _, err := p.Pick(&Request{}, nil); !errors.Is(err, ErrNoCandidates) {
			t.Errorf("%s.Pick(nil) error = %v, want %v", name, err, ErrNoCandidates)
		}
	}
}

func TestRoundRobinIsEven(t *testing.T) {
	t.Parallel()
	candidates := newCandidates(t, 4)
	p, _ := New(PolicyRoundRobin, testOptions())
	counts := map[string]int{}
	for range 400 {
		b, err := p.Pick(&Request{}, candidates)
		if err != nil {
			t.Fatal(err)
		}
		counts[b.ID()]++
	}
	for _, b := range candidates {
		if counts[b.ID()] != 100 {
			t.Errorf("round robin sent %d of 400 requests to %s, want 100", counts[b.ID()], b.ID())
		}
	}
}

func TestLeastLoadedPicksIdlestAndSpreadsTies(t *testing.T) {
	t.Parallel()
	candidates := newCandidates(t, 3)
	candidates[0].Admit(0)
	candidates[0].Admit(0)
	candidates[1].Admit(0)
	p, _ := New(PolicyLeastLoaded, testOptions())
	b, _ := p.Pick(&Request{}, candidates)
	if b != candidates[2] {
		t.Fatalf("least_loaded picked %s, want the idle backend b2", b.ID())
	}

	candidates[2].Admit(0) // now b1 and b2 tie at one request each
	seen := map[string]bool{}
	for range 200 {
		b, _ := p.Pick(&Request{}, candidates)
		if b == candidates[0] {
			t.Fatal("least_loaded picked the busiest backend")
		}
		seen[b.ID()] = true
	}
	if len(seen) != 2 {
		t.Errorf("least_loaded used %d of 2 tied backends; ties should be spread", len(seen))
	}
}

func TestP2CNeverPicksTheBusierOfTwo(t *testing.T) {
	t.Parallel()
	candidates := newCandidates(t, 2)
	for range 5 {
		candidates[0].Admit(0)
	}
	p, _ := New(PolicyP2C, testOptions())
	for range 100 {
		// With two candidates both are always sampled, so the idle one wins.
		if b, _ := p.Pick(&Request{}, candidates); b != candidates[1] {
			t.Fatalf("p2c picked the busier backend %s", b.ID())
		}
	}
	single := candidates[:1]
	if b, _ := p.Pick(&Request{}, single); b != single[0] {
		t.Error("p2c with one candidate did not pick it")
	}
}

func TestPrefixAffinity(t *testing.T) {
	t.Parallel()
	candidates := newCandidates(t, 3)
	p, _ := New(PolicyPrefixAffinity, testOptions())

	// The longest match wins even when that backend is the busiest.
	for range 10 {
		candidates[1].Admit(0)
	}
	req := &Request{Blocks: 10, Matched: []int{2, 9, 0}}
	if b, _ := p.Pick(req, candidates); b != candidates[1] {
		t.Errorf("picked %s, want b1 with the longest match", b.ID())
	}

	// Equal matches fall back to the less loaded backend.
	req = &Request{Blocks: 10, Matched: []int{4, 4, 1}}
	if b, _ := p.Pick(req, candidates); b != candidates[0] {
		t.Errorf("picked %s, want b0: same match as b1 but idle", b.ID())
	}

	// With no prefix information it degrades to least loaded.
	if b, _ := p.Pick(&Request{}, candidates); b == candidates[1] {
		t.Error("picked the busiest backend without any prefix match")
	}
}

func TestRandomOnlyPicksCandidates(t *testing.T) {
	t.Parallel()
	candidates := newCandidates(t, 3)
	allowed := map[*backend.Backend]bool{}
	for _, b := range candidates {
		allowed[b] = true
	}
	p, _ := New(PolicyRandom, testOptions())
	seen := map[string]bool{}
	for range 300 {
		b, err := p.Pick(&Request{}, candidates)
		if err != nil {
			t.Fatal(err)
		}
		if !allowed[b] {
			t.Fatalf("random picked %s, which is not a candidate", b.ID())
		}
		seen[b.ID()] = true
	}
	// With 300 draws over 3 candidates, missing one has probability
	// 3 * (2/3)^300, effectively zero.
	if len(seen) != len(candidates) {
		t.Errorf("random reached %d of %d candidates in 300 picks", len(seen), len(candidates))
	}
}

// testOptions returns options that satisfy every policy, for pools of up to
// 16 backends.
func testOptions() Options {
	return Options{
		Estimator: NewEstimator(EstimatorConfig{
			InitialPrefillTokensPerSecond: 10000,
			Overhead:                      10 * time.Millisecond,
			InitialBytesPerToken:          4,
			BlockBytes:                    64,
		}, 16),
		BalanceAbs: 16,
		BalanceRel: 1.5,
		TieEpsilon: 0.05,
	}
}
