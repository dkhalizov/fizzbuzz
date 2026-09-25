package main

import (
	"encoding/json"
	"errors"
	"mime"
	"net/http"
	"net/url"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
	"unsafe"

	"fizzbuzz/internal/fizzbuzz"
)

const (
	loopBodyMax  = 256 << 10 // larger responses go to a goroutine
	cacheBodyMax = 64 << 10
	cacheMaxLen  = 4096
	cacheMaxSize = 32 << 20
	logFlush     = 64 << 10
)

type conn struct {
	fd       int
	buf      []byte // request bytes in buf[r:w]
	r, w     int
	out      []byte
	outOff   int
	waitOut  bool // out waits for EPOLLOUT; reading stops
	closing  bool // close after out is sent
	owned    bool // a goroutine writes a large response
	dead     bool // set by that goroutine when the write failed
	last     int64
	partial  int64 // when the current incomplete request began
	outSince int64
}

type entry struct {
	resp   []byte // complete GET response
	dateAt int
	cell   *atomic.Uint64
}

type loop struct {
	s          *server
	id         int
	epfd, lfd  int
	wakeR      int
	wakeW      int
	conns      []*conn
	shard      *shard
	cache      map[string]*entry
	cacheSize  int
	date       [29]byte
	dateSec    int64
	now        int64
	lastSweep  int64
	logbuf     []byte
	lastLog    int64
	backMu     sync.Mutex
	back       []*conn
	backLogs   [][]byte
	events     [256]syscall.EpollEvent
	timeoutMs  int
	maxRequest int
}

func newLoop(s *server, id int, sh *shard) (*loop, error) {
	l := &loop{s: s, id: id, shard: sh, cache: map[string]*entry{}, timeoutMs: 1000}
	if s.cfg.spin {
		l.timeoutMs = 0
	}
	l.maxRequest = 2*s.cfg.maxHeader + 4096
	var err error
	if l.lfd, err = listenReusePort(s.cfg.port); err != nil {
		return nil, err
	}
	if l.epfd, err = syscall.EpollCreate1(syscall.EPOLL_CLOEXEC); err != nil {
		return nil, err
	}
	var p [2]int
	if err = syscall.Pipe2(p[:], syscall.O_NONBLOCK|syscall.O_CLOEXEC); err != nil {
		return nil, err
	}
	l.wakeR, l.wakeW = p[0], p[1]
	for _, fd := range []int{l.lfd, l.wakeR} {
		if err := syscall.EpollCtl(l.epfd, syscall.EPOLL_CTL_ADD, fd, &syscall.EpollEvent{Events: syscall.EPOLLIN, Fd: int32(fd)}); err != nil {
			return nil, err
		}
	}
	l.tick()
	return l, nil
}

func (l *loop) run(cpu int) {
	runtime.LockOSThread()
	if cpu >= 0 {
		pinThread(cpu)
	}
	for {
		n, err := syscall.EpollWait(l.epfd, l.events[:], l.timeoutMs)
		if err != nil && err != syscall.EINTR {
			panic(err)
		}
		l.tick()
		for i := range n {
			ev := l.events[i]
			fd := int(ev.Fd)
			switch fd {
			case l.lfd:
				l.accept()
			case l.wakeR:
				l.takeBack()
			default:
				c := l.conns[fd]
				if c == nil || c.owned {
					continue
				}
				if ev.Events&syscall.EPOLLOUT != 0 {
					l.flushOut(c)
				} else {
					l.readConn(c)
				}
			}
		}
	}
}

func (l *loop) tick() {
	now := time.Now()
	l.now = now.UnixNano()
	if sec := now.Unix(); sec != l.dateSec {
		l.dateSec = sec
		now.UTC().AppendFormat(l.date[:0], http.TimeFormat)
	}
	if l.s.cfg.log && len(l.logbuf) > 0 && (len(l.logbuf) >= logFlush || l.now-l.lastLog > int64(100*time.Millisecond)) {
		_, _ = writeAll(1, l.logbuf)
		l.logbuf = l.logbuf[:0]
		l.lastLog = l.now
	}
	if l.now-l.lastSweep > int64(time.Second) {
		l.lastSweep = l.now
		l.sweep()
	}
}

