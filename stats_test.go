package main

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"

	"fizzbuzz/internal/fizzbuzz"
)

var (
	keyA = fizzbuzz.Params{Int1: 3, Int2: 5, Limit: 15, Str1: "fizz", Str2: "buzz"}
	keyB = fizzbuzz.Params{Int1: 5, Int2: 3, Limit: 15, Str1: "buzz", Str2: "fizz"}
)

// stores runs the same contract against every statsStore implementation.
func stores(t *testing.T, maxBytes int64) map[string]statsStore {
	mr := miniredis.RunT(t)
	return map[string]statsStore{
		"memory": newCounter(maxBytes),
		"redis":  newRedisStore(redis.NewClient(&redis.Options{Addr: mr.Addr()}), maxBytes),
	}
}

// record records one hit and flushes it, so every store reports saturation
// from the same call.
func record(t *testing.T, s statsStore, p fizzbuzz.Params) (saturatedNow bool) {
	t.Helper()
	sat := s.Record(p)
	flushSat, err := s.Flush(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return sat || flushSat
}

func TestStoreContract(t *testing.T) {
	ctx := context.Background()
	odd := fizzbuzz.Params{Int1: 7, Int2: 7, Limit: 1, Str1: `"<é>"`, Str2: strings.Repeat("🙂", 10)}
	for name, s := range stores(t, 1<<20) {
		t.Run(name, func(t *testing.T) {
			if _, hits, err := s.Top(ctx); err != nil || hits != 0 {
				t.Fatalf("empty: hits=%d err=%v", hits, err)
			}
			for _, p := range []fizzbuzz.Params{keyA, odd, keyA, odd, odd} {
				record(t, s, p)
			}
			if p, hits, err := s.Top(ctx); err != nil || p != odd || hits != 3 {
				t.Fatalf("top = %+v with %d hits (err %v), want odd with 3", p, hits, err)
			}
		})
	}
}

func TestStoreSaturation(t *testing.T) {
	ctx := context.Background()
	for name, s := range stores(t, 200) { // one key fits, two do not
		t.Run(name, func(t *testing.T) {
			if record(t, s, keyA) {
				t.Fatal("first key reported saturation")
			}
			if !record(t, s, keyB) {
				t.Fatal("second key did not report saturation")
			}
			if record(t, s, keyA) {
				t.Fatal("saturation reported twice")
			}
			if _, _, err := s.Top(ctx); !errors.Is(err, errStatsUnavailable) {
				t.Fatalf("Top err = %v, want errStatsUnavailable", err)
			}
		})
	}
}

// Pins the memory implementation: on a tie the current leader stays.
func TestCounterTie(t *testing.T) {
	c := newCounter(1 << 20)
	for _, k := range []fizzbuzz.Params{keyA, keyB, keyB, keyA} {
		c.Record(k)
	}
	if p, hits, _ := c.Top(context.Background()); p != keyB || hits != 2 {
		t.Fatalf("top = %+v with %d hits, want B with 2", p, hits)
	}
}

// Meaningful only under -race.
func TestCounterConcurrent(t *testing.T) {
	ctx := context.Background()
	c := newCounter(1 << 20)
	var wg sync.WaitGroup
	for g := range 50 {
		wg.Go(func() {
			for i := range 1000 {
				k := keyA
				if (g+i)%2 == 1 {
					k = keyB
				}
				c.Record(k)
				_, _, _ = c.Top(ctx)
			}
		})
	}
	wg.Wait()
	if _, hits, _ := c.Top(ctx); hits != 25_000 {
		t.Fatalf("top hits = %d, want 25000", hits)
	}
}
