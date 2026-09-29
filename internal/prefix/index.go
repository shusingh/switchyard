package prefix

import (
	"sync"
	"time"
)

// Index records which backends are believed to hold each prefix block. It is
// approximate: it tracks what the router sent where, not what an engine
// actually kept, and relies on bounded budgets, a TTL, and backend
// generations to keep beliefs close to reality. See
// docs/engineering/design.md section 7.
//
// Index is safe for concurrent use. Matches take a shared lock and run in
// parallel; inserts take an exclusive lock.
type Index struct {
	ttl time.Duration
	now func() time.Time

	mu       sync.RWMutex
	entries  map[uint64][]*node // guarded by mu; at most one node per backend
	backends []backendList      // guarded by mu
}

// node is one (block, backend) belief. It is linked into its backend's LRU
// list.
type node struct {
	hash       uint64
	backend    int
	generation uint64
	lastUsed   time.Time
	prev, next *node
}

// backendList is a backend's nodes in recency order: head is most recent.
type backendList struct {
	head, tail *node
	size       int
	capacity   int
}

// NewIndex returns an index for backends numbered 0 to capacities-1, where
// capacities[b] is backend b's budget in blocks. Beliefs older than ttl are
// ignored. now supplies the clock; nil means time.Now.
func NewIndex(capacities []int, ttl time.Duration, now func() time.Time) *Index {
	if now == nil {
		now = time.Now
	}
	ix := &Index{
		ttl:      ttl,
		now:      now,
		entries:  make(map[uint64][]*node),
		backends: make([]backendList, len(capacities)),
	}
	for i, c := range capacities {
		ix.backends[i].capacity = c
	}
	return ix
}

// Match stores in matched[b] the number of leading blocks of hashes believed
// to be cached on backend b. Beliefs from an earlier generation than
// generations[b], or older than the TTL, do not count. matched and
// generations must have one element per backend.
//
// A backend's match ends at its first missing block: engines evict a prefix
// from its tail, so a cached prefix is contiguous from the start.
func (ix *Index) Match(hashes []uint64, generations []uint64, matched []int) {
	clear(matched)
	oldest := ix.now().Add(-ix.ttl)
	ix.mu.RLock()
	defer ix.mu.RUnlock()
	for i, h := range hashes {
		alive := false
		for _, n := range ix.entries[h] {
			b := n.backend
			if matched[b] == i && n.generation == generations[b] && n.lastUsed.After(oldest) {
				matched[b] = i + 1
				alive = true
			}
		}
		if !alive {
			return // no backend holds block i, so none holds anything after it
		}
	}
}

// Insert records that backend b, in generation gen, now holds hashes. The
// blocks become b's most recently used, with the first block most recent of
// all, so that when b's budget is exceeded the oldest prefixes are evicted
// from their tails.
func (ix *Index) Insert(b int, gen uint64, hashes []uint64) {
	now := ix.now()
	ix.mu.Lock()
	defer ix.mu.Unlock()
	list := &ix.backends[b]
	for i := len(hashes) - 1; i >= 0; i-- {
		n := ix.find(hashes[i], b)
		if n == nil {
			n = &node{hash: hashes[i], backend: b}
			ix.entries[n.hash] = append(ix.entries[n.hash], n)
			list.size++
		} else {
			list.unlink(n)
		}
		n.generation, n.lastUsed = gen, now
		list.pushFront(n)
	}
	for list.size > list.capacity || (list.tail != nil && now.Sub(list.tail.lastUsed) > ix.ttl) {
		ix.evict(list.tail)
	}
}

// Len returns the number of (block, backend) beliefs held.
func (ix *Index) Len() int {
	ix.mu.RLock()
	defer ix.mu.RUnlock()
	n := 0
	for i := range ix.backends {
		n += ix.backends[i].size
	}
	return n
}

// find returns backend b's node for hash h, or nil. The caller holds mu.
func (ix *Index) find(h uint64, b int) *node {
	for _, n := range ix.entries[h] {
		if n.backend == b {
			return n
		}
	}
	return nil
}

// evict removes n from its backend's list and from the entry map. The caller
// holds mu exclusively.
func (ix *Index) evict(n *node) {
	list := &ix.backends[n.backend]
	list.unlink(n)
	list.size--
	nodes := ix.entries[n.hash]
	for i, m := range nodes {
		if m == n {
			nodes[i] = nodes[len(nodes)-1]
			nodes[len(nodes)-1] = nil
			nodes = nodes[:len(nodes)-1]
			break
		}
	}
	if len(nodes) == 0 {
		delete(ix.entries, n.hash)
	} else {
		ix.entries[n.hash] = nodes
	}
}

func (l *backendList) pushFront(n *node) {
	n.prev, n.next = nil, l.head
	if l.head != nil {
		l.head.prev = n
	}
	l.head = n
	if l.tail == nil {
		l.tail = n
	}
}

func (l *backendList) unlink(n *node) {
	if n.prev != nil {
		n.prev.next = n.next
	} else {
		l.head = n.next
	}
	if n.next != nil {
		n.next.prev = n.prev
	} else {
		l.tail = n.prev
	}
	n.prev, n.next = nil, nil
}
