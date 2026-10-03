// Package cache is an epoch-validated, size-bounded plan cache.
//
// Concurrency contract:
//   - readers never observe partially written entries: entries are built
//     outside the lock and published with a single map store;
//   - concurrent planners of the same key share one planning pass
//     (singleflight): the first caller plans, others wait on a channel;
//   - a failed or cancelled planning pass is never stored.
package cache

import (
	"sync"

	"github.com/guoyyit/query-optimizer-go/plan"
)

// Entry is an immutable cached plan plus the catalog epoch it is valid for.
type Entry struct {
	Plan    plan.Physical
	Epoch   uint64
	Explain string
	// Payload carries planner-specific metadata (notes, markers).
	Payload any
}

// Cache maps query fingerprints to plans.
type Cache struct {
	mu       sync.Mutex
	entries  map[string]*Entry
	order    []string // FIFO eviction
	max      int
	inflight map[string]*call
	hits     int64
	misses   int64
}

type call struct {
	done  chan struct{}
	entry *Entry
	err   error
}

func New(maxEntries int) *Cache {
	return &Cache{entries: map[string]*Entry{}, inflight: map[string]*call{}, max: maxEntries}
}

// Get returns the cached plan for key valid at epoch, or nil. Entries from
// a different epoch are treated as absent and evicted lazily.
func (c *Cache) Get(key string, epoch uint64) *Entry {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.entries[key]
	if !ok {
		c.misses++
		return nil
	}
	if e.Epoch != epoch {
		delete(c.entries, key)
		c.misses++
		return nil
	}
	c.hits++
	return e
}

// GetOrPlan returns the cached entry if valid, otherwise runs planFn once
// per key (concurrent callers share the result) and stores its result.
// A cancelled/failed planFn result is never stored. The returned bool
// reports whether a valid cached entry was served (singleflight waiters
// observe false: the plan was not in the cache when they arrived).
func (c *Cache) GetOrPlan(key string, epoch uint64, planFn func() (*Entry, error)) (*Entry, bool, error) {
	if c.max <= 0 {
		e, err := planFn() // caching disabled
		return e, false, err
	}
	c.mu.Lock()
	if e, ok := c.entries[key]; ok && e.Epoch == epoch {
		c.hits++
		c.mu.Unlock()
		return e, true, nil
	}
	if cl, ok := c.inflight[key]; ok {
		c.mu.Unlock()
		<-cl.done
		return cl.entry, false, cl.err
	}
	cl := &call{done: make(chan struct{})}
	c.inflight[key] = cl
	c.misses++
	c.mu.Unlock()

	entry, err := planFn()

	c.mu.Lock()
	delete(c.inflight, key)
	if err == nil && entry != nil {
		entry.Epoch = epoch
		c.entries[key] = entry
		c.order = append(c.order, key)
		for len(c.entries) > c.max {
			victim := c.order[0]
			c.order = c.order[1:]
			delete(c.entries, victim)
		}
	}
	cl.entry, cl.err = entry, err
	close(cl.done)
	c.mu.Unlock()
	return entry, false, err
}

// InvalidateAll drops every cached entry.
func (c *Cache) InvalidateAll() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries = map[string]*Entry{}
	c.order = nil
}

// Len reports the number of cached entries.
func (c *Cache) Len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.entries)
}

// Stats returns cache hit/miss counters.
func (c *Cache) Stats() (hits, misses int64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.hits, c.misses
}