// sweep closes idle connections, slow request heads and stuck writes, as the
// timeouts of the service do.
func (l *loop) sweep() {
	cfg := l.s.cfg
	for _, c := range l.conns {
		switch {
		case c == nil || c.owned:
		case c.waitOut && l.now-c.outSince > int64(cfg.writeTimeout),
			c.partial != 0 && l.now-c.partial > int64(cfg.readHeader),
			l.now-c.last > int64(cfg.idle):
			l.closeConn(c)
		}
	}
}

func (l *loop) accept() {
	for {
		fd, _, err := syscall.Accept4(l.lfd, syscall.SOCK_NONBLOCK|syscall.SOCK_CLOEXEC)
		if err != nil {
			return // EAGAIN, or an error that the next accept reports again
		}
		_ = syscall.SetsockoptInt(fd, syscall.IPPROTO_TCP, syscall.TCP_NODELAY, 1)
		if l.s.cfg.sndbuf > 0 {
			_ = syscall.SetsockoptInt(fd, syscall.SOL_SOCKET, syscall.SO_SNDBUF, l.s.cfg.sndbuf)
		}
		if err := syscall.EpollCtl(l.epfd, syscall.EPOLL_CTL_ADD, fd, &syscall.EpollEvent{Events: syscall.EPOLLIN | syscall.EPOLLRDHUP, Fd: int32(fd)}); err != nil {
			syscall.Close(fd)
			continue
		}
		for fd >= len(l.conns) {
			l.conns = append(l.conns, nil)
		}
		l.conns[fd] = &conn{fd: fd, buf: make([]byte, 4096), last: l.now}
	}
}

func (l *loop) closeConn(c *conn) {
	l.conns[c.fd] = nil
	syscall.Close(c.fd) // also removes it from epoll
}

func (l *loop) readConn(c *conn) {
	if c.w == len(c.buf) {
		if len(c.buf) >= l.maxRequest {
			l.closeConn(c)
			return
		}
		nb := make([]byte, min(2*len(c.buf), l.maxRequest))
		copy(nb, c.buf[:c.w])
		c.buf = nb
	}
	n, err := syscall.Read(c.fd, c.buf[c.w:])
	if n <= 0 {
		if err != syscall.EAGAIN {
			l.closeConn(c)
		}
		return
	}
	c.w += n
	c.last = l.now
	l.process(c)
}

func (l *loop) process(c *conn) {
	for c.r < c.w && !c.closing {
		req, n, bad := parse(c.buf[c.r:c.w], l.s.cfg.maxHeader)
		if bad != 0 {
			req.close = true
			c.out = l.appendError(c.out, &req, bad, strings.ToLower(statusText[bad]), false)
			c.closing = true
			break
		}
		if n == 0 {
			if c.partial == 0 {
				c.partial = l.now
			}
			break
		}
		c.partial = 0
		c.r += n
		if req.close {
			c.closing = true
		}
		if l.handle(c, &req) {
			return // a goroutine owns the connection now
		}
	}
	if c.r == c.w {
		c.r, c.w = 0, 0
	} else if c.r > 0 {
		c.w = copy(c.buf, c.buf[c.r:c.w])
		c.r = 0
	}
	l.flushOut(c)
}

func (l *loop) flushOut(c *conn) {
	for c.outOff < len(c.out) {
		n, err := syscall.Write(c.fd, c.out[c.outOff:])
		if err == syscall.EAGAIN {
			if !c.waitOut {
				c.waitOut, c.outSince = true, l.now
				_ = syscall.EpollCtl(l.epfd, syscall.EPOLL_CTL_MOD, c.fd, &syscall.EpollEvent{Events: syscall.EPOLLOUT, Fd: int32(c.fd)})
			}
			return
		}
		if err != nil {
			l.closeConn(c)
			return
		}
		c.outOff += n
	}
	if cap(c.out) > 64<<10 {
		c.out = nil // a large response does not keep its buffer
	} else {
		c.out = c.out[:0]
	}
	c.outOff = 0
	if c.closing {
		l.closeConn(c)
		return
	}
	if c.waitOut {
		c.waitOut = false
		_ = syscall.EpollCtl(l.epfd, syscall.EPOLL_CTL_MOD, c.fd, &syscall.EpollEvent{Events: syscall.EPOLLIN | syscall.EPOLLRDHUP, Fd: int32(c.fd)})
		l.process(c) // requests that arrived while the response waited
	}
}

