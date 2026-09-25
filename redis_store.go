package main

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"sync"

	"github.com/redis/go-redis/v9"

	"fizzbuzz/internal/fizzbuzz"
)

// The Redis budget counts bytes, not keys, because each string can have 1 KiB.
const statsKeyOverhead = 128 // bytes for one key, without its strings

func memoryCost(p fizzbuzz.Params) int64 {
	return statsKeyOverhead + int64(len(p.Str1)+len(p.Str2))
}

// One hash tag keeps all keys in the same Redis Cluster slot.
var redisKeys = []string{"{fizzbuzz}:stats:counts", "{fizzbuzz}:stats:used", "{fizzbuzz}:stats:saturated"}

// A replica keeps the sequence number of its last batch for this time. A
// retry that arrives later can count a batch twice. This occurs only if a
// replica cannot reach Redis for longer than this time after Redis applied the
// batch.
const batchSeqTTLSeconds = 24 * 60 * 60

// The script applies one batch from one replica atomically. KEYS[4] holds the
// last sequence number of the replica, so a retry of an applied batch has no
// effect. The script checks the byte budget before it writes, so a batch is
// applied completely or not at all. It returns 0 if the batch is applied now or
// was applied before, 1 if the store was saturated, and 2 if this batch
// saturated it. ARGV is the sequence number, the TTL, the budget, and then a
// member, its hits and its cost for each key.
var flushScript = redis.NewScript(`
if redis.call('EXISTS', KEYS[3]) == 1 then return 1 end
if tonumber(redis.call('GET', KEYS[4]) or '0') >= tonumber(ARGV[1]) then return 0 end
local used = tonumber(redis.call('GET', KEYS[2]) or '0')
for i = 4, #ARGV, 3 do
  if not redis.call('ZSCORE', KEYS[1], ARGV[i]) then used = used + tonumber(ARGV[i+2]) end
end
if used > tonumber(ARGV[3]) then
  redis.call('SET', KEYS[3], '1')
  redis.call('DEL', KEYS[1], KEYS[2])
  return 2
end
for i = 4, #ARGV, 3 do redis.call('ZINCRBY', KEYS[1], ARGV[i+1], ARGV[i]) end
redis.call('SET', KEYS[2], used)
redis.call('SET', KEYS[4], ARGV[1], 'EX', ARGV[2])
return 0
`)

var saturateScript = redis.NewScript(`
redis.call('SET', KEYS[3], '1')
redis.call('DEL', KEYS[1], KEYS[2])
return 0
`)

// redisStore shares exact counts between replicas. Record counts in local
// memory. Flush sends all local counts in one script call. Saturation
// continues until you delete the keys. A restart does not clear it.
type redisStore struct {
	rdb      *redis.Client
	maxBytes int64
	keys     []string

	flushMu sync.Mutex // one batch in flight at a time
	seq     int64
	batch   []any // script arguments of the batch that Redis did not acknowledge

	mu        sync.Mutex
	pending   map[fizzbuzz.Params]*pendingHits
	usedBytes int64
	overflow  bool
}

// pendingHits keeps a key until Redis acknowledges all its hits. Thus a key
// uses the local budget one time, also while a batch waits for a retry.
type pendingHits struct {
	n    uint64 // hits not acknowledged by Redis
	sent uint64 // part of n in the batch in flight
}

func newRedisStore(rdb *redis.Client, maxBytes int64) *redisStore {
	// A new ID for each process, because the sequence numbers start again at 1.
	seqKey := "{fizzbuzz}:stats:seq:" + rand.Text()
	return &redisStore{
		rdb:      rdb,
		maxBytes: maxBytes,
		keys:     append(append([]string{}, redisKeys...), seqKey),
		pending:  make(map[fizzbuzz.Params]*pendingHits),
	}
}

// Record uses the same byte budget as the memory store. A local cost is less
// than the cost of the same key in Redis, and each pending key becomes a
// member in Redis. Thus, if the pending keys exceed the budget, Redis
// certainly exceeds it too.
func (s *redisStore) Record(p fizzbuzz.Params) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.overflow {
		return false
	}
	h, ok := s.pending[p]
	if !ok {
		cost := memoryCost(p)
		if s.usedBytes+cost > s.maxBytes {
			s.overflow = true
			return true
		}
		s.usedBytes += cost
		h = &pendingHits{}
		s.pending[ownStrings(p)] = h
	}
	h.n++
	return false
}

// Flush retries the unacknowledged batch with the same sequence number before
// it starts a new batch. Thus Redis counts each batch exactly one time.
func (s *redisStore) Flush(ctx context.Context) (bool, error) {
	s.flushMu.Lock()
	defer s.flushMu.Unlock()

	s.mu.Lock()
	overflow := s.overflow
	s.mu.Unlock()
	if overflow {
		if err := saturateScript.Run(ctx, s.rdb, s.keys).Err(); err != nil {
			return false, err
		}
		s.mu.Lock()
		s.pending, s.usedBytes, s.overflow, s.batch = make(map[fizzbuzz.Params]*pendingHits), 0, false, nil
		s.mu.Unlock()
		return false, nil
	}

	if s.batch == nil {
		var err error
		if s.batch, err = s.nextBatch(); err != nil || s.batch == nil {
			return false, err
		}
	}
	res, err := flushScript.Run(ctx, s.rdb, s.keys, s.batch...).Int()
	if err != nil {
		return false, err
	}
	s.batch = nil
	s.mu.Lock()
	defer s.mu.Unlock()
	for p, h := range s.pending {
		h.n -= h.sent
		h.sent = 0
		if h.n == 0 {
			delete(s.pending, p)
			s.usedBytes -= memoryCost(p)
		}
	}
	return res == 2, nil
}

// nextBatch returns nil if no hits are pending.
func (s *redisStore) nextBatch() ([]any, error) {
	type hit struct {
		p fizzbuzz.Params
		n uint64
	}
	s.mu.Lock()
	hits := make([]hit, 0, len(s.pending))
	for p, h := range s.pending {
		h.sent = h.n
		hits = append(hits, hit{p, h.n})
	}
	s.mu.Unlock()
	if len(hits) == 0 {
		return nil, nil
	}
	s.seq++
	args := make([]any, 0, 3+3*len(hits))
	args = append(args, s.seq, batchSeqTTLSeconds, s.maxBytes)
	for _, h := range hits {
		member, err := json.Marshal(paramsJSON(h.p))
		if err != nil {
			return nil, err
		}
		args = append(args, member, h.n, statsKeyOverhead+len(member))
	}
	return args, nil
}

func (s *redisStore) Top(ctx context.Context) (fizzbuzz.Params, uint64, error) {
	pipe := s.rdb.Pipeline()
	saturated := pipe.Exists(ctx, redisKeys[2])
	top := pipe.ZRevRangeWithScores(ctx, redisKeys[0], 0, 0)
	if _, err := pipe.Exec(ctx); err != nil && !errors.Is(err, redis.Nil) {
		return fizzbuzz.Params{}, 0, err
	}
	if saturated.Val() == 1 {
		return fizzbuzz.Params{}, 0, errStatsUnavailable
	}
	if len(top.Val()) == 0 {
		return fizzbuzz.Params{}, 0, nil
	}
	var pj paramsJSON
	if err := json.Unmarshal([]byte(top.Val()[0].Member.(string)), &pj); err != nil {
		return fizzbuzz.Params{}, 0, err
	}
	return fizzbuzz.Params(pj), uint64(top.Val()[0].Score), nil
}
