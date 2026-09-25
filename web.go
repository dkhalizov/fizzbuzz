package main

import (
	_ "embed"
	"net/http"

	"github.com/gin-gonic/gin"
)

var (
	//go:embed web/index.html
	indexHTML []byte
	//go:embed web/app.js
	appJS []byte
	//go:embed web/app.css
	appCSS []byte
)

// registerUI serves the embedded single-page UI. It has no inline script or
// style, so the CSP allows only this origin.
func registerUI(r *gin.Engine) {
	r.GET("/", asset("text/html; charset=utf-8", indexHTML))
	r.GET("/app.js", asset("text/javascript; charset=utf-8", appJS))
	r.GET("/app.css", asset("text/css; charset=utf-8", appCSS))
}

func asset(contentType string, body []byte) gin.HandlerFunc {
	return func(c *gin.Context) {
		h := c.Writer.Header()
		h.Set("Content-Security-Policy", "default-src 'self'; frame-ancestors 'none'")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Cache-Control", "no-cache")
		c.Data(http.StatusOK, contentType, body)
	}
}
