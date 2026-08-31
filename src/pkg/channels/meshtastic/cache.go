// SPDX-License-Identifier: GPL-3.0-only

package meshtastic

import (
	"sync"
	"time"
)

type inboundKey struct{ from, id uint32 }
type sentKey struct{ id, channel uint32 }

type cacheEntry[K comparable] struct {
	key K
	at  time.Time
	gen uint64
}

type boundedCache[K comparable] struct {
	mu       sync.Mutex
	entries  map[K]cacheEntry[K]
	ring     []cacheEntry[K]
	next     int
	capacity int
	ttl      time.Duration
	gen      uint64
}

func newBoundedCache[K comparable](capacity int, ttl time.Duration) *boundedCache[K] {
	return &boundedCache[K]{
		entries:  make(map[K]cacheEntry[K], capacity),
		ring:     make([]cacheEntry[K], capacity),
		capacity: capacity,
		ttl:      ttl,
	}
}

func (c *boundedCache[K]) contains(key K, now time.Time) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.entries[key]
	if ok && now.Sub(e.at) >= c.ttl {
		delete(c.entries, key)
		return false
	}
	return ok
}

func (c *boundedCache[K]) add(key K, now time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if e, ok := c.entries[key]; ok && now.Sub(e.at) < c.ttl {
		return
	}
	c.gen++
	e := cacheEntry[K]{key: key, at: now, gen: c.gen}
	old := c.ring[c.next]
	if old.gen != 0 {
		if live, ok := c.entries[old.key]; ok && live.gen == old.gen {
			delete(c.entries, old.key)
		}
	}
	c.ring[c.next] = e
	c.next = (c.next + 1) % c.capacity
	c.entries[key] = e
}

func (c *boundedCache[K]) seenOrAdd(key K, now time.Time) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if e, ok := c.entries[key]; ok && now.Sub(e.at) < c.ttl {
		return true
	}
	c.gen++
	e := cacheEntry[K]{key: key, at: now, gen: c.gen}
	old := c.ring[c.next]
	if old.gen != 0 {
		if live, ok := c.entries[old.key]; ok && live.gen == old.gen {
			delete(c.entries, old.key)
		}
	}
	c.ring[c.next] = e
	c.next = (c.next + 1) % c.capacity
	c.entries[key] = e
	return false
}

func (c *boundedCache[K]) len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.entries)
}

func (c *boundedCache[K]) any(match func(K) bool, now time.Time) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	for k, e := range c.entries {
		if now.Sub(e.at) >= c.ttl {
			delete(c.entries, k)
			continue
		}
		if match(k) {
			return true
		}
	}
	return false
}
