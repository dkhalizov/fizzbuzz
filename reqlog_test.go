package main

import (
	"bytes"
	"context"
	"log/slog"
	"sync"
	"testing"
	"time"
)

func slogLine(t time.Time, method, path string, status int, elapsed time.Duration) []byte {
	var buf bytes.Buffer
	h := slog.NewJSONHandler(&buf, nil)
	r := slog.NewRecord(t, slog.LevelInfo, "request", 0)
	r.AddAttrs(
		slog.String("method", method),
		slog.String("path", path),
		slog.Int("status", status),
		slog.Float64("duration_ms", float64(elapsed.Microseconds())/1000),
	)
	_ = h.Handle(context.Background(), r)
	return buf.Bytes()
}

func TestRequestLogMatchesSlog(t *testing.T) {
	t0 := time.Date(2026, 9, 25, 18, 47, 31, 605720646, time.UTC)
	for _, c := range []struct {
		path string
		d    time.Duration
	}{
		{"/fizzbuzz", 4 * time.Microsecond},
		{"/", 0},
		{"/stats", 12500 * time.Microsecond},
		{"/x\"<>&\\\x00\x1f\x7f\n\r\té\xff\u2028\u2029", 999999 * time.Microsecond},
		{"/fizzbuzz", 3 * time.Hour},
		{"/fizzbuzz", 1000 * time.Microsecond},
		{"/fizzbuzz", 1234567 * time.Nanosecond},
	} {
		got := appendRequestLine(nil, t0, "GET", c.path, 200, c.d)
		if want := slogLine(t0, "GET", c.path, 200, c.d); !bytes.Equal(got, want) {
			t.Errorf("got  %s\nwant %s", got, want)
		}
	}
}

func FuzzRequestLog(f *testing.F) {
	f.Add("GET", "/fizzbuzz", 200, int64(4000), int64(1))
	f.Add("QUERY", "/x\xff\u2028", 404, int64(-5), int64(1<<40))
	f.Fuzz(func(t *testing.T, method, path string, status int, d, unix int64) {
		t0 := time.Unix(0, unix%(1<<62)).UTC()
		if y := t0.Year(); y < 0 || y >= 10000 {
			return
		}
		d %= int64(1000 * time.Hour) // under 10^15 microseconds
		got := appendRequestLine(nil, t0, method, path, status, time.Duration(d))
		if want := slogLine(t0, method, path, status, time.Duration(d)); !bytes.Equal(got, want) {
			t.Fatalf("got  %q\nwant %q", got, want)
		}
	})
}

func TestRequestLogFlushesAtShutdown(t *testing.T) {
	var out lockedBuffer
	l := newRequestLog(&out)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { l.run(ctx); close(done) }()
	const n = 5000 // more than logFlushBytes, so the kick path runs too
	for range n {
		l.log(time.Unix(0, 0).UTC(), "GET", "/fizzbuzz", 200, time.Millisecond)
	}
	cancel()
	<-done
	if got := bytes.Count(out.Bytes(), []byte("\n")); got != n {
		t.Fatalf("%d lines, want %d", got, n)
	}
}

type lockedBuffer struct {
	mu sync.Mutex
	bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.Buffer.Write(p)
}
