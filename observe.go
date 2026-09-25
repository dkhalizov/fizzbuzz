package main

import (
	"math/rand/v2"
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
	// A new registry is empty. The goroutine and file descriptor counts show
	// connections that stay open.
	reg.MustRegister(m.requests, m.duration,
		collectors.NewGoCollector(),
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}))
	return m
}

// observe logs and measures each request, also the 404 and 405 of gin. The
// route label is the route template, not the raw path, so the labels stay bounded.
func observe(log *requestLog, m *metrics) gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()
		c.Next()
		end := time.Now()
		elapsed := end.Sub(start)

		status := c.Writer.Status()
		route := c.FullPath()
		if route == "" {
			route = "unmatched"
		}
		method := methodLabel(c.Request.Method)
		m.requests.WithLabelValues(route, method, strconv.Itoa(status)).Inc()
		m.duration.WithLabelValues(route, method).Observe(elapsed.Seconds())
		// Sampling: every error, and 1 in 100 other requests.
		if status >= 400 || rand.Uint32N(100) == 0 {
			log.log(end, c.Request.Method, c.Request.URL.Path, status, elapsed)
		}
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
