package sim

import "container/list"

// blockCache models vLLM's KV cache with automatic prefix caching.
//
// The cache holds a fixed number of blocks. A block that stores a full prompt
// block is indexed by its chained hash, so a later request with the same
// prefix can reuse it. Blocks referenced by running sequences are pinned.
// Cached blocks nobody references stay resident until space is needed, then
// they are evicted least recently used first. Blocks that are not reusable
// (a prompt's partial final block, output tokens) are private to their
// sequence and freed when it finishes.
//
// blockCache is not safe for concurrent use; the engine's scheduler loop owns
// it.
type blockCache struct {
	capacity int

	entries map[uint64]*cacheEntry
	// evictable holds cached entries with no references, most recently used
	// at the front.
	evictable list.List
	// private counts blocks held by running sequences that are not indexed.
	private int
	// pinned counts blocks referenced by running sequences, indexed or not.
	pinned int
}

type cacheEntry struct {
	hash uint64
	refs int
	elem *list.Element // set while the entry is evictable
}

func newBlockCache(capacity int) *blockCache {
	return &blockCache{capacity: capacity, entries: make(map[uint64]*cacheEntry)}
}

// free returns the number of blocks not occupied by any entry or sequence.
func (c *blockCache) free() int {
	return c.capacity - len(c.entries) - c.private
}

// match returns how many leading blocks of hashes are cached.
func (c *blockCache) match(hashes []uint64) int {
	for i, h := range hashes {
		if _, ok := c.entries[h]; !ok {
			return i
		}
	}
	return len(hashes)
}

// allocation records the blocks a sequence holds so they can be released.
type allocation struct {
	hashes  []uint64
	private int
}

// allocate reserves blocks for a sequence whose full prompt blocks are hashes
// and which needs privateBlocks more blocks (partial prompt tail and output).
// It reuses cached prefix blocks, evicts unreferenced blocks as needed, and
// reports how many prompt blocks were cache hits. It returns false, and
// changes nothing, if the blocks cannot fit.
func (c *blockCache) allocate(hashes []uint64, privateBlocks int) (allocation, int, bool) {
	hit := c.match(hashes)
	need := len(hashes) - hit + privateBlocks
	if need > c.free()+c.evictableCount(hashes[:hit]) {
		return allocation{}, 0, false
	}
	// Pin the hits first so eviction cannot take them.
	for _, h := range hashes[:hit] {
		c.ref(c.entries[h])
	}
	for c.free() < need {
		c.evictOne()
	}
	for _, h := range hashes[hit:] {
		// A later block can outlive an evicted earlier one when eviction
		// orders of different sequences interleave. Reuse it rather than
		// replace it; it is not counted as a hit because the prefix before
		// it has to be recomputed anyway.
		if e, ok := c.entries[h]; ok {
			c.ref(e)
			continue
		}
		e := &cacheEntry{hash: h}
		c.entries[h] = e
		c.ref(e)
	}
	c.private += privateBlocks
	c.pinned += privateBlocks
	return allocation{hashes: hashes, private: privateBlocks}, hit, true
}

// evictableCount returns how many blocks eviction could free, excluding
// entries in keep, which are about to be pinned.
func (c *blockCache) evictableCount(keep []uint64) int {
	n := c.evictable.Len()
	for _, h := range keep {
		if c.entries[h].refs == 0 {
			n--
		}
	}
	return n
}

// release returns a sequence's blocks. Its prompt blocks stay cached and
// become evictable; its private blocks are freed.
//
// Blocks are made evictable from the last to the first, so the first block is
// the most recently used and a prefix is evicted from its tail. That keeps a
// cached prefix contiguous from the start, as in vLLM.
func (c *blockCache) release(a allocation) {
	for i := len(a.hashes) - 1; i >= 0; i-- {
		e := c.entries[a.hashes[i]]
		e.refs--
		c.pinned--
		if e.refs == 0 {
			e.elem = c.evictable.PushFront(e)
		}
	}
	c.private -= a.private
	c.pinned -= a.private
}

// reset drops every unreferenced cached block, as vLLM's
// /reset_prefix_cache does.
func (c *blockCache) reset() {
	for c.evictable.Len() > 0 {
		c.evictOne()
	}
}

func (c *blockCache) ref(e *cacheEntry) {
	if e.refs == 0 && e.elem != nil {
		c.evictable.Remove(e.elem)
		e.elem = nil
	}
	e.refs++
	c.pinned++
}

func (c *blockCache) evictOne() {
	back := c.evictable.Back()
	e, _ := back.Value.(*cacheEntry)
	c.evictable.Remove(back)
	delete(c.entries, e.hash)
}
