package main

import (
	"net/http"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
)

type metrics struct {
	requests *prometheus.CounterVec
	duration *prometheus.HistogramVec
	// The resolved series, filled on first use. WithLabelValues hashes the
	// label values and looks up a map on each call. Series appear in /metrics
	// only after their first request, as before.
	reqCache [len(routeLabels)][len(methodLabels)][len(statusLabels)]atomic.Pointer[prometheus.Counter]
	durCache [len(routeLabels)][len(methodLabels)]atomic.Pointer[prometheus.Observer]
}

// The fixed label sets. The last entry of each is the fallback. A client
// controls the method, so an unknown method is "other".
var (
	routeLabels  = [...]string{"/fizzbuzz", "/stats", "/healthz", "/metrics", "/", "/app.js", "/app.css", "unmatched"}
	methodLabels = [...]string{http.MethodGet, http.MethodHead, http.MethodPost, http.MethodPut, http.MethodPatch,
		http.MethodDelete, http.MethodOptions, http.MethodConnect, http.MethodTrace, "QUERY", "other"}
	statusLabels = [...]int{200, 400, 404, 405, 413, 415, 503, 0}
)

func labelIndex[T comparable](set []T, v T) int {
	for i, x := range set[:len(set)-1] {
		if x == v {
			return i
		}
	}
	return len(set) - 1
}

// methodLabel maps client-controlled methods onto a fixed label set.
func methodLabel(m string) string { return methodLabels[labelIndex(methodLabels[:], m)] }

// observe records one request. A status outside statusLabels skips the
// cache.
func (m *metrics) observe(route, method string, status int, elapsed time.Duration) {
	ri, mi, si := labelIndex(routeLabels[:], route), labelIndex(methodLabels[:], method), labelIndex(statusLabels[:], status)
	if statusLabels[si] != status {
		m.requests.WithLabelValues(routeLabels[ri], methodLabels[mi], strconv.Itoa(status)).Inc()
	} else {
		slot := &m.reqCache[ri][mi][si]
		c := slot.Load()
		if c == nil {
			v := m.requests.WithLabelValues(routeLabels[ri], methodLabels[mi], strconv.Itoa(status))
			c = &v
			slot.Store(c)
		}
		(*c).Inc()
	}
	slot := &m.durCache[ri][mi]
	o := slot.Load()
	if o == nil {
		v := m.duration.WithLabelValues(routeLabels[ri], methodLabels[mi])
		o = &v
		slot.Store(o)
	}
	(*o).Observe(elapsed.Seconds())
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
		// An empty or unknown route gets the label "unmatched".
		m.observe(c.FullPath(), c.Request.Method, status, elapsed)
		log.log(end, c.Request.Method, c.Request.URL.Path, status, elapsed)
	}
}
