package sim

import "testing"

// hashes returns n distinct fake block hashes starting at from.
func hashes(from uint64, n int) []uint64 {
	h := make([]uint64, n)
	for i := range h {
		h[i] = from
		from++
	}
	return h
}

func TestCacheReusesPrefix(t *testing.T) {
	t.Parallel()
	c := newBlockCache(10)
	a, hit, ok := c.allocate(hashes(1, 4), 1)
	if !ok || hit != 0 {
		t.Fatalf("first allocate: ok=%v hit=%d, want true, 0", ok, hit)
	}
	c.release(a)

	// Same first three blocks, different fourth.
	b, hit, ok := c.allocate([]uint64{1, 2, 3, 99}, 1)
	if !ok || hit != 3 {
		t.Fatalf("second allocate: ok=%v hit=%d, want true, 3", ok, hit)
	}
	c.release(b)
}

func TestCacheFailsWhenPinnedBlocksFillCapacity(t *testing.T) {
	t.Parallel()
	c := newBlockCache(5)
	a, _, ok := c.allocate(hashes(1, 3), 1) // 4 blocks pinned
	if !ok {
		t.Fatal("first allocate failed")
	}
	if _, _, ok := c.allocate(hashes(100, 2), 0); ok {
		t.Fatal("allocate beyond capacity succeeded while blocks are pinned")
	}
	if got := c.pinned; got != 4 {
		t.Errorf("a failed allocate changed pinned to %d, want 4", got)
	}
	c.release(a)
	if _, _, ok := c.allocate(hashes(100, 2), 0); !ok {
		t.Error("allocate failed after release, want eviction to make room")
	}
}

func TestCacheEvictsPrefixFromTheTail(t *testing.T) {
	t.Parallel()
	c := newBlockCache(4)
	a, _, _ := c.allocate(hashes(1, 4), 0)
	c.release(a)

	// Needing two blocks evicts the last two blocks of the cached prefix,
	// keeping the first two, so the prefix stays contiguous from the start.
	b, _, ok := c.allocate(hashes(50, 2), 0)
	if !ok {
		t.Fatal("allocate failed")
	}
	c.release(b)
	if got := c.match(hashes(1, 4)); got != 2 {
		t.Errorf("match after eviction = %d blocks, want 2 (the prefix head)", got)
	}
}

func TestCacheHitIsNotEvictedDuringItsOwnAllocation(t *testing.T) {
	t.Parallel()
	c := newBlockCache(4)
	a, _, _ := c.allocate(hashes(1, 3), 0)
	c.release(a)
	// Reuses blocks 1..3 and needs one more; the only free block is the
	// fourth, so nothing may be evicted.
	b, hit, ok := c.allocate(hashes(1, 3), 1)
	if !ok || hit != 3 {
		t.Fatalf("allocate: ok=%v hit=%d, want true, 3", ok, hit)
	}
	if c.free() != 0 || c.pinned != 4 {
		t.Errorf("free=%d pinned=%d, want 0 and 4", c.free(), c.pinned)
	}
	c.release(b)
}

func TestCacheReset(t *testing.T) {
	t.Parallel()
	c := newBlockCache(8)
	running, _, _ := c.allocate(hashes(1, 2), 0)
	done, _, _ := c.allocate(hashes(10, 2), 0)
	c.release(done)
	c.reset()
	if c.match(hashes(10, 2)) != 0 {
		t.Error("reset kept an unreferenced cached prefix")
	}
	if c.match(hashes(1, 2)) != 2 {
		t.Error("reset dropped blocks a running sequence still references")
	}
	c.release(running)
}

func TestCacheSharedBlocksNeedAllReleases(t *testing.T) {
	t.Parallel()
	c := newBlockCache(6)
	a, _, _ := c.allocate(hashes(1, 3), 0)
	b, hit, _ := c.allocate(hashes(1, 3), 0)
	if hit != 3 || c.pinned != 6 {
		t.Fatalf("hit=%d pinned=%d, want 3 and 6 (one reference per sequence)", hit, c.pinned)
	}
	c.release(a)
	if c.evictable.Len() != 0 {
		t.Error("blocks became evictable while another sequence still references them")
	}
	c.release(b)
	if c.evictable.Len() != 3 || c.pinned != 0 {
		t.Errorf("after both releases: evictable=%d pinned=%d, want 3 and 0", c.evictable.Len(), c.pinned)
	}
}
