package main

import (
	"errors"
	"io"
	"mime"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
)

// QUERY (RFC 10008) is GET with the input in the body. It is safe and
// idempotent, and /stats counts it like GET. The body uses the form encoding of
// the GET query string, so both use one parser and one set of rules.
const queryMediaType = "application/x-www-form-urlencoded"

// acceptQuery advertises QUERY support on every /fizzbuzz response (RFC 10008 §3).
func acceptQuery(c *gin.Context) { c.Writer.Header()["Accept-Query"] = acceptQueryHeader }

func (s *server) query(c *gin.Context) {
	// RFC 10008 §2: reject a missing or unsupported Content-Type.
	if mt, _, _ := mime.ParseMediaType(c.GetHeader("Content-Type")); mt != queryMediaType {
		c.JSON(http.StatusUnsupportedMediaType, gin.H{"error": "Content-Type: must be " + queryMediaType})
		return
	}
	// The body gets the same budget as a query string in the request header.
	limit := int64(s.cfg.MaxHeaderBytes)
	body, err := io.ReadAll(http.MaxBytesReader(c.Writer, c.Request.Body, limit))
	if err != nil {
		if tooBig := (*http.MaxBytesError)(nil); errors.As(err, &tooBig) {
			c.JSON(http.StatusRequestEntityTooLarge, gin.H{"error": "body: must be at most " + strconv.FormatInt(limit, 10) + " bytes"})
		} else {
			c.JSON(http.StatusBadRequest, gin.H{"error": "body: unreadable"})
		}
		return
	}
	c.Request.URL.RawQuery = string(body)
	s.fizzbuzz(c)
}
