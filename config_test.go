package main

import (
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
}
