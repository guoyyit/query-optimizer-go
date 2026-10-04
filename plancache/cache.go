// Package plancache caches physical plans keyed by query fingerprint plus
// catalog and configuration versions. Entries are immutable; a version
// mismatch always yields a miss, so a stale plan can never be reused.
package plancache

import (
	"sync"
	"sync/atomic"
)

// Key identifies a plan entry.
type Key struct {
	QueryHash      string
	CatalogVersion int64
	ConfigHash     string
}

// Entry is a cached, immutable plan.
type Entry struct {
	Key   Key
	Value any
}

// Cache is a concurrency-safe plan cache.
type Cache struct {
	mu      sync.RWMutex
	entries map[Key]*Entry
	hits    atomic.Int64
	misses  atomic.Int64
}

// New creates an empty cache.
func New() *Cache {
	return &Cache{entries: map[Key]*Entry{}}
}

// Get returns the entry for key or (nil, false) on a miss.
func (c *Cache) Get(k Key) (*Entry, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	e, ok := c.entries[k]
	if !ok {
		c.misses.Add(1)
		return nil, false
	}
	c.hits.Add(1)
	return e, true
}

// Put stores an entry.
func (c *Cache) Put(k Key, v any) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries[k] = &Entry{Key: k, Value: v}
}

// Invalidate clears all entries.
func (c *Cache) Invalidate() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries = map[Key]*Entry{}
}

// Stats returns hit/miss counters.
func (c *Cache) Stats() (hits, misses int64) {
	return c.hits.Load(), c.misses.Load()
}

// Size returns the number of cached entries.
func (c *Cache) Size() int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return len(c.entries)
}
