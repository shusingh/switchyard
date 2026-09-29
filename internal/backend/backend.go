// Package backend tracks the model servers Switchyard routes to: their
// identity, health, and in-flight load.
package backend

import (
	"fmt"
	"net/url"
	"sync/atomic"

	"github.com/shusingh/switchyard/internal/config"
)

// Backend is one model server. Its exported methods are safe for concurrent
// use.
type Backend struct {
	id    string
	index int
	base  *url.URL

	healthy    atomic.Bool
	generation atomic.Uint64

	// Load, maintained through Tickets. See load.go.
	inFlight       atomic.Int64
	pendingPrefill atomic.Int64
	decoding       atomic.Int64

	// Health-check bookkeeping. Only the HealthChecker reads or writes these
	// fields, from one goroutine per backend per round, and rounds never
	// overlap, so they need no synchronization.
	checked    bool
	okStreak   int
	failStreak int
}

// ID returns the backend's configured identifier.
func (b *Backend) ID() string { return b.id }

// Index returns the backend's position in the pool, from 0. Per-backend state
// elsewhere, such as the prefix index, is kept in slices indexed by it.
func (b *Backend) Index() int { return b.index }

// Healthy reports whether the backend is currently eligible for traffic.
func (b *Backend) Healthy() bool { return b.healthy.Load() }

// Generation increases every time the backend becomes healthy. State derived
// from an earlier generation, such as cached-prefix beliefs, is stale because
// the server may have restarted with an empty cache.
func (b *Backend) Generation() uint64 { return b.generation.Load() }

// Endpoint returns the absolute URL for path and rawQuery on this backend.
func (b *Backend) Endpoint(path, rawQuery string) *url.URL {
	u := b.base.JoinPath(path)
	u.RawQuery = rawQuery
	return u
}

// setHealthy updates the health flag and reports whether it changed. Becoming
// healthy starts a new generation.
func (b *Backend) setHealthy(healthy bool) bool {
	if b.healthy.Swap(healthy) == healthy {
		return false
	}
	if healthy {
		b.generation.Add(1)
	}
	return true
}

// Pool is the fixed set of configured backends.
type Pool struct {
	backends []*Backend
}

// NewPool builds a pool from configuration. Backends start unhealthy until
// their first successful health check.
func NewPool(cfgs []config.Backend) (*Pool, error) {
	p := &Pool{backends: make([]*Backend, 0, len(cfgs))}
	for i, c := range cfgs {
		u, err := url.Parse(c.URL)
		if err != nil {
			return nil, fmt.Errorf("backend %s: parse url: %w", c.ID, err)
		}
		p.backends = append(p.backends, &Backend{id: c.ID, index: i, base: u})
	}
	return p, nil
}

// Backends returns every backend in configuration order. The slice is shared
// and must not be modified.
func (p *Pool) Backends() []*Backend { return p.backends }

// AppendHealthy appends the currently healthy backends to dst and returns the
// extended slice. Callers on the request path pass a reused buffer to avoid
// allocating.
func (p *Pool) AppendHealthy(dst []*Backend) []*Backend {
	for _, b := range p.backends {
		if b.Healthy() {
			dst = append(dst, b)
		}
	}
	return dst
}

// Generations appends every backend's current generation to dst, in pool
// order, and returns the extended slice.
func (p *Pool) Generations(dst []uint64) []uint64 {
	for _, b := range p.backends {
		dst = append(dst, b.Generation())
	}
	return dst
}

// AnyHealthy reports whether at least one backend is healthy.
func (p *Pool) AnyHealthy() bool {
	for _, b := range p.backends {
		if b.Healthy() {
			return true
		}
	}
	return false
}
