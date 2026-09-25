package main

import (
	"io"
	"strconv"
	"sync"
	"time"
	"unicode/utf8"
)

// requestLog writes the request line of slog.JSONHandler by hand. slog
// formats the float with encoding/json and walks its attributes through
// interfaces. This writer appends the same bytes to a pooled buffer.
// TestRequestLogMatchesSlog and FuzzRequestLog compare it with slog.
type requestLog struct {
	mu sync.Mutex // one Write for each line, in order
	w  io.Writer
}

var logBufPool = sync.Pool{New: func() any { b := make([]byte, 0, 256); return &b }}

func (l *requestLog) log(t time.Time, method, path string, status int, elapsed time.Duration) {
	bp := logBufPool.Get().(*[]byte)
	b := appendRequestLine((*bp)[:0], t, method, path, status, elapsed)
	l.mu.Lock()
	_, _ = l.w.Write(b)
	l.mu.Unlock()
	*bp = b
	logBufPool.Put(bp)
}

func appendRequestLine(b []byte, t time.Time, method, path string, status int, elapsed time.Duration) []byte {
	b = append(b, `{"time":"`...)
	b = t.AppendFormat(b, time.RFC3339Nano)
	b = append(b, `","level":"INFO","msg":"request","method":`...)
	b = appendJSONString(b, method)
	b = append(b, `,"path":`...)
	b = appendJSONString(b, path)
	b = append(b, `,"status":`...)
	b = strconv.AppendInt(b, int64(status), 10)
	b = append(b, `,"duration_ms":`...)
	b = appendMillis(b, elapsed.Microseconds())
	return append(b, "}\n"...)
}

// appendMillis writes us/1000 as json.Marshal writes the float64. For
// |us| < 10^15 the float has at most 15 significant digits, so its shortest
// form is the exact decimal.
func appendMillis(b []byte, us int64) []byte {
	if us < 0 {
		b = append(b, '-')
		us = -us
	}
	b = strconv.AppendInt(b, us/1000, 10)
	frac := us % 1000
	if frac == 0 {
		return b
	}
	d := [4]byte{'.', byte('0' + frac/100), byte('0' + frac/10%10), byte('0' + frac%10)}
	n := 4
	for d[n-1] == '0' {
		n--
	}
	return append(b, d[:n]...)
}

const hexDigits = "0123456789abcdef"

// appendJSONString escapes like slog: quote, backslash and control
// characters, U+2028 and U+2029. Invalid UTF-8 becomes a raw U+FFFD. It does not
// escape <, > and &.
func appendJSONString(b []byte, s string) []byte {
	b = append(b, '"')
	start := 0
	for i := 0; i < len(s); {
		if c := s[i]; c < utf8.RuneSelf {
			if c >= 0x20 && c != '"' && c != '\\' {
				i++
				continue
			}
			b = append(b, s[start:i]...)
			switch c {
			case '"', '\\':
				b = append(b, '\\', c)
			case '\n':
				b = append(b, '\\', 'n')
			case '\r':
				b = append(b, '\\', 'r')
			case '\t':
				b = append(b, '\\', 't')
			default:
				b = append(b, '\\', 'u', '0', '0', hexDigits[c>>4], hexDigits[c&0xf])
			}
			i++
			start = i
			continue
		}
		r, size := utf8.DecodeRuneInString(s[i:])
		if r == utf8.RuneError && size == 1 {
			b = append(b, s[start:i]...)
			b = append(b, "\ufffd"...) // raw, as slog writes it with encoding/json/v2
			i += size
			start = i
			continue
		}
		if r == ' ' || r == ' ' {
			b = append(b, s[start:i]...)
			b = append(b, '\\', 'u', '2', '0', '2', hexDigits[r&0xf])
			i += size
			start = i
			continue
		}
		i += size
	}
	b = append(b, s[start:]...)
	return append(b, '"')
}
