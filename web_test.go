package main

import (
	"net/http"
	"strings"
	"testing"
)

func TestUI(t *testing.T) {
	_, h := newTestServer(t)
	for path, ct := range map[string]string{
		"/":        "text/html; charset=utf-8",
		"/app.js":  "text/javascript; charset=utf-8",
		"/app.css": "text/css; charset=utf-8",
	} {
		rec := do(h, http.MethodGet, path)
		// nosniff makes the browser refuse a script or stylesheet served with the wrong type.
		if rec.Code != http.StatusOK || rec.Header().Get("Content-Type") != ct || rec.Body.Len() == 0 {
			t.Errorf("%s: got %d %q, %d bytes", path, rec.Code, rec.Header().Get("Content-Type"), rec.Body.Len())
		}
		if !strings.Contains(rec.Header().Get("Content-Security-Policy"), "default-src 'self'") {
			t.Errorf("%s: missing CSP", path)
		}
	}
	index := do(h, http.MethodGet, "/").Body.String()
	for _, ref := range []string{`src="/app.js"`, `href="/app.css"`} {
		if !strings.Contains(index, ref) {
			t.Errorf("index.html does not reference %s", ref)
		}
	}
}
