package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
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
	r.Use(observe(newMetrics(reg)))
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
	var resp fizzbuzz.Response
	if err == nil {
		resp, err = p.Prepare(s.cfg.Limits)
	}
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	size := resp.Size

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

	if s.store.Record(p) {
		s.warnSaturated()
	}
	setBodyHeaders(c, size)
	// A write error means that the client left. net/http then closes the connection.
	dw := dwPool.Get().(*deadlineWriter)
	*dw = deadlineWriter{w: c.Writer, dl: findDeadliner(c.Writer), d: s.cfg.WriteChunkTimeout}
	_, _ = resp.WriteTo(dw)
	*dw = deadlineWriter{} // drop the references before the pool keeps it
	dwPool.Put(dw)
}

func (s *server) flush(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, s.cfg.RedisTimeout)
	defer cancel()
	saturatedNow, err := s.store.Flush(ctx)
	if saturatedNow {
		s.warnSaturated()
	}
	if err != nil {
		s.logStoreErr(err)
	}
	return err
}

// flushLoop flushes one last time when ctx ends, so a normal shutdown loses no
// hits.
func (s *server) flushLoop(ctx context.Context) {
	t := time.NewTicker(s.cfg.StatsFlushInterval)
	defer t.Stop()
	for {
		select {
		case <-t.C:
			_ = s.flush(ctx)
		case <-ctx.Done():
			_ = s.flush(context.WithoutCancel(ctx))
			return
		}
	}
}

// top flushes first, so a client sees its own hits on this replica.
func (s *server) top(ctx context.Context) (fizzbuzz.Params, uint64, error) {
	if err := s.flush(ctx); err != nil {
		return fizzbuzz.Params{}, 0, err
	}
	ctx, cancel := context.WithTimeout(ctx, s.cfg.RedisTimeout)
	defer cancel()
	p, hits, err := s.store.Top(ctx)
	if err != nil && !errors.Is(err, errStatsUnavailable) {
		s.logStoreErr(err)
	}
	return p, hits, err
}

func (s *server) warnSaturated() {
	s.log.Warn("statistics saturated; /stats returns 503", "max_bytes", s.cfg.StatsMaxBytes)
}

// logStoreErr logs one time in 10 s or less often, so an outage does not flood the log.
func (s *server) logStoreErr(err error) {
	now, last := time.Now().UnixNano(), s.lastErrLog.Load()
	if now-last >= int64(10*time.Second) && s.lastErrLog.CompareAndSwap(last, now) {
		s.log.Warn("stats store error", "err", err)
	}
}

// Shared header values. A direct map assignment skips the key
// canonicalization and the []string allocation of Header.Set. The capacity is
// 1, so an Add elsewhere copies the slice and does not change the shared one.
var (
	jsonContentType   = []string{"application/json"}[:1:1]
	acceptQueryHeader = []string{queryMediaType}[:1:1]
)

// autoLengthBytes is bufferBeforeChunkingSize of net/http. When a handler
// returns and its whole body is in that buffer, net/http sets Content-Length
// itself, without an allocation. TestContentLengthOnWire checks the limit.
const autoLengthBytes = 2048

func setBodyHeaders(c *gin.Context, size int64) {
	h := c.Writer.Header()
	h["Content-Type"] = jsonContentType
	if size > autoLengthBytes || c.Request.Method == http.MethodHead {
		h["Content-Length"] = []string{strconv.FormatInt(size, 10)}
	}
}

type paramsJSON struct {
	Int1  int    `json:"int1"`
	Int2  int    `json:"int2"`
	Limit int    `json:"limit"`
	Str1  string `json:"str1"`
	Str2  string `json:"str2"`
}

