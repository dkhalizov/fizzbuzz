package main

import (
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
)

type metrics struct {
	requests *prometheus.CounterVec
	duration *prometheus.HistogramVec
}

func newMetrics(reg *prometheus.Registry) *metrics {
	m := &metrics{
		requests: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "http_requests_total",
			Help: "HTTP requests by route, method and status.",
		}, []string{"route", "method", "status"}),
		duration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "http_request_duration_seconds",
			Help:    "HTTP request duration by route and method.",
			Buckets: prometheus.DefBuckets,
		}, []string{"route", "method"}),
	}
	// A dedicated registry starts empty; goroutine and fd counts reveal held connections.
	reg.MustRegister(m.requests, m.duration,
		collectors.NewGoCollector(),
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}))
	return m
}

// observe logs and measures every request, including gin's 404 and 405.
// Labels stay bounded: the route template, never the raw path.
func observe(log *slog.Logger, m *metrics) gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()
		c.Next()
		elapsed := time.Since(start)

		status := c.Writer.Status()
		route := c.FullPath()
		if route == "" {
			route = "unmatched"
		}
		method := methodLabel(c.Request.Method)
		m.requests.WithLabelValues(route, method, strconv.Itoa(status)).Inc()
		m.duration.WithLabelValues(route, method).Observe(elapsed.Seconds())
		log.LogAttrs(c.Request.Context(), slog.LevelInfo, "request",
			slog.String("method", c.Request.Method),
			slog.String("path", c.Request.URL.Path),
			slog.Int("status", status),
			slog.Float64("duration_ms", float64(elapsed.Microseconds())/1000),
		)
	}
}

// methodLabel maps client-controlled methods onto a fixed label set.
func methodLabel(m string) string {
	switch m {
	case http.MethodGet, http.MethodHead, http.MethodPost, http.MethodPut, http.MethodPatch,
		http.MethodDelete, http.MethodOptions, http.MethodConnect, http.MethodTrace, "QUERY":
		return m
	}
	return "other"
}
