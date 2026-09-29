package prefix

import (
	"fmt"
	"slices"
	"sync"
	"testing"
	"time"
)

// fakeClock is a manually advanced clock.
type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fakeClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

func seq(from uint64, n int) []uint64 {
	h := make([]uint64, n)
	for i := range h {
		h[i] = from
		from++
	}
	return h
}

func match(ix *Index, hashes []uint64, gens []uint64) []int {
	m := make([]int, len(gens))
	ix.Match(hashes, gens, m)
	return m
}

func TestMatchCountsLeadingBlocksPerBackend(t *testing.T) {
	t.Parallel()
	ix := NewIndex([]int{100, 100, 100}, time.Hour, nil)
	gens := []uint64{1, 1, 1}
	ix.Insert(0, 1, seq(1, 5))                    // blocks 1..5
	ix.Insert(1, 1, seq(1, 2))                    // blocks 1..2
	ix.Insert(2, 1, []uint64{1, 2, 99, 100, 101}) // diverges at block 3

	got := match(ix, seq(1, 8), gens)
	if want := []int{5, 2, 2}; !slices.Equal(got, want) {
		t.Errorf("Match = %v, want %v", got, want)
	}
}

func TestMatchIgnoresStaleGenerations(t *testing.T) {
	t.Parallel()
	ix := NewIndex([]int{100}, time.Hour, nil)
	ix.Insert(0, 1, seq(1, 4))
	if got := match(ix, seq(1, 4), []uint64{2}); got[0] != 0 {
		t.Errorf("Match after a generation bump = %d, want 0", got[0])
	}
	ix.Insert(0, 2, seq(1, 4))
	if got := match(ix, seq(1, 4), []uint64{2}); got[0] != 4 {
		t.Errorf("Match after re-insert in the new generation = %d, want 4", got[0])
	}
}

func TestMatchIgnoresExpiredBeliefs(t *testing.T) {
	t.Parallel()
	clock := &fakeClock{t: time.Unix(0, 0)}
	ix := NewIndex([]int{100}, time.Minute, clock.now)
	ix.Insert(0, 1, seq(1, 3))
	clock.advance(30 * time.Second)
	if got := match(ix, seq(1, 3), []uint64{1}); got[0] != 3 {
		t.Fatalf("Match within TTL = %d, want 3", got[0])
	}
	clock.advance(time.Minute)
	if got := match(ix, seq(1, 3), []uint64{1}); got[0] != 0 {
		t.Errorf("Match after TTL = %d, want 0", got[0])
	}
	// Expired beliefs are dropped on the next insert.
	ix.Insert(0, 1, seq(50, 1))
	if ix.Len() != 1 {
		t.Errorf("Len after insert = %d, want 1 (expired beliefs evicted)", ix.Len())
	}
}

func TestCapacityEvictsOldestPrefixFromTheTail(t *testing.T) {
	t.Parallel()
	ix := NewIndex([]int{4}, time.Hour, nil)
	ix.Insert(0, 1, seq(1, 4))   // old prefix fills the budget
	ix.Insert(0, 1, seq(100, 2)) // new prefix needs 2 blocks

	if ix.Len() != 4 {
		t.Fatalf("Len = %d, want the budget of 4", ix.Len())
	}
	if got := match(ix, seq(1, 4), []uint64{1}); got[0] != 2 {
		t.Errorf("old prefix matches %d blocks, want 2: eviction must take its tail", got[0])
	}
	if got := match(ix, seq(100, 2), []uint64{1}); got[0] != 2 {
		t.Errorf("new prefix matches %d blocks, want 2", got[0])
	}
}

