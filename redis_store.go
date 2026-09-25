package main

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/redis/go-redis/v9"

	"fizzbuzz/internal/fizzbuzz"
)

// One hash tag keeps all three keys in the same Redis Cluster slot.
var redisKeys = []string{"{fizzbuzz}:stats:counts", "{fizzbuzz}:stats:used", "{fizzbuzz}:stats:saturated"}

// The script is atomic across replicas. It adds a new member only if the member
// fits the byte budget. If not, the store saturates permanently. It returns 0 if
// it counted the hit, 1 if the store was saturated, 2 if this call saturated it.
var recordScript = redis.NewScript(`
if redis.call('EXISTS', KEYS[3]) == 1 then return 1 end
if not redis.call('ZSCORE', KEYS[1], ARGV[1]) then
  if tonumber(redis.call('GET', KEYS[2]) or '0') + tonumber(ARGV[2]) > tonumber(ARGV[3]) then
    redis.call('SET', KEYS[3], '1')
    redis.call('DEL', KEYS[1], KEYS[2])
    return 2
  end
  redis.call('INCRBY', KEYS[2], ARGV[2])
end
redis.call('ZINCRBY', KEYS[1], 1, ARGV[1])
return 0
`)

// redisStore shares exact counts between replicas. Saturation continues until
// you delete the keys. A restart does not clear it.
type redisStore struct {
	rdb      *redis.Client
	maxBytes int64
}

func (s *redisStore) Record(ctx context.Context, p fizzbuzz.Params) (bool, error) {
	member, err := json.Marshal(paramsJSON(p))
	if err != nil {
		return false, err
	}
	res, err := recordScript.Run(ctx, s.rdb, redisKeys, member, statsKeyOverhead+len(member), s.maxBytes).Int()
	return res == 2, err
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
