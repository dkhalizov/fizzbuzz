package main

import (
	"bytes"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

type discardWriter struct{ h http.Header }

func (d *discardWriter) Header() http.Header         { return d.h }
func (d *discardWriter) Write(b []byte) (int, error) { return len(b), nil }
func (d *discardWriter) WriteHeader(int)             {}

// SetWriteDeadline makes the benchmark take the same path as a real
// connection. Without it, each write builds an ErrNotSupported error.
func (d *discardWriter) SetWriteDeadline(time.Time) error { return nil }

// BenchmarkServe drives the whole request path in-process: parsing,
// validation, stats, metrics, JSON logging and generation.
func BenchmarkServe(b *testing.B) {
	cfg, _ := loadConfig(func(string) string { return "" })
	h := newServer(cfg, slog.New(slog.NewJSONHandler(io.Discard, nil)), newCounter(cfg.StatsMaxBytes)).handler(prometheus.NewRegistry())
	for _, n := range []int{100, 10_000, 1_000_000} {
		r := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/fizzbuzz?int1=3&int2=5&limit=%d&str1=fizz&str2=buzz", n), nil)
		b.Run(fmt.Sprintf("limit=%d", n), func(b *testing.B) {
			w := &discardWriter{h: http.Header{}}
			b.ReportAllocs()
			for b.Loop() {
				h.ServeHTTP(w, r)
			}
		})
	}
}

// BenchmarkServeLoopback sends keep-alive requests over a real loopback
// connection, so it includes net/http: the request parsing, the header
// writing and the syscalls. The client writes fixed bytes and reads into a
// fixed buffer, so it allocates nothing. The response has a known length.
func BenchmarkServeLoopback(b *testing.B) {
	cfg, _ := loadConfig(func(string) string { return "" })
	h := newServer(cfg, slog.New(slog.DiscardHandler), newCounter(cfg.StatsMaxBytes)).handler(prometheus.NewRegistry())
	srv := httptest.NewServer(h)
	defer srv.Close()
	conn, err := net.Dial("tcp", srv.Listener.Addr().String())
	if err != nil {
		b.Fatal(err)
	}
	defer conn.Close()
	req := []byte("GET /fizzbuzz?int1=3&int2=5&limit=100&str1=fizz&str2=buzz HTTP/1.1\r\nHost: x\r\n\r\n")
	buf := make([]byte, 4096)
	// The first response gives the length of each response.
	if _, err := conn.Write(req); err != nil {
		b.Fatal(err)
	}
	n, err := conn.Read(buf)
	if err != nil || !bytes.Contains(buf[:n], []byte(`"buzz"]`)) {
		b.Fatalf("first response: %v %q", err, buf[:n])
	}
	size := n
	b.ReportAllocs()
	for b.Loop() {
		if _, err := conn.Write(req); err != nil {
			b.Fatal(err)
		}
		// The Date header can change length only at a year boundary.
		if _, err := io.ReadFull(conn, buf[:size]); err != nil {
			b.Fatal(err)
		}
	}
}
