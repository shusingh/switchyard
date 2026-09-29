package admission

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

// clock is a manually advanced clock for bucket arithmetic.
type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *clock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

func newController(t *testing.T, cfg Config, now func() time.Time) *Controller {
	t.Helper()
	if cfg.Default.Name == "" {
		cfg.Default = TenantConfig{Name: "default", Weight: 1, MaxQueued: 100}
	}
	if cfg.QueueTimeout == 0 {
		cfg.QueueTimeout = 5 * time.Second
	}
	if cfg.Quantum == 0 {
		cfg.Quantum = 100
	}
	c, err := New(cfg, now)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestAdmitImmediatelyUnderCapacity(t *testing.T) {
	t.Parallel()
	c := newController(t, Config{MaxInFlight: 2}, nil)
	a, err := c.Admit(context.Background(), "default", 10)
	if err != nil {
		t.Fatal(err)
	}
	b, err := c.Admit(context.Background(), "default", 10)
	if err != nil {
		t.Fatal(err)
	}
	if s := c.Stats(); s.InFlight != 2 || s.Queued != 0 {
		t.Errorf("Stats = %+v, want 2 in flight, 0 queued", s)
	}
	a.Release(-1)
	b.Release(-1)
	b.Release(-1) // idempotent
	if s := c.Stats(); s.InFlight != 0 {
		t.Errorf("InFlight after release = %d, want 0", s.InFlight)
	}
}

func TestTokenBudget(t *testing.T) {
	t.Parallel()
	clk := &clock{t: time.Unix(0, 0)}
	c := newController(t, Config{
		MaxInFlight: 100,
		Default:     TenantConfig{Name: "default", Weight: 1, MaxQueued: 10, TokensPerSecond: 100, Burst: 1000},
	}, clk.now)

	g, err := c.Admit(context.Background(), "default", 900)
	if err != nil {
		t.Fatalf("first request within burst: %v", err)
	}
	_, err = c.Admit(context.Background(), "default", 500)
	var reject *RejectError
	if !errors.As(err, &reject) || !errors.Is(err, ErrRateLimited) {
		t.Fatalf("over-budget request error = %v, want ErrRateLimited", err)
	}
	// 100 tokens remain; 400 more accrue in 4s at 100 per second.
	if reject.RetryAfter != 4*time.Second {
		t.Errorf("RetryAfter = %v, want 4s", reject.RetryAfter)
	}

	// The first request actually cost 100, not 900: the refund pays for the
	// second request immediately.
	g.Release(100)
	if _, err := c.Admit(context.Background(), "default", 500); err != nil {
		t.Errorf("after a refund the request should fit: %v", err)
	}
}

func TestQueueGrantsInOrderWhenCapacityFrees(t *testing.T) {
	t.Parallel()
	c := newController(t, Config{MaxInFlight: 1}, nil)
	first, _ := c.Admit(context.Background(), "default", 1)

	got := make(chan *Grant, 1)
	go func() {
		g, err := c.Admit(context.Background(), "default", 1)
		if err != nil {
			t.Error(err)
		}
		got <- g
	}()
	waitFor(t, func() bool { return c.Stats().Queued == 1 })
	first.Release(-1)
	g := <-got
	if s := c.Stats(); s.InFlight != 1 || s.Queued != 0 {
		t.Errorf("Stats after hand-off = %+v, want 1 in flight, 0 queued", s)
	}
	g.Release(-1)
}

func TestQueueFullAndTimeout(t *testing.T) {
	t.Parallel()
	c := newController(t, Config{
		MaxInFlight:  1,
		QueueTimeout: 50 * time.Millisecond,
		Default:      TenantConfig{Name: "default", Weight: 1, MaxQueued: 1},
	}, nil)
	held, _ := c.Admit(context.Background(), "default", 1)
	defer held.Release(-1)

	timedOut := make(chan error, 1)
	go func() {
		_, err := c.Admit(context.Background(), "default", 1)
		timedOut <- err
	}()
	waitFor(t, func() bool { return c.Stats().Queued == 1 })

	if _, err := c.Admit(context.Background(), "default", 1); !errors.Is(err, ErrQueueFull) {
		t.Errorf("request beyond MaxQueued: error = %v, want ErrQueueFull", err)
	}
	if err := <-timedOut; !errors.Is(err, ErrQueueTimeout) {
		t.Errorf("queued request: error = %v, want ErrQueueTimeout", err)
	}
	if s := c.Stats(); s.Queued != 0 || s.InFlight != 1 {
		t.Errorf("Stats after timeout = %+v, want 1 in flight, 0 queued", s)
	}
}

func TestCancelledWaiterLeavesQueue(t *testing.T) {
	t.Parallel()
	c := newController(t, Config{MaxInFlight: 1}, nil)
	held, _ := c.Admit(context.Background(), "default", 1)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := c.Admit(ctx, "default", 1)
		done <- err
	}()
	waitFor(t, func() bool { return c.Stats().Queued == 1 })
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Errorf("error = %v, want context.Canceled", err)
	}
	held.Release(-1)
	if s := c.Stats(); s != (Stats{}) {
		t.Errorf("Stats = %+v, want empty: the cancelled waiter must not be granted", s)
	}
}

