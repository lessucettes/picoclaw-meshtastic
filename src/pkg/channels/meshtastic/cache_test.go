// SPDX-License-Identifier: GPL-3.0-only

package meshtastic

import (
	"sync"
	"testing"
	"time"
)

func TestBoundedCacheTTLAndEviction(t *testing.T) {
	now := time.Unix(100, 0)
	c := newBoundedCache[int](2, time.Minute)
	if c.seenOrAdd(1, now) || !c.seenOrAdd(1, now) {
		t.Fatal("seen-or-add result mismatch")
	}
	c.add(2, now)
	c.add(3, now)
	if c.len() != 2 || c.contains(1, now) || !c.contains(2, now) || !c.contains(3, now) {
		t.Fatalf("bad eviction state: len=%d", c.len())
	}
	if c.contains(2, now.Add(time.Minute)) {
		t.Fatal("TTL boundary did not expire entry")
	}
}

func TestBoundedCacheConcurrent(t *testing.T) {
	c := newBoundedCache[int](32, time.Minute)
	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func(base int) {
			defer wg.Done()
			for i := 0; i < 1000; i++ {
				c.seenOrAdd(base+i, time.Now())
				c.contains(base+i, time.Now())
			}
		}(g * 1000)
	}
	wg.Wait()
	if c.len() > 32 {
		t.Fatalf("cache grew to %d", c.len())
	}
}
