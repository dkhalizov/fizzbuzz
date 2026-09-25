package main

import (
	"log/slog"
	"net/http"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/prometheus/client_golang/prometheus"
)

func TestMetricLabels(t *testing.T) {
	s := newServer(testConfig(t), slog.New(slog.DiscardHandler), newCounter(1<<20))
	reg := prometheus.NewRegistry()
	h := s.handler(reg)
	do(h, http.MethodGet, classic)
	do(h, http.MethodGet, "/nope")
	do(h, "BREW", "/fizzbuzz")

	requests := func(route, method, status string) float64 {
		m, _ := reg.Gather()
		for _, f := range m {
			if f.GetName() != "http_requests_total" {
				continue
			}
			for _, mm := range f.GetMetric() {
				l := map[string]string{}
				for _, lp := range mm.GetLabel() {
					l[lp.GetName()] = lp.GetValue()
				}
				if l["route"] == route && l["method"] == method && l["status"] == status {
					return mm.GetCounter().GetValue()
				}
			}
		}
		return 0
	}
	for _, c := range []struct{ route, method, status string }{
		{"/fizzbuzz", "GET", "200"},
		{"unmatched", "GET", "404"},
		{"unmatched", "other", "405"}, // client-controlled method is not a label value
	} {
		if got := requests(c.route, c.method, c.status); got != 1 {
			t.Errorf("http_requests_total%v = %v, want 1", c, got)
		}
	}
}

// Each route of the server must have its own label. Otherwise its requests
// count as "unmatched".
func TestRouteLabels(t *testing.T) {
	s := newServer(testConfig(t), slog.New(slog.DiscardHandler), newCounter(1<<20))
	h := s.handler(prometheus.NewRegistry()).(*gin.Engine)
	for _, r := range h.Routes() {
		if i := labelIndex(routeLabels[:], r.Path); i == len(routeLabels)-1 {
			t.Errorf("route %s has no entry in routeLabels", r.Path)
		}
	}
}
