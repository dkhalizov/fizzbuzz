package main

import (
	"context"
	"io"
	"strconv"
	"sync"
	"time"
	"unicode/utf8"
)

// requestLog writes the request line of slog.JSONHandler by hand. slog
// formats the float with encoding/json and walks its attributes through
// interfaces. TestRequestLogMatchesSlog and FuzzRequestLog compare it with slog.
//
// The lines collect in one buffer. run sends the buffer every logFlushInterval,
// or at once when it has logFlushBytes. Thus a request does not make a write
// syscall. The cost: a crash loses the lines of the last interval, and a line
// can appear up to logFlushInterval after its request.
type requestLog struct {
	mu    sync.Mutex // guards buf
	buf   []byte
	spare []byte
	wmu   sync.Mutex // one Write at a time, in order
	w     io.Writer
	kick  chan struct{}
}

const (
	logFlushInterval = 100 * time.Millisecond
	logFlushBytes    = 64 << 10
	logMaxBytes      = 1 << 20 // then the request writes: backpressure from a slow reader
)

func newRequestLog(w io.Writer) *requestLog {
	return &requestLog{w: w, buf: make([]byte, 0, 2*logFlushBytes), spare: make([]byte, 0, 2*logFlushBytes), kick: make(chan struct{}, 1)}
}

func (l *requestLog) log(t time.Time, method, path string, status int, elapsed time.Duration) {
	l.mu.Lock()
	l.buf = appendRequestLine(l.buf, t, method, path, status, elapsed)
	n := len(l.buf)
	l.mu.Unlock()
	if n >= logMaxBytes {
		l.flush()
	} else if n >= logFlushBytes {
		select {
		case l.kick <- struct{}{}:
		default:
		}
	}
}

// flush swaps the buffers under mu and writes outside it, so requests do not
// wait for the write.
func (l *requestLog) flush() {
	l.wmu.Lock()
	defer l.wmu.Unlock()
	l.mu.Lock()
	b := l.buf
	l.buf, l.spare = l.spare[:0], nil
	l.mu.Unlock()
	if len(b) > 0 {
		_, _ = l.w.Write(b)
	}
	l.mu.Lock()
	l.spare = b[:0]
	l.mu.Unlock()
}

// run flushes until ctx ends, then one last time.
func (l *requestLog) run(ctx context.Context) {
	t := time.NewTicker(logFlushInterval)
	defer t.Stop()
	for {
		select {
		case <-t.C:
		case <-l.kick:
		case <-ctx.Done():
			l.flush()
			return
		}
		l.flush()
	}
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
