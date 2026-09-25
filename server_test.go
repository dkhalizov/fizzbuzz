package main

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
)

// newTestServer returns a server with its own counter and registry, so tests
// share no state.
func newTestServer(t *testing.T) (*server, http.Handler) {
	t.Helper()
	s := newServer(testConfig(t), slog.New(slog.DiscardHandler), newCounter(1<<20))
	return s, s.handler(prometheus.NewRegistry())
}

func testConfig(t *testing.T) config {
	t.Helper()
	cfg, err := loadConfig(func(string) string { return "" })
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

func do(h http.Handler, method, target string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(method, target, nil))
	return rec
}

const classic = "/fizzbuzz?int1=3&int2=5&limit=15&str1=fizz&str2=buzz"

func TestFizzbuzz(t *testing.T) {
	_, h := newTestServer(t)
	rec := do(h, http.MethodGet, classic)
	want := `["1","2","fizz","4","buzz","fizz","7","8","fizz","buzz","11","fizz","13","14","fizzbuzz"]`
	if rec.Code != http.StatusOK || rec.Body.String() != want {
		t.Fatalf("got %d %s", rec.Code, rec.Body)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q", ct)
	}
	if cl := rec.Header().Get("Content-Length"); cl != strconv.Itoa(len(want)) {
		t.Errorf("Content-Length = %q, want %d", cl, len(want))
	}
}

func TestFizzbuzzErrors(t *testing.T) {
	_, h := newTestServer(t)
	cases := []struct{ query, param string }{
		{"int2=5&limit=15&str1=a&str2=b", "int1"},                           // missing
		{"int1=3&int1=4&int2=5&limit=15&str1=a&str2=b", "int1"},             // repeated
		{"int1=3.5&int2=5&limit=15&str1=a&str2=b", "int1"},                  // not an integer
		{"int1=99999999999999999999&int2=5&limit=15&str1=a&str2=b", "int1"}, // beyond int64
		{"int1=3&int2=0&limit=15&str1=a&str2=b", "int2"},
		{"int1=3&int2=5&limit=10000001&str1=a&str2=b", "limit"},
		{"int1=3&int2=5&limit=15&str1=&str2=b", "str1"},
		{"int1=3&int2=5&limit=15&str1=a&str2=%FF", "str2"}, // invalid UTF-8
		{"int1=3&int2=5&limit=15&str1=a&str2=" + strings.Repeat("x", 1025), "str2"},
		{"int1=1&int2=1&limit=10000000&str1=" + strings.Repeat("x", 1000) + "&str2=y", "limit"}, // ~10 GB
		{"int1=%zz", "query"},
	}
	for _, c := range cases {
		rec := do(h, http.MethodGet, "/fizzbuzz?"+c.query)
		var body struct{ Error string }
		_ = json.Unmarshal(rec.Body.Bytes(), &body)
		if rec.Code != http.StatusBadRequest || !strings.HasPrefix(body.Error, c.param+":") {
			t.Errorf("%.60s: got %d %q, want 400 naming %s", c.query, rec.Code, body.Error, c.param)
		}
	}
}

func TestHead(t *testing.T) {
	s, h := newTestServer(t)
	rec := do(h, http.MethodHead, classic)
	if rec.Code != http.StatusOK || rec.Body.Len() != 0 || rec.Header().Get("Content-Length") != "89" {
		t.Fatalf("got %d, body %d bytes, Content-Length %q", rec.Code, rec.Body.Len(), rec.Header().Get("Content-Length"))
	}
	if _, hits, _ := s.store.Top(context.Background()); hits != 0 {
		t.Errorf("HEAD was counted: hits = %d", hits)
	}
}

func TestStatsEndpoint(t *testing.T) {
	_, h := newTestServer(t)
	stats := func() string { return strings.TrimSpace(do(h, http.MethodGet, "/stats").Body.String()) }

	if got := stats(); got != `{"params":null,"hits":0}` {
		t.Fatalf("empty stats = %s", got)
	}
	do(h, http.MethodGet, classic)
	do(h, http.MethodGet, "/fizzbuzz?str2=buzz&limit=15&int1=03&int2=5&str1=fizz") // same parsed params
	do(h, http.MethodGet, "/fizzbuzz?int1=5&int2=3&limit=15&str1=buzz&str2=fizz")  // swapped: another key
	do(h, http.MethodGet, "/fizzbuzz?int1=0&int2=3&limit=15&str1=a&str2=b")        // invalid
	do(h, http.MethodHead, classic)
	do(h, http.MethodGet, "/healthz")
	do(h, http.MethodGet, "/metrics")

	want := `{"params":{"int1":3,"int2":5,"limit":15,"str1":"fizz","str2":"buzz"},"hits":2}`
	if got := stats(); got != want {
		t.Fatalf("stats = %s\nwant    %s", got, want)
	}
}

func TestStatsSaturated(t *testing.T) {
	s, h := newTestServer(t)
	s.store = newCounter(0) // the first key saturates the counter
	if rec := do(h, http.MethodGet, classic); rec.Code != http.StatusOK {
		t.Fatalf("/fizzbuzz must keep serving, got %d", rec.Code)
	}
	if rec := do(h, http.MethodGet, "/stats"); rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("/stats = %d, want 503", rec.Code)
	}
}

func TestAdmission(t *testing.T) {
	s, h := newTestServer(t)
	s.cfg.MaxInflightBytes = 50 // below the 89-byte classic response
	rec := do(h, http.MethodGet, classic)
	if rec.Code != http.StatusServiceUnavailable || rec.Header().Get("Retry-After") != "1" {
		t.Fatalf("got %d, Retry-After %q", rec.Code, rec.Header().Get("Retry-After"))
	}
	if _, hits, _ := s.store.Top(context.Background()); hits != 0 {
		t.Errorf("rejected request was counted")
	}
}

func TestUnroutedErrorsAreJSON(t *testing.T) {
	_, h := newTestServer(t)
	for _, c := range []struct {
		method, target string
		code           int
	}{
		{http.MethodGet, "/nope", http.StatusNotFound},
		{http.MethodPost, "/fizzbuzz", http.StatusMethodNotAllowed},
	} {
		rec := do(h, c.method, c.target)
		var body struct{ Error string }
		if rec.Code != c.code || json.Unmarshal(rec.Body.Bytes(), &body) != nil || body.Error == "" {
			t.Errorf("%s %s: got %d %q", c.method, c.target, rec.Code, rec.Body)
		}
	}
	if allow := do(h, http.MethodPost, "/fizzbuzz").Header().Get("Allow"); allow != "GET, HEAD, QUERY" {
		t.Errorf("Allow = %q", allow)
	}
}