func TestWeightedFairness(t *testing.T) {
	t.Parallel()
	c := newController(t, Config{
		MaxInFlight: 1,
		Quantum:     10,
		Default:     TenantConfig{Name: "default", Weight: 1, MaxQueued: 100},
		Tenants: []TenantConfig{
			{Name: "heavy", Weight: 1, MaxQueued: 100},
			{Name: "light", Weight: 3, MaxQueued: 100},
		},
	}, nil)
	held, _ := c.Admit(context.Background(), "heavy", 10)

	// Both tenants queue 40 equal-cost requests while capacity is held.
	var mu sync.Mutex
	var order []string
	var wg sync.WaitGroup
	for _, name := range []string{"heavy", "light"} {
		for range 40 {
			wg.Go(func() {
				g, err := c.Admit(context.Background(), name, 10)
				if err != nil {
					t.Error(err)
					return
				}
				mu.Lock()
				order = append(order, g.Tenant())
				mu.Unlock()
				g.Release(-1)
			})
		}
	}
	waitFor(t, func() bool { return c.Stats().Queued == 80 })
	held.Release(-1)
	wg.Wait()

	// While both tenants are backlogged, light (weight 3) should get about
	// three grants for each of heavy's.
	light := 0
	for _, name := range order[:40] {
		if name == "light" {
			light++
		}
	}
	if light < 26 || light > 34 {
		t.Errorf("light received %d of the first 40 grants, want about 30 for a 3:1 weight", light)
	}
}

func TestTenantForKey(t *testing.T) {
	t.Parallel()
	c := newController(t, Config{
		MaxInFlight: 1,
		Tenants:     []TenantConfig{{Name: "team-a", Weight: 1, APIKeys: []string{"sk-a1", "sk-a2"}}},
	}, nil)
	for key, want := range map[string]string{"sk-a1": "team-a", "sk-a2": "team-a", "sk-other": "default", "": "default"} {
		if got := c.TenantForKey(key); got != want {
			t.Errorf("TenantForKey(%q) = %q, want %q", key, got, want)
		}
	}
}

func TestNewRejectsDuplicates(t *testing.T) {
	t.Parallel()
	for _, tenants := range [][]TenantConfig{
		{{Name: "a"}, {Name: "a"}},
		{{Name: "a", APIKeys: []string{"k"}}, {Name: "b", APIKeys: []string{"k"}}},
	} {
		if _, err := New(Config{Default: TenantConfig{Name: "default"}, Tenants: tenants}, nil); err == nil {
			t.Errorf("New(%+v) error = nil, want a duplicate error", tenants)
		}
	}
}

// waitFor polls cond until it holds or the test times out. Admission state
// changes in other goroutines, so tests observe it rather than sleep.
func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("condition not reached within 5s")
		}
		time.Sleep(time.Millisecond)
	}
}
