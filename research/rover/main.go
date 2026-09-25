// Command rover is the fizzbuzz service without net/http and gin, for the
// performance research. It serves GET, HEAD and QUERY /fizzbuzz, /stats and
// /healthz with the same bytes as the service, on raw Linux syscalls:
//
//   - one OS thread for each core, each with an epoll loop and its own
//     SO_REUSEPORT listener, so loops share no lock and no socket
//   - a hand-written HTTP/1.1 parser with keep-alive and pipelining; the
//     responses to one read go out in one write
//   - a cache of complete small responses in each loop, keyed by the raw
//     query; a hit is one map lookup, one copy and one atomic add for /stats
//   - exact statistics in one shard for each loop
//   - large responses on a goroutine with blocking writes and SO_SNDTIMEO,
//     from the block-template generator
//
// It leaves out /metrics, the page and chunked request bodies.
//
// Settings: PORT, THREADS (default GOMAXPROCS), PIN=1 (one core for each
// loop), SPIN=1 (epoll_wait with timeout 0), LOG=1 (the request log of the
// service, in batches), CACHE=0 (no response cache), SNDBUF (bytes).
package main

import (
	"log"
	"os"
	"runtime"
	"strconv"
	"sync/atomic"
	"time"

	"fizzbuzz/internal/fizzbuzz"
)

type config struct {
	port                           int
	threads                        int
	pin, spin, log, cache          bool
	sndbuf                         int
	limits                         fizzbuzz.Limits
	maxInflight                    int64
	maxHeader                      int
	writeTimeout, idle, readHeader time.Duration
}

type server struct {
	cfg      config
	shards   []*shard
	inflight atomic.Int64
}

func envInt(k string, def int) int {
	if v, err := strconv.Atoi(os.Getenv(k)); err == nil {
		return v
	}
	return def
}

func main() {
	cfg := defaultConfig()
	cfg.port = envInt("PORT", cfg.port)
	cfg.threads = envInt("THREADS", cfg.threads)
	cfg.pin = os.Getenv("PIN") == "1"
	cfg.spin = os.Getenv("SPIN") == "1"
	cfg.log = os.Getenv("LOG") == "1"
	cfg.cache = os.Getenv("CACHE") != "0"
	cfg.sndbuf = envInt("SNDBUF", 0)
	if _, err := start(cfg); err != nil {
		log.Fatal(err)
	}
	log.Printf("rover listening on :%d with %d loops", cfg.port, cfg.threads)
	select {}
}

func start(cfg config) (*server, error) {
	s := &server{cfg: cfg}
	b := &budget{}
	b.max.Store(64 << 20)
	cpus := allowedCPUs()
	for i := range cfg.threads {
		sh := &shard{m: map[statKey]*atomic.Uint64{}, budget: b}
		s.shards = append(s.shards, sh)
		l, err := newLoop(s, i, sh)
		if err != nil {
			return nil, err
		}
		cpu := -1
		if cfg.pin && len(cpus) > 0 {
			cpu = cpus[i%len(cpus)]
		}
		go l.run(cpu)
	}
	return s, nil
}

func defaultConfig() config {
	return config{
		port:         8080,
		threads:      runtime.GOMAXPROCS(0),
		cache:        true,
		limits:       fizzbuzz.DefaultLimits,
		maxInflight:  1 << 30,
		maxHeader:    16 << 10,
		writeTimeout: 10 * time.Second,
		idle:         120 * time.Second,
		readHeader:   5 * time.Second,
	}
}
