package main

import (
	"context"
	"errors"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"unsafe"

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
	for name, s := range stores(t, 300) { // one key fits, two do not
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

func TestCounterSharesPrefix(t *testing.T) {
	c := newCounter(1 << 20)
	for limit := 1; limit <= 100; limit++ {
		c.Record(fizzbuzz.Params{Int1: 3, Int2: 5, Limit: limit, Str1: "fizz", Str2: "buzz"})
	}
	c.Record(keyB)
	want := 2*(prefixCost+2*allocSize(4)) + 101*countCost
	if len(c.prefixes) != 2 || len(c.counts) != 101 || c.usedBytes != want {
		t.Fatalf("%d prefixes, %d counts, %d bytes; want 2, 101, %d", len(c.prefixes), len(c.counts), c.usedBytes, want)
	}
}

// TestCounterBudget guards the README claim that the memory store uses
// STATS_MAX_BYTES or less. It compares the real heap growth with the charge at
// many fill levels, because the bytes of a Go map entry change as the map
// grows. The keys come from query strings with a large unknown parameter, so a
// store that keeps the parsed strings also keeps the whole query.
func TestCounterBudget(t *testing.T) {
	junk := strings.Repeat("j", 5_000)
	cases := []struct {
		name  string
		query func(i int) string
	}{
		{"one prefix", func(i int) string {
			return "int1=3&int2=5&limit=" + strconv.Itoa(i+1) + "&str1=fizz&str2=buzz&x=" + junk
		}},
		{"new prefixes, short strings", func(i int) string {
			return "int1=3&int2=5&limit=15&str1=s" + strconv.Itoa(i) + "&str2=t" + strconv.Itoa(i) + "&x=" + junk
		}},
		{"new prefixes, 769-byte strings", func(i int) string {
			s := strconv.Itoa(i)
			s = strings.Repeat("0", 769-len(s)) + s // 769 bytes use an 896-byte size class
			return "int1=3&int2=5&limit=15&str1=" + s + "&str2=" + s + "&x=" + junk
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			for n := 1; n < 20_000; n = n*5/4 + 1 {
				runtime.GC()
				runtime.GC() // the second GC also frees sync.Pool caches
				var before, after runtime.MemStats
				runtime.ReadMemStats(&before)
				c := newCounter(1 << 40)
				for i := range n {
					p, err := parseParams(tc.query(i))
					if err != nil {
						t.Fatal(err)
					}
					c.Record(p)
				}
				runtime.GC()
				runtime.GC()
				runtime.ReadMemStats(&after)
				const fixed = 16 << 10 // the counter, its empty maps and runtime noise
				if real := int64(after.HeapAlloc) - int64(before.HeapAlloc); real > c.usedBytes+fixed {
					t.Fatalf("%d keys: %d bytes on the heap, %d charged", n, real, c.usedBytes)
				}
				runtime.KeepAlive(c)
			}
		})
	}
}

func TestStoresCopyStrings(t *testing.T) {
	p, err := parseParams("int1=3&int2=5&limit=15&str1=fizz&str2=buzz&x=" + strings.Repeat("j", 5_000))
	if err != nil {
		t.Fatal(err)
	}
	shares := func(a, b string) bool { return unsafe.StringData(a) == unsafe.StringData(b) }

	c := newCounter(1 << 20)
	c.Record(p)
	for pk := range c.prefixes {
		if shares(pk.str1, p.Str1) || shares(pk.str2, p.Str2) {
			t.Error("the counter keeps the parsed strings")
		}
	}
	if top, _, _ := c.Top(context.Background()); shares(top.Str1, p.Str1) || shares(top.Str2, p.Str2) {
		t.Error("the counter keeps the parsed strings of the leader")
	}

	r, _ := newTestRedisStore(t, 1<<20)
	r.Record(p)
	for k := range r.pending {
		if shares(k.Str1, p.Str1) || shares(k.Str2, p.Str2) {
			t.Error("the Redis store keeps the parsed strings")
		}
	}
}
