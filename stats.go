package main

import (
	"context"
	"errors"
	"sync"

	"fizzbuzz/internal/fizzbuzz"
)

// The budget counts bytes, not keys, because each string can have 1 KiB.
const statsKeyOverhead = 128 // Params struct, count and map slot

var errStatsUnavailable = errors.New("statistics unavailable: too many distinct requests")

// statsStore backs /stats. An implementation counts exactly or returns
// errStatsUnavailable. It never reports a winner that can be wrong. Record
// tells if this call saturated the store, so the server logs it one time.
type statsStore interface {
	Record(ctx context.Context, p fizzbuzz.Params) (saturatedNow bool, err error)
	Top(ctx context.Context) (p fizzbuzz.Params, hits uint64, err error)
}

// counter is the in-memory statsStore, one for each process. When a new key
// would exceed the memory budget, the counter saturates. Top then fails until
// restart, because an exact winner is no longer known.
type counter struct {
	mu        sync.Mutex
	counts    map[fizzbuzz.Params]uint64
	top       fizzbuzz.Params
	topHits   uint64
	usedBytes int64
	maxBytes  int64
	saturated bool
}

func newCounter(maxBytes int64) *counter {
	return &counter{counts: make(map[fizzbuzz.Params]uint64), maxBytes: maxBytes}
}

func (c *counter) Record(_ context.Context, p fizzbuzz.Params) (bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.saturated {
		return false, nil
	}
	n, ok := c.counts[p]
	if !ok {
		cost := statsKeyOverhead + int64(len(p.Str1)+len(p.Str2))
		if c.usedBytes+cost > c.maxBytes {
			c.saturated, c.counts = true, nil
			return true, nil
		}
		c.usedBytes += cost
	}
	n++
	c.counts[p] = n
	// Counts only increase, so only the key of this call can take the lead.
	// On a tie, the current leader stays.
	if n > c.topHits {
		c.top, c.topHits = p, n
	}
	return false, nil
}

func (c *counter) Top(context.Context) (fizzbuzz.Params, uint64, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.saturated {
		return fizzbuzz.Params{}, 0, errStatsUnavailable
	}
	return c.top, c.topHits, nil
}
