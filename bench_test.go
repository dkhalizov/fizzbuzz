package main

import (
	"fmt"
	"io"
	"log/slog"
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
