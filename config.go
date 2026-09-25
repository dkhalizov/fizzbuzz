package main

import (
	"errors"
	"fmt"
	"strconv"
	"time"

	"fizzbuzz/internal/fizzbuzz"
)

type config struct {
	Port              string
	Limits            fizzbuzz.Limits
	MaxInflightBytes  int64
	WriteChunkTimeout time.Duration
	ReadHeaderTimeout time.Duration
	ReadTimeout       time.Duration
	IdleTimeout       time.Duration
	ShutdownTimeout   time.Duration
	MaxHeaderBytes    int
	StatsStore        string // "memory" or "redis"
	StatsMaxBytes     int64
	RedisAddr         string
	RedisTimeout      time.Duration
}

// loadConfig reads the environment over the defaults and reports every bad
// value at once.
func loadConfig(getenv func(string) string) (config, error) {
	c := config{
		Port:              "8080",
		Limits:            fizzbuzz.DefaultLimits,
		MaxInflightBytes:  1 << 30,
		WriteChunkTimeout: 10 * time.Second,
		ReadHeaderTimeout: 5 * time.Second,
		// QUERY reads a body. Also, net/http reads up to 256 KiB of an unread
		// body before it responds.
		ReadTimeout:     10 * time.Second,
		IdleTimeout:     120 * time.Second,
		ShutdownTimeout: 25 * time.Second, // under the 30 s Kubernetes grace period
		MaxHeaderBytes:  16 << 10,         // two percent-encoded 1 KiB strings fit
		StatsStore:      "memory",
		StatsMaxBytes:   64 << 20,
		RedisTimeout:    100 * time.Millisecond,
	}
	var errs []error
	set := func(key string, parse func(string) error) {
		if v := getenv(key); v != "" {
			if err := parse(v); err != nil {
				errs = append(errs, fmt.Errorf("%s=%q: %w", key, v, err))
			}
		}
	}
	str := func(dst *string) func(string) error { return func(v string) error { *dst = v; return nil } }
	i64 := func(dst *int64) func(string) error {
		return func(v string) (err error) { *dst, err = strconv.ParseInt(v, 10, 64); return err }
	}
	num := func(dst *int) func(string) error {
		return func(v string) (err error) { *dst, err = strconv.Atoi(v); return err }
	}
	dur := func(dst *time.Duration) func(string) error {
		return func(v string) (err error) { *dst, err = time.ParseDuration(v); return err }
	}

	set("PORT", str(&c.Port))
	set("MAX_LIMIT", num(&c.Limits.MaxLimit))
	set("MAX_STR_BYTES", num(&c.Limits.MaxStrBytes))
	set("MAX_RESPONSE_BYTES", i64(&c.Limits.MaxResponseBytes))
	set("MAX_INFLIGHT_BYTES", i64(&c.MaxInflightBytes))
	set("WRITE_CHUNK_TIMEOUT", dur(&c.WriteChunkTimeout))
	set("READ_HEADER_TIMEOUT", dur(&c.ReadHeaderTimeout))
	set("READ_TIMEOUT", dur(&c.ReadTimeout))
	set("IDLE_TIMEOUT", dur(&c.IdleTimeout))
	set("SHUTDOWN_TIMEOUT", dur(&c.ShutdownTimeout))
	set("MAX_HEADER_BYTES", num(&c.MaxHeaderBytes))
	set("STATS_STORE", str(&c.StatsStore))
	set("STATS_MAX_BYTES", i64(&c.StatsMaxBytes))
	set("REDIS_ADDR", str(&c.RedisAddr))
	set("REDIS_TIMEOUT", dur(&c.RedisTimeout))

	switch {
	case c.MaxInflightBytes < c.Limits.MaxResponseBytes:
		errs = append(errs, errors.New("MAX_INFLIGHT_BYTES must be >= MAX_RESPONSE_BYTES, or the largest response could never run"))
	case c.StatsStore != "memory" && c.StatsStore != "redis":
		errs = append(errs, fmt.Errorf("STATS_STORE=%q: must be memory or redis", c.StatsStore))
	case c.StatsStore == "redis" && c.RedisAddr == "":
		errs = append(errs, errors.New("REDIS_ADDR is required when STATS_STORE=redis"))
	}
	return c, errors.Join(errs...)
}
