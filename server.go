package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"fizzbuzz/internal/fizzbuzz"
)

type server struct {
	cfg        config
	log        *slog.Logger
	store      statsStore
	inflight   atomic.Int64 // response bytes being streamed
	lastErrLog atomic.Int64
}

func newServer(cfg config, log *slog.Logger, store statsStore) *server {
	return &server{cfg: cfg, log: log, store: store}
}

func (s *server) handler(reg *prometheus.Registry) http.Handler {
	gin.SetMode(gin.ReleaseMode)
	r := gin.New()
	r.HandleMethodNotAllowed = true
	r.Use(observe(s.log, newMetrics(reg)))
	r.NoRoute(func(c *gin.Context) { c.JSON(http.StatusNotFound, gin.H{"error": "not found"}) })
	r.NoMethod(func(c *gin.Context) { c.JSON(http.StatusMethodNotAllowed, gin.H{"error": "method not allowed"}) })
	r.GET("/fizzbuzz", acceptQuery, s.fizzbuzz)
	r.HEAD("/fizzbuzz", acceptQuery, s.fizzbuzz)
	r.Handle("QUERY", "/fizzbuzz", acceptQuery, s.query) // gin 1.13 adds r.QUERY
	r.GET("/stats", s.statistics)
	registerUI(r)
	r.GET("/healthz", func(c *gin.Context) { c.JSON(http.StatusOK, gin.H{"status": "ok"}) })
	r.GET("/metrics", gin.WrapH(promhttp.HandlerFor(reg, promhttp.HandlerOpts{})))
	return r
}

func (s *server) fizzbuzz(c *gin.Context) {
	p, err := parseParams(c.Request.URL.RawQuery)
	if err == nil {
		err = p.Validate(s.cfg.Limits)
	}
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	size := fizzbuzz.JSONSize(p)

	if c.Request.Method == http.MethodHead {
		setBodyHeaders(c, size)
		return
	}
	if s.inflight.Add(size) > s.cfg.MaxInflightBytes {
		s.inflight.Add(-size)
		c.Header("Retry-After", "1")
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "server busy: too many large responses in flight"})
		return
	}
	defer s.inflight.Add(-size)

	s.record(c.Request.Context(), p)
	setBodyHeaders(c, size)
	// A write error means that the client left. net/http then closes the connection.
	dw := &deadlineWriter{w: c.Writer, rc: http.NewResponseController(c.Writer), d: s.cfg.WriteChunkTimeout}
	_, _ = fizzbuzz.WriteJSON(dw, p)
}

// record does not fail the request. A store outage stops the statistics, not /fizzbuzz.
// ponytail: one synchronous Redis round trip for each request. If it shows in
// the latency, count locally and send the counts in batches.
func (s *server) record(ctx context.Context, p fizzbuzz.Params) {
	ctx, cancel := context.WithTimeout(ctx, s.cfg.RedisTimeout)
	defer cancel()
	saturatedNow, err := s.store.Record(ctx, p)
	if saturatedNow {
		s.log.Warn("statistics saturated; /stats returns 503", "max_bytes", s.cfg.StatsMaxBytes)
	}
	if err != nil {
		s.logStoreErr(err)
	}
}

// logStoreErr logs one time in 10 s or less often, so an outage does not flood the log.
func (s *server) logStoreErr(err error) {
	now, last := time.Now().UnixNano(), s.lastErrLog.Load()
	if now-last >= int64(10*time.Second) && s.lastErrLog.CompareAndSwap(last, now) {
		s.log.Warn("stats store error", "err", err)
	}
}

func setBodyHeaders(c *gin.Context, size int64) {
	c.Header("Content-Type", "application/json")
	c.Header("Content-Length", strconv.FormatInt(size, 10))
}

type paramsJSON struct {
	Int1  int    `json:"int1"`
	Int2  int    `json:"int2"`
	Limit int    `json:"limit"`
	Str1  string `json:"str1"`
	Str2  string `json:"str2"`
}

func (s *server) statistics(c *gin.Context) {
	ctx, cancel := context.WithTimeout(c.Request.Context(), s.cfg.RedisTimeout)
	defer cancel()
	p, hits, err := s.store.Top(ctx)
	if err != nil {
		if !errors.Is(err, errStatsUnavailable) {
			s.logStoreErr(err)
			err = errors.New("statistics unavailable: store error")
		}
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": err.Error()})
		return
	}
	resp := struct {
		Params *paramsJSON `json:"params"`
		Hits   uint64      `json:"hits"`
	}{Hits: hits}
	if hits > 0 {
		pj := paramsJSON(p)
		resp.Params = &pj
	}
	c.JSON(http.StatusOK, resp)
}

// parseParams requires each parameter exactly one time and ignores unknown
// parameters. Params.Validate checks the ranges.
func parseParams(rawQuery string) (fizzbuzz.Params, error) {
	var p fizzbuzz.Params
	q, err := url.ParseQuery(rawQuery)
	if err != nil {
		return p, &fizzbuzz.FieldError{Param: "query", Reason: "malformed percent-encoding"}
	}
	get := func(name string) (string, error) {
		switch vs := q[name]; len(vs) {
		case 0:
			return "", &fizzbuzz.FieldError{Param: name, Reason: "is required"}
		case 1:
			return vs[0], nil
		default:
			return "", &fizzbuzz.FieldError{Param: name, Reason: "must be given exactly once"}
		}
	}
	getInt := func(name string) (int, error) {
		v, err := get(name)
		if err != nil {
			return 0, err
		}
		n, err := strconv.ParseInt(v, 10, 64)
		switch {
		case errors.Is(err, strconv.ErrRange):
			return 0, &fizzbuzz.FieldError{Param: name, Reason: "is out of range"}
		case err != nil:
			return 0, &fizzbuzz.FieldError{Param: name, Reason: "must be a base-10 integer"}
		}
		return int(n), nil
	}
	if p.Int1, err = getInt("int1"); err != nil {
		return p, err
	}
	if p.Int2, err = getInt("int2"); err != nil {
		return p, err
	}
	if p.Limit, err = getInt("limit"); err != nil {
		return p, err
	}
	if p.Str1, err = get("str1"); err != nil {
		return p, err
	}
	p.Str2, err = get("str2")
	return p, err
}

// deadlineWriter disconnects clients that stop reading. net/http clears the
// deadline when the response is complete.
type deadlineWriter struct {
	w  http.ResponseWriter
	rc *http.ResponseController
	d  time.Duration
}

func (d *deadlineWriter) Write(b []byte) (int, error) {
	_ = d.rc.SetWriteDeadline(time.Now().Add(d.d)) // httptest does not support deadlines
	return d.w.Write(b)
}