// str views b as a string without a copy. The string is valid until the
// connection buffer changes, which is after the request is complete.
func str(b []byte) string { return unsafe.String(unsafe.SliceData(b), len(b)) }

// handle appends the response to c.out. It returns true when it gave the
// connection to a goroutine.
func (l *loop) handle(c *conn, req *request) bool {
	var start time.Time
	if l.s.cfg.log {
		start = time.Now()
	}
	target := req.target
	path, query := target, []byte(nil)
	if i := indexByte(target, '?'); i >= 0 {
		path, query = target[:i], target[i+1:]
	}
	pathS := str(path)
	if strings.IndexByte(pathS, '%') >= 0 {
		if u, err := url.PathUnescape(pathS); err == nil {
			pathS = u
		}
	}
	method := str(req.method)
	status := 200
	switch pathS {
	case "/fizzbuzz":
		switch method {
		case "GET", "HEAD", "QUERY":
			var handoff bool
			status, handoff = l.fizzbuzz(c, req, query, start)
			if handoff {
				return true
			}
		default:
			status = 405
			c.out = l.appendJSON(c.out, req, 405, []byte(`{"error":"method not allowed"}`), false, "GET, HEAD, QUERY", false)
		}
	case "/stats", "/healthz":
		if method != "GET" {
			status = 405
			c.out = l.appendJSON(c.out, req, 405, []byte(`{"error":"method not allowed"}`), false, "GET", false)
		} else if pathS == "/healthz" {
			c.out = l.appendJSON(c.out, req, 200, []byte(`{"status":"ok"}`), false, "", false)
		} else {
			status = l.stats(c, req)
		}
	default:
		status = 404
		c.out = l.appendJSON(c.out, req, 404, []byte(`{"error":"not found"}`), false, "", false)
	}
	l.logLine(start, method, pathS, status)
	return false
}

func indexByte(b []byte, c byte) int {
	for i, x := range b {
		if x == c {
			return i
		}
	}
	return -1
}

func (l *loop) logLine(start time.Time, method, path string, status int) {
	if !l.s.cfg.log {
		return
	}
	end := time.Now()
	l.logbuf = appendRequestLine(l.logbuf, end, method, path, status, end.Sub(start))
}

func (l *loop) fizzbuzz(c *conn, req *request, query []byte, start time.Time) (status int, handoff bool) {
	get := str(req.method) == "GET"
	cacheable := l.s.cfg.cache && get && !req.close && !req.http10
	if cacheable {
		if e := l.cache[string(query)]; e != nil {
			at := len(c.out)
			c.out = append(c.out, e.resp...)
			copy(c.out[at+e.dateAt:], l.date[:])
			if e.cell != nil {
				e.cell.Add(1)
			}
			return 200, false
		}
	}
	q := str(query)
	if str(req.method) == "QUERY" {
		if req.contentLength > l.s.cfg.maxHeader {
			req.close, c.closing = true, true
			c.out = l.appendError(c.out, req, 413, "body: must be at most 16384 bytes", true)
			return 413, false
		}
		if mt, _, _ := mime.ParseMediaType(string(req.contentType)); mt != "application/x-www-form-urlencoded" {
			c.out = l.appendError(c.out, req, 415, "Content-Type: must be application/x-www-form-urlencoded", true)
			return 415, false
		}
		q = str(req.body)
	}
	p, err := parseParams(q)
	var resp fizzbuzz.Response
	if err == nil {
		resp, err = p.Prepare(l.s.cfg.limits)
	}
	if err != nil {
		c.out = l.appendError(c.out, req, 400, err.Error(), true)
		return 400, false
	}
	size := resp.Size
	if str(req.method) == "HEAD" {
		c.out, _ = l.appendFizzHead(c.out, req, size, true)
		return 200, false
	}
	if l.s.inflight.Add(size) > l.s.cfg.maxInflight {
		l.s.inflight.Add(-size)
		c.out = l.appendError(c.out, req, 503, "server busy: too many large responses in flight", true)
		return 503, false
	}
	cell := l.shard.cell(p)
	if cell != nil {
		cell.Add(1)
	}
	if size > loopBodyMax {
		l.handoff(c, req, resp, start)
		return 200, true
	}
	at := len(c.out)
	var dateAt int
	c.out, dateAt = l.appendFizzHead(c.out, req, size, false)
	_, _ = resp.WriteTo(appendWriter{&c.out})
	l.s.inflight.Add(-size)
	if cacheable && size <= cacheBodyMax {
		l.store(string(query), c.out[at:], dateAt-at, cell)
	}
	return 200, false
}

