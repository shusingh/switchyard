// Package admission decides whether and when a request may proceed:
// per-tenant token budgets, a global concurrency cap, and fair queuing across
// tenants when the cap is reached. Overload is shed early with a retry hint
// instead of queuing without bound. See docs/engineering/design.md section 10.
package admission

import (
	"container/list"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"sync"
	"time"
)

var (
	// ErrRateLimited means the tenant's token budget is exhausted.
	ErrRateLimited = errors.New("tenant token budget exhausted")
	// ErrQueueFull means the tenant already has the maximum number of
	// requests waiting.
	ErrQueueFull = errors.New("admission queue full")
	// ErrQueueTimeout means the request waited for capacity longer than the
	// queue timeout.
	ErrQueueTimeout = errors.New("timed out waiting for capacity")
)

// RejectError reports why a request was not admitted and when retrying might
// succeed.
type RejectError struct {
	Err        error
	RetryAfter time.Duration
}

func (e *RejectError) Error() string {
	return fmt.Sprintf("%v (retry after %s)", e.Err, e.RetryAfter.Round(time.Second))
}

func (e *RejectError) Unwrap() error { return e.Err }

// TenantConfig describes one tenant's share of capacity.
type TenantConfig struct {
	Name string
	// APIKeys identify the tenant from the request's bearer token.
	APIKeys []string
	// Weight is the tenant's share of capacity under contention, relative
	// to other tenants' weights.
	Weight int
	// TokensPerSecond and Burst size the tenant's token bucket. Zero
	// TokensPerSecond means no budget.
	TokensPerSecond float64
	Burst           float64
	// MaxQueued bounds how many of the tenant's requests may wait for
	// capacity at once.
	MaxQueued int
}

// Config configures a Controller.
type Config struct {
	// MaxInFlight caps concurrently admitted requests across all tenants.
	MaxInFlight int
	// QueueTimeout bounds how long a request waits for capacity.
	QueueTimeout time.Duration
	// Quantum is the cost credited to a tenant each time deficit round robin
	// visits it, times its weight. It should be near a typical request's
	// cost.
	Quantum float64
	// Default applies to requests that match no configured tenant.
	Default TenantConfig
	Tenants []TenantConfig
}

// Controller admits requests. It is safe for concurrent use.
type Controller struct {
	cfg   Config
	now   func() time.Time
	byKey map[[sha256.Size]byte]*tenant // key digests, so lookups never compare raw secrets

	mu       sync.Mutex
	byName   map[string]*tenant // guarded by mu
	ring     []*tenant          // guarded by mu; deficit round robin order
	cursor   int                // guarded by mu
	inFlight int                // guarded by mu
	queued   int                // guarded by mu
}

type tenant struct {
	cfg     TenantConfig
	bucket  *bucket
	queue   list.List // of *waiter, guarded by Controller.mu
	deficit float64   // guarded by Controller.mu
}

type waiter struct {
	cost    float64
	ready   chan struct{} // closed when granted
	granted bool          // guarded by Controller.mu
	elem    *list.Element
}

// New returns a Controller. now supplies the clock; nil means time.Now.
func New(cfg Config, now func() time.Time) (*Controller, error) {
	if now == nil {
		now = time.Now
	}
	c := &Controller{
		cfg:    cfg,
		now:    now,
		byKey:  make(map[[sha256.Size]byte]*tenant),
		byName: make(map[string]*tenant),
	}
	add := func(tc TenantConfig) error {
		if _, dup := c.byName[tc.Name]; dup {
			return fmt.Errorf("duplicate tenant %q", tc.Name)
		}
		t := &tenant{cfg: tc, bucket: newBucket(tc.TokensPerSecond, tc.Burst, now())}
		c.byName[tc.Name] = t
		c.ring = append(c.ring, t)
		for _, k := range tc.APIKeys {
			d := sha256.Sum256([]byte(k))
			if _, dup := c.byKey[d]; dup {
				return fmt.Errorf("tenant %q: API key assigned to more than one tenant", tc.Name)
			}
			c.byKey[d] = t
		}
		return nil
	}
	if err := add(cfg.Default); err != nil {
		return nil, err
	}
	for _, tc := range cfg.Tenants {
		if err := add(tc); err != nil {
			return nil, err
		}
	}
	return c, nil
}

// TenantForKey returns the tenant a bearer token belongs to, or the default
// tenant's name if it matches none.
func (c *Controller) TenantForKey(apiKey string) string {
	if apiKey != "" {
		if t, ok := c.byKey[sha256.Sum256([]byte(apiKey))]; ok {
			return t.cfg.Name
		}
	}
	return c.cfg.Default.Name
}