func (s *server) statistics(c *gin.Context) {
	p, hits, err := s.top(c.Request.Context())
	if err != nil {
		if !errors.Is(err, errStatsUnavailable) {
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
// parameters. Params.Validate checks the ranges. It accepts and rejects the
// same queries as url.ParseQuery, but it does not build a url.Values: a
// value without escapes stays a substring of the query. FuzzParseParams
// compares it with url.ParseQuery.
func parseParams(rawQuery string) (fizzbuzz.Params, error) {
	var p fizzbuzz.Params
	vals, counts, ok := scanQuery(rawQuery)
	if !ok {
		return p, &fizzbuzz.FieldError{Param: "query", Reason: "malformed percent-encoding"}
	}
	get := func(i int) (string, error) {
		switch counts[i] {
		case 0:
			return "", &fizzbuzz.FieldError{Param: paramNames[i], Reason: "is required"}
		case 1:
			return vals[i], nil
		default:
			return "", &fizzbuzz.FieldError{Param: paramNames[i], Reason: "must be given exactly once"}
		}
	}
	getInt := func(i int) (int, error) {
		v, err := get(i)
		if err != nil {
			return 0, err
		}
		n, err := strconv.ParseInt(v, 10, 64)
		switch {
		case errors.Is(err, strconv.ErrRange):
			return 0, &fizzbuzz.FieldError{Param: paramNames[i], Reason: "is out of range"}
		case err != nil:
			return 0, &fizzbuzz.FieldError{Param: paramNames[i], Reason: "must be a base-10 integer"}
		}
		return int(n), nil
	}
	var err error
	if p.Int1, err = getInt(0); err != nil {
		return p, err
	}
	if p.Int2, err = getInt(1); err != nil {
		return p, err
	}
	if p.Limit, err = getInt(2); err != nil {
		return p, err
	}
	if p.Str1, err = get(3); err != nil {
		return p, err
	}
	p.Str2, err = get(4)
	return p, err
}

var paramNames = [5]string{"int1", "int2", "limit", "str1", "str2"}

// maxQueryParams is the default limit of url.ParseQuery (GODEBUG
// urlmaxqueryparams).
const maxQueryParams = 10000

// scanQuery returns the first value and the count (0, 1 or 2 for "more") of
// each parameter in paramNames. ok is false where url.ParseQuery returns an
// error: too many parameters, a key with ';', or a bad escape in any key or
// value.
func scanQuery(q string) (vals [5]string, counts [5]uint8, ok bool) {
	if strings.Count(q, "&") >= maxQueryParams {
		return vals, counts, false
	}
	for q != "" {
		var seg string
		seg, q, _ = strings.Cut(q, "&")
		if strings.IndexByte(seg, ';') >= 0 {
			return vals, counts, false
		}
		if seg == "" {
			continue
		}
		k, v, _ := strings.Cut(seg, "=")
		k, ok := queryUnescape(k)
		if !ok {
			return vals, counts, false
		}
		v, ok = queryUnescape(v)
		if !ok {
			return vals, counts, false
		}
		for i, name := range paramNames {
			if k == name {
				if counts[i] == 0 {
					vals[i] = v
				}
				counts[i] = min(counts[i]+1, 2)
				break
			}
		}
	}
	return vals, counts, true
}

// queryUnescape returns s itself when it has nothing to decode.
func queryUnescape(s string) (string, bool) {
	if strings.IndexByte(s, '%') < 0 && strings.IndexByte(s, '+') < 0 {
		return s, true
	}
	u, err := url.QueryUnescape(s)
	return u, err == nil
}

// deadlineWriter disconnects clients that stop reading. net/http clears the
// deadline when the response is complete.
type deadlineWriter struct {
	w  http.ResponseWriter
	dl deadliner // nil when the writer has no deadline, as in httptest
	d  time.Duration
}

// A pooled writer, because the generator takes an io.Writer, and a value in
// an interface moves to the heap.
var dwPool = sync.Pool{New: func() any { return new(deadlineWriter) }}

func (d *deadlineWriter) Write(b []byte) (int, error) {
	if d.dl != nil {
		_ = d.dl.SetWriteDeadline(time.Now().Add(d.d))
	}
	return d.w.Write(b)
}

type deadliner interface{ SetWriteDeadline(time.Time) error }

// findDeadliner does what http.NewResponseController does for
// SetWriteDeadline, without the allocation of the controller.
func findDeadliner(w http.ResponseWriter) deadliner {
	for {
		switch t := w.(type) {
		case deadliner:
			return t
		case interface{ Unwrap() http.ResponseWriter }:
			w = t.Unwrap()
		default:
			return nil
		}
	}
}