func TestReinsertRefreshesRecency(t *testing.T) {
	t.Parallel()
	ix := NewIndex([]int{4}, time.Hour, nil)
	ix.Insert(0, 1, seq(1, 2))
	ix.Insert(0, 1, seq(10, 2))
	ix.Insert(0, 1, seq(1, 2)) // prefix 1 is used again
	ix.Insert(0, 1, seq(20, 2))

	if got := match(ix, seq(1, 2), []uint64{1}); got[0] != 2 {
		t.Errorf("recently used prefix matches %d blocks, want 2", got[0])
	}
	if got := match(ix, seq(10, 2), []uint64{1}); got[0] != 0 {
		t.Errorf("least recently used prefix matches %d blocks, want 0 (evicted)", got[0])
	}
}

func TestBackendsHaveIndependentBudgets(t *testing.T) {
	t.Parallel()
	ix := NewIndex([]int{2, 10}, time.Hour, nil)
	ix.Insert(0, 1, seq(1, 5))
	ix.Insert(1, 1, seq(1, 5))
	got := match(ix, seq(1, 5), []uint64{1, 1})
	if want := []int{2, 5}; !slices.Equal(got, want) {
		t.Errorf("Match = %v, want %v", got, want)
	}
}

func TestRemoveAndClear(t *testing.T) {
	t.Parallel()
	ix := NewIndex([]int{100, 100}, time.Hour, nil)
	gens := []uint64{1, 1}
	ix.Insert(0, 1, seq(1, 4))
	ix.Insert(1, 1, seq(1, 4))

	// Evicting block 3 on backend 0 cuts its match there; backend 1 keeps it.
	ix.Remove(0, []uint64{3, 999})
	if got := match(ix, seq(1, 4), gens); !slices.Equal(got, []int{2, 4}) {
		t.Errorf("Match after Remove = %v, want [2 4]", got)
	}

	ix.Clear(1)
	if got := match(ix, seq(1, 4), gens); !slices.Equal(got, []int{2, 0}) {
		t.Errorf("Match after Clear(1) = %v, want [2 0]", got)
	}
	if ix.Len() != 3 {
		t.Errorf("Len = %d, want 3 (backend 0's remaining blocks)", ix.Len())
	}
}

func TestIndexConcurrentUse(t *testing.T) {
	t.Parallel()
	ix := NewIndex([]int{64, 64, 64, 64}, time.Hour, nil)
	var wg sync.WaitGroup
	for w := range 8 {
		wg.Go(func() {
			m := make([]int, 4)
			gens := []uint64{1, 1, 1, 1}
			for i := range 500 {
				h := seq(uint64(i%20), 8)
				ix.Match(h, gens, m)
				ix.Insert((w+i)%4, 1, h)
			}
		})
	}
	wg.Wait()
	if n := ix.Len(); n > 4*64 {
		t.Errorf("Len = %d, exceeds total budget %d", n, 4*64)
	}
}

func benchmarkMatch(b *testing.B, backends, blocks int) {
	capacities := make([]int, backends)
	gens := make([]uint64, backends)
	for i := range capacities {
		capacities[i], gens[i] = 1<<20, 1
	}
	ix := NewIndex(capacities, time.Hour, nil)
	hashes := seq(1, blocks)
	for bk := range backends {
		ix.Insert(bk, 1, hashes[:blocks*(bk+1)/backends]) // staggered overlap
	}
	matched := make([]int, backends)
	b.ReportAllocs()
	for b.Loop() {
		ix.Match(hashes, gens, matched)
	}
}

func BenchmarkMatch(b *testing.B) {
	for _, backends := range []int{4, 16} {
		for _, blocks := range []int{64, 1024, 4096} {
			b.Run(fmt.Sprintf("backends=%d/blocks=%d", backends, blocks), func(b *testing.B) {
				benchmarkMatch(b, backends, blocks)
			})
		}
	}
}

func BenchmarkInsert(b *testing.B) {
	for _, blocks := range []int{64, 1024} {
		b.Run(fmt.Sprintf("blocks=%d", blocks), func(b *testing.B) {
			ix := NewIndex([]int{1 << 16}, time.Hour, nil)
			hashes := seq(1, blocks)
			b.ReportAllocs()
			for b.Loop() {
				ix.Insert(0, 1, hashes)
			}
		})
	}
}
