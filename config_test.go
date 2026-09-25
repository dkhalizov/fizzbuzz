package main

import (
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestLoadConfig(t *testing.T) {
	env := func(kv map[string]string) func(string) string { return func(k string) string { return kv[k] } }

	cfg, err := loadConfig(env(map[string]string{"MAX_LIMIT": "500", "READ_TIMEOUT": "3s"}))
	if err != nil || cfg.Limits.MaxLimit != 500 || cfg.ReadTimeout != 3*time.Second || cfg.Port != "8080" {
		t.Fatalf("overrides: %+v, %v", cfg, err)
	}

	_, err = loadConfig(env(map[string]string{
		"READ_TIMEOUT":       "soon",
		"MAX_INFLIGHT_BYTES": "1",
	}))
	for _, want := range []string{"READ_TIMEOUT", "MAX_INFLIGHT_BYTES must be >="} {
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("error %v does not mention %q", err, want)
		}
	}
	if _, err := loadConfig(env(map[string]string{"STATS_STORE": "redis"})); err == nil {
		t.Error("redis without REDIS_ADDR was accepted")
	}
	if _, err := loadConfig(env(map[string]string{"STATS_FLUSH_INTERVAL": "0s"})); err == nil {
		t.Error("STATS_FLUSH_INTERVAL=0s was accepted") // time.NewTicker panics on it
	}
}

// TestHeaderFitsStrings guards the README reason for MAX_STR_BYTES: a request
// with two strings of the maximum size, each byte percent-encoded, fits in
// MAX_HEADER_BYTES, which is the 16 KB URL limit of Cloudflare.
func TestHeaderFitsStrings(t *testing.T) {
	cfg, _ := loadConfig(func(string) string { return "" })
	s := strings.Repeat("\x00", cfg.Limits.MaxStrBytes) // percent-encodes to 3 bytes each
	q := url.Values{"int1": {"9223372036854775807"}, "int2": {"9223372036854775807"}, "limit": {"10000000"}, "str1": {s}, "str2": {s}}
	line := "QUERY /fizzbuzz?" + q.Encode() + " HTTP/1.1\r\nHost: fizzbuzz.khalizov.com\r\n\r\n"
	if len(line) > cfg.MaxHeaderBytes || cfg.MaxHeaderBytes > 16<<10 {
		t.Errorf("request of %d bytes, MaxHeaderBytes %d", len(line), cfg.MaxHeaderBytes)
	}
}
