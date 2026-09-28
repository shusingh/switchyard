// Package scheduler chooses which backend serves each request.
//
// Every routing policy implements Policy and runs through the same request
// path, so baselines and Switchyard's own policy are compared under identical
// conditions. See docs/engineering/design.md section 9.
package scheduler

import (
	"errors"
	"fmt"
	"math/rand/v2"
	"slices"
	"sync/atomic"

	"github.com/shusingh/switchyard/internal/backend"
)

// Policy names accepted by New.
const (
	PolicyRoundRobin = "round_robin"
	PolicyRandom     = "random"
)

// ErrNoCandidates is returned when there is no healthy backend to choose.
var ErrNoCandidates = errors.New("no healthy backend")

// Request describes the request being routed.
type Request struct {
	Model string
}

// Policy chooses a backend for a request. Implementations must be safe for
// concurrent use and must not block: Pick runs on every request's hot path.
type Policy interface {
	// Name returns the policy's configuration name.
	Name() string
	// Pick returns one of candidates, which are all healthy, or
	// ErrNoCandidates if candidates is empty.
	Pick(req *Request, candidates []*backend.Backend) (*backend.Backend, error)
}

// Names returns the names of all available policies, sorted.
func Names() []string {
	names := []string{PolicyRoundRobin, PolicyRandom}
	slices.Sort(names)
	return names
}

// New returns the policy with the given name.
func New(name string) (Policy, error) {
	switch name {
	case PolicyRoundRobin:
		return &roundRobin{}, nil
	case PolicyRandom:
		return randomPolicy{}, nil
	default:
		return nil, fmt.Errorf("unknown routing policy %q (valid: %v)", name, Names())
	}
}

// roundRobin cycles through the candidates. When the healthy set changes the
// rotation continues from the same counter, so load stays evenly spread.
type roundRobin struct {
	next atomic.Uint64
}

func (*roundRobin) Name() string { return PolicyRoundRobin }

func (p *roundRobin) Pick(_ *Request, candidates []*backend.Backend) (*backend.Backend, error) {
	if len(candidates) == 0 {
		return nil, ErrNoCandidates
	}
	n := p.next.Add(1) - 1
	return candidates[n%uint64(len(candidates))], nil
}

// randomPolicy picks uniformly at random; it is the control group in
// benchmarks.
type randomPolicy struct{}

func (randomPolicy) Name() string { return PolicyRandom }

func (randomPolicy) Pick(_ *Request, candidates []*backend.Backend) (*backend.Backend, error) {
	if len(candidates) == 0 {
		return nil, ErrNoCandidates
	}
	return candidates[rand.IntN(len(candidates))], nil //nolint:gosec // G404: load balancing, not security
}
