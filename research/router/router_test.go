// Package router compares the cost of routing alone: gin, http.ServeMux and a
// switch on the path. Each serves the routes of the service with an empty
// handler, so the benchmark measures dispatch, not work.
package router

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

var paths = []string{"/fizzbuzz", "/stats", "/healthz", "/metrics", "/", "/app.js", "/app.css"}

type nopWriter struct{ h http.Header }

func (w *nopWriter) Header() http.Header         { return w.h }
func (w *nopWriter) Write(b []byte) (int, error) { return len(b), nil }
func (w *nopWriter) WriteHeader(int)             {}

func ginRouter() http.Handler {
	gin.SetMode(gin.ReleaseMode)
	r := gin.New()
	r.HandleMethodNotAllowed = true
	r.Use(func(c *gin.Context) { c.Next(); _ = c.FullPath() })
	for _, p := range paths {
		r.GET(p, func(*gin.Context) {})
	}
	r.HEAD("/fizzbuzz", func(*gin.Context) {})
	r.Handle("QUERY", "/fizzbuzz", func(*gin.Context) {})
	return r
}

func muxRouter() http.Handler {
	m := http.NewServeMux()
	nop := func(http.ResponseWriter, *http.Request) {}
	for _, p := range paths {
		if p == "/" {
			m.HandleFunc("GET /{$}", nop)
			continue
		}
		m.HandleFunc("GET "+p, nop) // GET also matches HEAD
	}
	m.HandleFunc("QUERY /fizzbuzz", nop)
	return m
}

type switchRouter struct{}

func (switchRouter) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch r.URL.Path {
	case "/fizzbuzz":
		switch r.Method {
		case http.MethodGet, http.MethodHead, "QUERY":
		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	case "/stats", "/healthz", "/metrics", "/", "/app.js", "/app.css":
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func BenchmarkRoute(b *testing.B) {
	for _, rt := range []struct {
		name string
		h    http.Handler
	}{{"gin", ginRouter()}, {"servemux", muxRouter()}, {"switch", switchRouter{}}} {
		for _, target := range []string{"/fizzbuzz?int1=3&int2=5&limit=100&str1=fizz&str2=buzz", "/nope"} {
			r := httptest.NewRequest(http.MethodGet, target, nil)
			name := rt.name + "/hit"
			if target == "/nope" {
				name = rt.name + "/404"
			}
			b.Run(name, func(b *testing.B) {
				w := &nopWriter{h: http.Header{}}
				b.ReportAllocs()
				for b.Loop() {
					rt.h.ServeHTTP(w, r)
				}
			})
		}
	}
}
