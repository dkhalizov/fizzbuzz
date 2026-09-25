package main

import (
	"errors"
	"sync"
	"sync/atomic"

	"fizzbuzz/internal/fizzbuzz"
)

// Each loop counts in its own shard, so a request takes no shared lock and
// touches no shared cache line. /stats merges the shards. The counts stay
// exact: a request that the budget cannot hold saturates the store, and
// /stats then returns 503, as in the service.
//
// Difference to the service: the same request on two loops costs two
// entries of the budget.

var errSaturated = errors.New("statistics unavailable: too many distinct requests")

const entryCost = 200 // bytes charged for each entry, plus the strings

type statKey struct {
	int1, int2, limit int
	str1, str2        string
}

type shard struct {
	mu     sync.Mutex // guards m; the counts are atomic
	m      map[statKey]*atomic.Uint64
	budget *budget
}

type budget struct {
	used, max atomic.Int64
	saturated atomic.Bool
}

// cell returns the counter for p, or nil when the store is saturated. The
// loop keeps the pointer in its response cache, so a cache hit counts with
// one uncontended atomic add.
func (s *shard) cell(p fizzbuzz.Params) *atomic.Uint64 {
	if s.budget.saturated.Load() {
		return nil
	}
	k := statKey{p.Int1, p.Int2, p.Limit, p.Str1, p.Str2}
	s.mu.Lock()
	defer s.mu.Unlock()
	if c := s.m[k]; c != nil {
		return c
	}
	cost := int64(entryCost + len(p.Str1) + len(p.Str2))
	if s.budget.used.Add(cost) > s.budget.max.Load() {
		s.budget.saturated.Store(true)
		return nil
	}
	k.str1, k.str2 = cloneString(p.Str1), cloneString(p.Str2)
	c := new(atomic.Uint64)
	s.m[k] = c
	return c
}

func cloneString(s string) string { return string([]byte(s)) }

func top(shards []*shard) (statKey, uint64, error) {
	if shards[0].budget.saturated.Load() {
		return statKey{}, 0, errSaturated
	}
	sum := map[statKey]uint64{}
	for _, s := range shards {
		s.mu.Lock()
		for k, c := range s.m {
			sum[k] += c.Load()
		}
		s.mu.Unlock()
	}
	var best statKey
	var hits uint64
	for k, n := range sum {
		if n > hits {
			best, hits = k, n
		}
	}
	return best, hits, nil
}
