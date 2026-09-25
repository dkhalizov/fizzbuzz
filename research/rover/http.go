package main

import (
	"bytes"
	"strconv"
)

type request struct {
	method, target []byte
	http10         bool
	close          bool // no keep-alive after this request
	contentLength  int  // -1 when absent
	contentType    []byte
	body           []byte
}

// parse reads the request at the start of b. It returns its length, 0 when b
// holds only part of it, or a status code for a request it rejects.
func parse(b []byte, maxHeader int) (req request, n, bad int) {
	end := bytes.Index(b, []byte("\r\n\r\n"))
	if end < 0 {
		if len(b) > maxHeader+4096 { // net/http allows 4096 bytes over MaxHeaderBytes
			return req, 0, 431
		}
		return req, 0, 0
	}
	head := b[:end+2]
	nl := bytes.Index(head, []byte("\r\n"))
	line := head[:nl]
	sp1 := bytes.IndexByte(line, ' ')
	sp2 := bytes.LastIndexByte(line, ' ')
	if sp1 <= 0 || sp2 <= sp1+1 {
		return req, 0, 400
	}
	req.method, req.target = line[:sp1], line[sp1+1:sp2]
	switch string(line[sp2+1:]) {
	case "HTTP/1.1":
	case "HTTP/1.0":
		req.http10, req.close = true, true
	default:
		return req, 0, 400
	}
	req.contentLength = -1
	for rest := head[nl+2:]; len(rest) > 0; {
		i := bytes.Index(rest, []byte("\r\n"))
		h := rest[:i]
		rest = rest[i+2:]
		colon := bytes.IndexByte(h, ':')
		if colon <= 0 {
			return req, 0, 400
		}
		name, value := h[:colon], bytes.Trim(h[colon+1:], " \t")
		switch {
		case eqFold(name, "content-length"):
			v, err := strconv.Atoi(string(value))
			if err != nil || v < 0 || req.contentLength >= 0 {
				return req, 0, 400
			}
			req.contentLength = v
		case eqFold(name, "transfer-encoding"):
			return req, 0, 501 // rover reads no chunked bodies
		case eqFold(name, "connection"):
			switch {
			case eqFold(value, "close"):
				req.close = true
			case eqFold(value, "keep-alive"):
				req.close = false
			}
		case eqFold(name, "content-type"):
			req.contentType = value
		}
	}
	n = end + 4
	if req.contentLength > 0 {
		if req.contentLength > maxHeader {
			return req, n, 0 // the handler answers 413 and closes
		}
		if len(b) < n+req.contentLength {
			return req, 0, 0
		}
		req.body = b[n : n+req.contentLength]
		n += req.contentLength
	}
	return req, n, 0
}

func eqFold(b []byte, lower string) bool {
	if len(b) != len(lower) {
		return false
	}
	for i := range b {
		c := b[i]
		if 'A' <= c && c <= 'Z' {
			c += 'a' - 'A'
		}
		if c != lower[i] {
			return false
		}
	}
	return true
}

var statusText = map[int]string{
	200: "OK", 400: "Bad Request", 404: "Not Found", 405: "Method Not Allowed",
	413: "Request Entity Too Large", 415: "Unsupported Media Type",
	431: "Request Header Fields Too Large", 501: "Not Implemented", 503: "Service Unavailable",
}

func appendStatus(out []byte, req *request, status int) []byte {
	if req.http10 {
		out = append(out, "HTTP/1.0 "...)
	} else {
		out = append(out, "HTTP/1.1 "...)
	}
	out = strconv.AppendInt(out, int64(status), 10)
	out = append(out, ' ')
	out = append(out, statusText[status]...)
	return append(out, "\r\n"...)
}

// The header order is that of net/http: the handler headers sorted, then
// Date, then a Content-Length that net/http computed, then Connection.

// appendJSON writes a gin c.JSON response with a small body.
func (l *loop) appendJSON(out []byte, req *request, status int, body []byte, acceptQuery bool, allow string, retryAfter bool) []byte {
	out = appendStatus(out, req, status)
	if acceptQuery {
		out = append(out, "Accept-Query: application/x-www-form-urlencoded\r\n"...)
	}
	if allow != "" {
		out = append(out, "Allow: "...)
		out = append(out, allow...)
		out = append(out, "\r\n"...)
	}
	out = append(out, "Content-Type: application/json; charset=utf-8\r\n"...)
	if retryAfter {
		out = append(out, "Retry-After: 1\r\n"...)
	}
	out = append(out, "Date: "...)
	out = append(out, l.date[:]...)
	if string(req.method) != "HEAD" {
		out = append(out, "\r\nContent-Length: "...)
		out = strconv.AppendInt(out, int64(len(body)), 10)
	}
	out = appendConnection(out, req)
	out = append(out, "\r\n\r\n"...)
	if string(req.method) != "HEAD" {
		out = append(out, body...)
	}
	return out
}

func appendConnection(out []byte, req *request) []byte {
	if req.close && !req.http10 {
		out = append(out, "\r\nConnection: close"...)
	}
	return out
}

// appendError writes {"error":msg}. The messages hold no characters that
// JSON escapes; TestErrorsMatchService compares them with the service.
func (l *loop) appendError(out []byte, req *request, status int, msg string, acceptQuery bool) []byte {
	body := make([]byte, 0, len(msg)+12)
	body = append(body, `{"error":"`...)
	body = append(body, msg...)
	body = append(body, `"}`...)
	return l.appendJSON(out, req, status, body, acceptQuery, "", status == 503)
}

// appendFizzHead writes the head of a /fizzbuzz response. The handler sets
// Content-Length above 2048 bytes and for HEAD. Below, net/http adds it after
// Date. dateAt is the offset of the date in out.
func (l *loop) appendFizzHead(out []byte, req *request, size int64, head bool) (b []byte, dateAt int) {
	out = appendStatus(out, req, 200)
	out = append(out, "Accept-Query: application/x-www-form-urlencoded\r\n"...)
	explicit := size > 2048 || head
	if explicit {
		out = append(out, "Content-Length: "...)
		out = strconv.AppendInt(out, size, 10)
		out = append(out, "\r\n"...)
	}
	out = append(out, "Content-Type: application/json\r\nDate: "...)
	dateAt = len(out)
	out = append(out, l.date[:]...)
	if !explicit {
		out = append(out, "\r\nContent-Length: "...)
		out = strconv.AppendInt(out, size, 10)
	}
	out = appendConnection(out, req)
	return append(out, "\r\n\r\n"...), dateAt
}

type appendWriter struct{ b *[]byte }

func (w appendWriter) Write(p []byte) (int, error) {
	*w.b = append(*w.b, p...)
	return len(p), nil
}
