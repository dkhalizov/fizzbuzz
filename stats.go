package main

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"

	"fizzbuzz/internal/fizzbuzz"
)

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

// The budget charges the heap bytes of one map entry when the Go map has its
// lowest load, measured and rounded up. TestCounterBudget fails if the real
// memory exceeds the charge.
const (
	prefixCost = 152 // map[prefix]uint32 entry, without the strings
	countCost  = 48  // map[uint64]uint64 entry
)

// Requests that differ only in limit share one prefix. Each different limit
// then costs one count.
type prefix struct {
	int1, int2 int
	str1, str2 string
}

// counter is the in-memory statsStore, one for each process. When a new key
// would exceed the memory budget, the counter saturates. Top then fails until
// restart, because an exact winner is no longer known.
type counter struct {
	mu        sync.Mutex
	prefixes  map[prefix]uint32
	counts    map[uint64]uint64 // prefix ID << 32 | limit
	top       fizzbuzz.Params
	topKey    uint64 // 0 before the first hit: limit is at least 1
	topHits   uint64
	usedBytes int64
	maxBytes  int64
	saturated bool
}

func newCounter(maxBytes int64) *counter {
	return &counter{prefixes: make(map[prefix]uint32), counts: make(map[uint64]uint64), maxBytes: maxBytes}
}

func (c *counter) Record(p fizzbuzz.Params) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.saturated {
		return false
	}
	pk := prefix{p.Int1, p.Int2, p.Str1, p.Str2}
	id, known := c.prefixes[pk]
	if !known {
		id = uint32(len(c.prefixes))
	}
	key := uint64(id)<<32 | uint64(p.Limit)
	n, counted := c.counts[key]
	if !counted {
		cost := int64(countCost)
		if !known {
			cost += prefixCost + allocSize(len(p.Str1)) + allocSize(len(p.Str2))
		}
		if c.usedBytes+cost > c.maxBytes {
			c.saturated, c.prefixes, c.counts = true, nil, nil
			return true
		}
		c.usedBytes += cost
		if !known {
			pk.str1, pk.str2 = strings.Clone(p.Str1), strings.Clone(p.Str2)
			c.prefixes[pk] = id
		}
	}
	n++
	c.counts[key] = n
	// Counts only increase, so only the key of this call can take the lead.
	// On a tie, the current leader stays.
	if n > c.topHits {
		if key != c.topKey {
			c.top, c.topKey = ownStrings(p), key
		}
		c.topHits = n
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

// ownStrings copies the strings of p. A parsed string can share the memory of
// the whole query string or body, which can have MAX_HEADER_BYTES.
func ownStrings(p fizzbuzz.Params) fizzbuzz.Params {
	p.Str1, p.Str2 = strings.Clone(p.Str1), strings.Clone(p.Str2)
	return p
}

// allocSize is the heap size of an n-byte allocation: Go rounds it up to a
// size class.
func allocSize(n int) int64 { return int64(cap(slices.Grow([]byte(nil), n))) }
