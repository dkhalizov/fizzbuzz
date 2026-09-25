package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/redis/go-redis/v9"
)

func newTestRedisStore(t *testing.T, maxBytes int64) (*redisStore, *miniredis.Miniredis) {
	t.Helper()
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr(), MaxRetries: -1})
	t.Cleanup(func() { _ = rdb.Close() })
	return newRedisStore(rdb, maxBytes), mr
}

// callCounter counts the commands that the client sends, but not the
// commands that open a connection.
type callCounter struct{ n int }

func (c *callCounter) DialHook(next redis.DialHook) redis.DialHook { return next }
func (c *callCounter) ProcessHook(next redis.ProcessHook) redis.ProcessHook {
	return func(ctx context.Context, cmd redis.Cmder) error {
		if cmd.Name() != "hello" && cmd.Name() != "client" {
			c.n++
		}
		return next(ctx, cmd)
	}
}
func (c *callCounter) ProcessPipelineHook(next redis.ProcessPipelineHook) redis.ProcessPipelineHook {
	return next
}

func flushCtx(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	t.Cleanup(cancel)
	return ctx
}

func wantTop(t *testing.T, s statsStore, want uint64) {
	t.Helper()
	p, hits, err := s.Top(flushCtx(t))
	if err != nil || p != keyA || hits != want {
		t.Fatalf("top = %+v with %d hits (err %v), want A with %d", p, hits, err, want)
	}
}

func TestRedisBatch(t *testing.T) {
	s, _ := newTestRedisStore(t, 1<<20)
	calls := &callCounter{}
	s.rdb.AddHook(calls)
	if _, err := s.Flush(flushCtx(t)); err != nil || calls.n != 0 {
		t.Fatalf("empty flush: err %v, %d commands", err, calls.n)
	}
	for range 1000 {
		s.Record(keyA)
	}
	s.Record(keyB)
	if calls.n != 0 {
		t.Fatalf("Record sent %d commands", calls.n)
	}
	if _, err := s.Flush(flushCtx(t)); err != nil {
		t.Fatal(err)
	}
	if calls.n > 2 { // EVALSHA, then EVAL after NOSCRIPT
		t.Fatalf("flush sent %d commands, want one script call", calls.n)
	}
	if len(s.pending) != 0 || s.usedBytes != 0 {
		t.Fatalf("after flush: %d pending keys, %d bytes", len(s.pending), s.usedBytes)
	}
	wantTop(t, s, 1000)
}

// The reply of a script call can get lost after Redis applied it. The retry
// must not count the batch again.
func TestRedisRetryCountsOnce(t *testing.T) {
	s, _ := newTestRedisStore(t, 1<<20)
	for range 3 {
		s.Record(keyA)
	}
	batch, err := s.nextBatch()
	if err != nil {
		t.Fatal(err)
	}
	s.batch = batch
	if err := flushScript.Run(flushCtx(t), s.rdb, s.keys, batch...).Err(); err != nil {
		t.Fatal(err)
	}
	s.Record(keyA) // arrives while the batch is in flight
	s.Record(keyA)

	if _, err := s.Flush(flushCtx(t)); err != nil {
		t.Fatal(err)
	}
	wantTop(t, s, 3)
	if _, err := s.Flush(flushCtx(t)); err != nil {
		t.Fatal(err)
	}
	wantTop(t, s, 5)
}

func TestRedisOutage(t *testing.T) {
	s, mr := newTestRedisStore(t, 1<<20)
	s.Record(keyA)
	if _, err := s.Flush(flushCtx(t)); err != nil {
		t.Fatal(err)
	}
	mr.Close()
	for range 4 {
		s.Record(keyA)
		if _, err := s.Flush(flushCtx(t)); err == nil {
			t.Fatal("flush succeeded without Redis")
		}
	}
	if err := mr.Restart(); err != nil {
		t.Fatal(err)
	}
	for range 2 { // the batch from the outage, then the hits after it
		if _, err := s.Flush(flushCtx(t)); err != nil {
			t.Fatal(err)
		}
	}
	wantTop(t, s, 5)
}

// Pending keys that exceed the budget saturate the shared store, also after an
// outage, because Redis would exceed the budget with the same keys.
func TestRedisLocalOverflow(t *testing.T) {
	s, mr := newTestRedisStore(t, 200) // one key fits, two do not
	mr.Close()
	if s.Record(keyA) {
		t.Fatal("first key reported saturation")
	}
	if !s.Record(keyB) {
		t.Fatal("second key did not report saturation")
	}
	if s.Record(keyA) {
		t.Fatal("saturation reported twice")
	}
	if err := mr.Restart(); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Flush(flushCtx(t)); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.Top(flushCtx(t)); !errors.Is(err, errStatsUnavailable) {
		t.Fatalf("Top err = %v, want errStatsUnavailable", err)
	}
}

// Each replica has its own sequence numbers. Batches with the same number from
// two replicas must both count.
func TestRedisReplicas(t *testing.T) {
	a, mr := newTestRedisStore(t, 1<<20)
	b := newRedisStore(redis.NewClient(&redis.Options{Addr: mr.Addr()}), 1<<20)
	for _, s := range []*redisStore{a, b, a} {
		s.Record(keyA)
		if _, err := s.Flush(flushCtx(t)); err != nil {
			t.Fatal(err)
		}
	}
	wantTop(t, b, 3)
}

// Meaningful only under -race.
func TestRedisConcurrent(t *testing.T) {
	s, _ := newTestRedisStore(t, 1<<20)
	var wg sync.WaitGroup
	for range 20 {
		wg.Go(func() {
			for range 500 {
				s.Record(keyA)
			}
		})
	}
	wg.Go(func() {
		for range 20 {
			_, _ = s.Flush(context.Background())
		}
	})
	wg.Wait()
	if _, err := s.Flush(flushCtx(t)); err != nil {
		t.Fatal(err)
	}
	wantTop(t, s, 10_000)
}

func TestServerRedis(t *testing.T) {
	s, _ := newTestRedisStore(t, 1<<20)
	srv := newServer(testConfig(t), slog.New(slog.DiscardHandler), s)
	h := srv.handler(prometheus.NewRegistry())

	do(h, http.MethodGet, classic)
	want := `{"params":{"int1":3,"int2":5,"limit":15,"str1":"fizz","str2":"buzz"},"hits":1}`
	if got := strings.TrimSpace(do(h, http.MethodGet, "/stats").Body.String()); got != want {
		t.Fatalf("/stats does not include the hits of this replica: %s", got)
	}

	ctx, stop := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { srv.flushLoop(ctx); close(done) }()
	do(h, http.MethodGet, classic)
	stop()
	<-done
	wantTop(t, s, 2)
}
