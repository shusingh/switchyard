package sim

import (
	"math/rand/v2"
	"sync"
	"sync/atomic"
)

// Faults makes an engine misbehave on purpose, for testing how the router
// copes with failing backends.
type Faults struct {
	// ErrorRate is the fraction of requests answered 503 at once, as an
	// overloaded engine would.
	ErrorRate float64
	// AbortRate is the fraction of streaming requests whose connection is
	// dropped partway through generation, as if the engine crashed.
	AbortRate float64
	// Seed makes fault decisions reproducible. Zero picks a fixed seed.
	Seed uint64
}

// faultState holds an engine's fault configuration and runtime switches.
type faultState struct {
	cfg Faults

	mu  sync.Mutex
	rng *rand.Rand // guarded by mu

	unhealthy atomic.Bool
}

func newFaultState(cfg Faults) *faultState {
	seed := cfg.Seed
	if seed == 0 {
		seed = 1
	}
	return &faultState{cfg: cfg, rng: rand.New(rand.NewPCG(seed, 0xfa017))} //nolint:gosec // G404: test fault injection, not security
}

// roll reports whether an event with probability p happens.
func (f *faultState) roll(p float64) bool {
	if p <= 0 {
		return false
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.rng.Float64() < p
}

// SetUnhealthy makes the engine's /health endpoint fail (true) or pass
// (false), simulating a backend that drops out of and returns to service.
func (e *Engine) SetUnhealthy(unhealthy bool) { e.faults.unhealthy.Store(unhealthy) }

// Restart simulates an engine restart: its prefix cache is emptied, so
// anything the router believed was cached there is gone.
func (e *Engine) Restart() { e.sched.requestReset() }
