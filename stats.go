package main

import (
	"context"
	"errors"
	"sync"

	"fizzbuzz/internal/fizzbuzz"
)

// The budget counts bytes, not keys, because each string can have 1 KiB.
const statsKeyOverhead = 128 // Params struct, count and map slot

func memoryCost(p fizzbuzz.Params) int64 {
	return statsKeyOverhead + int64(len(p.Str1)+len(p.Str2))
}

var errStatsUnavailable = errors.New("statistics unavailable: too many distinct requests")

// statsStore backs /stats. An implementation counts exactly or returns
// errStatsUnavailable. It never reports a winner that can be wrong. Record
// does not wait for the network, because it runs before each response.
// Record and Flush tell if the call saturated the store, so the server logs
// it one time.
type statsStore interface {
	Record(p fizzbuzz.Params) (saturatedNow bool)
	// Flush sends the local counts to the shared store. Top includes only
	// the counts that a Flush sent.
	Flush(ctx context.Context) (saturatedNow bool, err error)
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

func (c *counter) Record(p fizzbuzz.Params) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.saturated {
		return false
	}
	n, ok := c.counts[p]
	if !ok {
		cost := memoryCost(p)
		if c.usedBytes+cost > c.maxBytes {
			c.saturated, c.counts = true, nil
			return true
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
	return false
}

func (c *counter) Flush(context.Context) (bool, error) { return false, nil }

func (c *counter) Top(context.Context) (fizzbuzz.Params, uint64, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.saturated {
		return fizzbuzz.Params{}, 0, errStatsUnavailable
	}
	return c.top, c.topHits, nil
}