func (l *loop) store(key string, resp []byte, dateAt int, cell *atomic.Uint64) {
	if len(l.cache) >= cacheMaxLen || l.cacheSize+len(resp) > cacheMaxSize {
		l.cache, l.cacheSize = map[string]*entry{}, 0 // bounded memory, simple eviction
	}
	l.cache[key] = &entry{resp: append([]byte(nil), resp...), dateAt: dateAt, cell: cell}
	l.cacheSize += len(resp) + len(key)
}

// handoff gives the connection to a goroutine that writes the response with
// blocking writes. The loop takes the connection back after it.
func (l *loop) handoff(c *conn, req *request, resp fizzbuzz.Response, start time.Time) {
	c.owned = true
	_ = syscall.EpollCtl(l.epfd, syscall.EPOLL_CTL_DEL, c.fd, nil)
	pending := append([]byte(nil), c.out[c.outOff:]...)
	pending, _ = l.appendFizzHead(pending, req, resp.Size, false)
	c.out, c.outOff = c.out[:0], 0
	method := string(req.method)
	go func() {
		setBlocking(c.fd, int64(l.s.cfg.writeTimeout))
		_, err := writeAll(c.fd, pending)
		if err == nil {
			_, err = resp.WriteTo(fdWriter{c.fd})
		}
		l.s.inflight.Add(-resp.Size)
		_ = syscall.SetNonblock(c.fd, true)
		c.dead = err != nil
		var line []byte
		if l.s.cfg.log {
			end := time.Now()
			line = appendRequestLine(nil, end, method, "/fizzbuzz", 200, end.Sub(start))
		}
		l.backMu.Lock()
		l.back = append(l.back, c)
		l.backLogs = append(l.backLogs, line)
		l.backMu.Unlock()
		_, _ = syscall.Write(l.wakeW, []byte{1})
	}()
}

func (l *loop) takeBack() {
	var drain [64]byte
	for {
		if n, _ := syscall.Read(l.wakeR, drain[:]); n <= 0 {
			break
		}
	}
	l.backMu.Lock()
	back, logs := l.back, l.backLogs
	l.back, l.backLogs = nil, nil
	l.backMu.Unlock()
	for _, line := range logs {
		l.logbuf = append(l.logbuf, line...)
	}
	for _, c := range back {
		c.owned = false
		c.last = l.now
		if c.dead || c.closing {
			l.closeConn(c)
			continue
		}
		if err := syscall.EpollCtl(l.epfd, syscall.EPOLL_CTL_ADD, c.fd, &syscall.EpollEvent{Events: syscall.EPOLLIN | syscall.EPOLLRDHUP, Fd: int32(c.fd)}); err != nil {
			l.closeConn(c)
			continue
		}
		l.process(c)
	}
}

type paramsJSON struct {
	Int1  int    `json:"int1"`
	Int2  int    `json:"int2"`
	Limit int    `json:"limit"`
	Str1  string `json:"str1"`
	Str2  string `json:"str2"`
}

func (l *loop) stats(c *conn, req *request) int {
	k, hits, err := top(l.s.shards)
	if err != nil {
		if !errors.Is(err, errSaturated) {
			err = errors.New("statistics unavailable: store error")
		}
		c.out = l.appendError(c.out, req, 503, err.Error(), false)
		return 503
	}
	resp := struct {
		Params *paramsJSON `json:"params"`
		Hits   uint64      `json:"hits"`
	}{Hits: hits}
	if hits > 0 {
		resp.Params = &paramsJSON{k.int1, k.int2, k.limit, k.str1, k.str2}
	}
	body, _ := json.Marshal(resp)
	c.out = l.appendJSON(c.out, req, 200, body, false, "", false)
	return 200
}
