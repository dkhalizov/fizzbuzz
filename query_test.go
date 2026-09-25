package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func query(h http.Handler, contentType, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest("QUERY", "/fizzbuzz", strings.NewReader(body))
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestQueryMatchesGet(t *testing.T) {
	s, h := newTestServer(t)
	get := do(h, http.MethodGet, classic)
	q := query(h, "application/x-www-form-urlencoded; charset=utf-8", strings.TrimPrefix(classic, "/fizzbuzz?"))
	if q.Code != http.StatusOK || q.Body.String() != get.Body.String() ||
		q.Header().Get("Content-Length") != get.Header().Get("Content-Length") {
		t.Fatalf("QUERY got %d %q, GET got %q", q.Code, q.Body, get.Body)
	}
	for _, rec := range []*httptest.ResponseRecorder{get, q} {
		if a := rec.Header().Get("Accept-Query"); a != queryMediaType {
			t.Errorf("Accept-Query = %q", a)
		}
	}
	if _, hits, _ := s.store.Top(t.Context()); hits != 2 {
		t.Errorf("hits = %d, want GET and QUERY counted as the same request", hits)
	}
	if methodLabel("QUERY") != "QUERY" {
		t.Error(`QUERY is reported as method="other" in metrics`)
	}
}

func TestQueryErrors(t *testing.T) {
	_, h := newTestServer(t)
	form := "application/x-www-form-urlencoded"
	for _, c := range []struct {
		contentType, body string
		code              int
		param             string
	}{
		{"", "int1=3", http.StatusUnsupportedMediaType, "Content-Type"},
		{"application/json", `{"int1":3}`, http.StatusUnsupportedMediaType, "Content-Type"},
		{form, "int1=3&int2=5&limit=15&str1=a&str2=" + strings.Repeat("x", 17<<10), http.StatusRequestEntityTooLarge, "body"},
		{form, "int1=3&int2=5&limit=0&str1=a&str2=b", http.StatusBadRequest, "limit"},
	} {
		rec := query(h, c.contentType, c.body)
		var body struct{ Error string }
		_ = json.Unmarshal(rec.Body.Bytes(), &body)
		if rec.Code != c.code || !strings.HasPrefix(body.Error, c.param+":") {
			t.Errorf("%q %.40s: got %d %q, want %d naming %s", c.contentType, c.body, rec.Code, body.Error, c.code, c.param)
		}
		if rec.Header().Get("Accept-Query") != queryMediaType {
			t.Errorf("%q: error response lacks Accept-Query", c.contentType)
		}
	}
}
