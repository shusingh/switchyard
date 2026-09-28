package scheduler

import (
	"errors"
	"fmt"
	"testing"

	"github.com/shusingh/switchyard/internal/backend"
	"github.com/shusingh/switchyard/internal/config"
)

func newCandidates(t *testing.T, n int) []*backend.Backend {
	t.Helper()
	cfgs := make([]config.Backend, n)
	for i := range n {
		cfgs[i] = config.Backend{ID: fmt.Sprintf("b%d", i), URL: fmt.Sprintf("http://localhost:%d", 8001+i)}
	}
	p, err := backend.NewPool(cfgs)
	if err != nil {
		t.Fatal(err)
	}
	return p.Backends()
}

func TestNew(t *testing.T) {
	t.Parallel()
	for _, name := range Names() {
		p, err := New(name)
		if err != nil {
			t.Errorf("New(%q) error = %v", name, err)
			continue
		}
		if p.Name() != name {
			t.Errorf("New(%q).Name() = %q", name, p.Name())
		}
	}
	if _, err := New("nonexistent"); err == nil {
		t.Error(`New("nonexistent") error = nil, want an error`)
	}
}

func TestPoliciesRejectEmptyCandidates(t *testing.T) {
	t.Parallel()
	for _, name := range Names() {
		p, _ := New(name)
		if _, err := p.Pick(&Request{}, nil); !errors.Is(err, ErrNoCandidates) {
			t.Errorf("%s.Pick(nil) error = %v, want %v", name, err, ErrNoCandidates)
		}
	}
}

func TestRoundRobinIsEven(t *testing.T) {
	t.Parallel()
	candidates := newCandidates(t, 4)
	p, _ := New(PolicyRoundRobin)
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

func TestRandomOnlyPicksCandidates(t *testing.T) {
	t.Parallel()
	candidates := newCandidates(t, 3)
	allowed := map[*backend.Backend]bool{}
	for _, b := range candidates {
		allowed[b] = true
	}
	p, _ := New(PolicyRandom)
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