// Grant is an admitted request's hold on capacity. Release must be called
// exactly once when the request finishes.
type Grant struct {
	c        *Controller
	t        *tenant
	cost     float64
	released bool
}

// Tenant returns the name of the tenant the grant was charged to.
func (g *Grant) Tenant() string { return g.t.cfg.Name }

// Release returns the grant's capacity and corrects the tenant's budget with
// the request's actual cost. A negative actualCost keeps the estimate.
func (g *Grant) Release(actualCost float64) {
	c := g.c
	c.mu.Lock()
	defer c.mu.Unlock()
	if g.released {
		return
	}
	g.released = true
	if actualCost >= 0 {
		g.t.bucket.adjust(actualCost-g.cost, c.now())
	}
	c.inFlight--
	c.dispatch()
}

// Admit asks to run a request of the given estimated cost for the named
// tenant (unknown names use the default tenant). It returns at once if there
// is capacity, waits in the tenant's queue if not, and returns a
// *RejectError if the tenant is over budget, its queue is full, or the wait
// times out. It returns ctx's error if ctx ends first.
func (c *Controller) Admit(ctx context.Context, tenantName string, cost float64) (*Grant, error) {
	c.mu.Lock()
	t, ok := c.byName[tenantName]
	if !ok {
		t = c.byName[c.cfg.Default.Name]
	}
	now := c.now()
	if wait := t.bucket.take(cost, now); wait > 0 {
		c.mu.Unlock()
		return nil, &RejectError{Err: ErrRateLimited, RetryAfter: wait}
	}
	// Requests queue behind any that are already waiting, so a burst from
	// one tenant cannot jump ahead of the fair order.
	if c.inFlight < c.cfg.MaxInFlight && c.queued == 0 {
		c.inFlight++
		c.mu.Unlock()
		return &Grant{c: c, t: t, cost: cost}, nil
	}
	if t.queue.Len() >= t.cfg.MaxQueued {
		t.bucket.adjust(-cost, now) // refund: the request never ran
		c.mu.Unlock()
		return nil, &RejectError{Err: ErrQueueFull, RetryAfter: c.cfg.QueueTimeout}
	}
	w := &waiter{cost: cost, ready: make(chan struct{})}
	w.elem = t.queue.PushBack(w)
	c.queued++
	c.mu.Unlock()

	timer := time.NewTimer(c.cfg.QueueTimeout)
	defer timer.Stop()
	var cause error
	select {
	case <-w.ready:
		return &Grant{c: c, t: t, cost: cost}, nil
	case <-ctx.Done():
		cause = ctx.Err()
	case <-timer.C:
		cause = &RejectError{Err: ErrQueueTimeout, RetryAfter: c.cfg.QueueTimeout}
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	if w.granted {
		// Granted at the same moment the wait ended; give the slot back so
		// it is not leaked.
		c.inFlight--
		c.dispatch()
	} else {
		t.queue.Remove(w.elem)
		c.queued--
	}
	t.bucket.adjust(-cost, c.now()) // refund: the request never ran
	return nil, cause
}

// dispatch grants waiting requests while there is capacity, choosing among
// tenants by deficit round robin: each visit credits a tenant Quantum times
// its weight, and the tenant's oldest request runs once the credit covers its
// cost. Over time each backlogged tenant receives capacity in proportion to
// its weight, measured in cost rather than request count. The caller holds
// c.mu.
func (c *Controller) dispatch() {
	for c.inFlight < c.cfg.MaxInFlight && c.queued > 0 {
		t := c.ring[c.cursor]
		front := t.queue.Front()
		if front == nil {
			t.deficit = 0 // an idle tenant does not bank credit
			c.cursor = (c.cursor + 1) % len(c.ring)
			continue
		}
		w, _ := front.Value.(*waiter)
		if t.deficit < w.cost {
			t.deficit += c.cfg.Quantum * float64(max(t.cfg.Weight, 1))
			c.cursor = (c.cursor + 1) % len(c.ring)
			continue
		}
		t.deficit -= w.cost
		t.queue.Remove(front)
		c.queued--
		c.inFlight++
		w.granted = true
		close(w.ready)
	}
}

// Stats is a snapshot of admission state.
type Stats struct {
	InFlight int
	Queued   int
}

// Stats returns the current number of admitted and waiting requests.
func (c *Controller) Stats() Stats {
	c.mu.Lock()
	defer c.mu.Unlock()
	return Stats{InFlight: c.inFlight, Queued: c.queued}
}
