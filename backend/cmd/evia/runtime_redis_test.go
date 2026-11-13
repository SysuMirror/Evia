package main

import (
	"reflect"
	"testing"
	"time"

	"github.com/howtomakeaname/Evia/backend/internal/platform/admissionstats"
	"github.com/howtomakeaname/Evia/backend/internal/platform/budget"
)

func TestAdmissionStatsConfigForwardsSentinel(t *testing.T) {
	addrs := []string{"127.0.0.1:26379", "127.0.0.1:26380", "127.0.0.1:26381"}
	b := budget.Config{
		URL:                "",
		Timeout:            200 * time.Millisecond,
		PoolSize:           32,
		AllowInsecureLocal: true,
		Sentinel: budget.SentinelConfig{
			MasterName:       "evia-redis",
			Addrs:            addrs,
			Password:         "data-secret",
			SentinelPassword: "sentinel-secret",
			TLSEnabled:       false,
		},
	}
	cfg := admissionStatsConfig(b)
	want := admissionstats.SentinelConfig{
		MasterName:       "evia-redis",
		Addrs:            addrs,
		Password:         "data-secret",
		SentinelPassword: "sentinel-secret",
		TLSEnabled:       false,
	}
	if !reflect.DeepEqual(cfg.Sentinel, want) || cfg.URL != "" || !cfg.AllowInsecureLocal {
		t.Fatalf("sentinel topology not forwarded: %+v", cfg)
	}
	// The derived config must not share mutable state with the budget config.
	b.Sentinel.Addrs[0] = "changed:1"
	if cfg.Sentinel.Addrs[0] != "127.0.0.1:26379" {
		t.Fatal("admission stats config aliases the budget sentinel address slice")
	}
}

func TestAdmissionStatsConfigKeepsURLPath(t *testing.T) {
	b := budget.DefaultConfig("redis://127.0.0.1:16379/0")
	b.AllowInsecureLocal = true
	cfg := admissionStatsConfig(b)
	if cfg.URL != b.URL || cfg.Sentinel.MasterName != "" || len(cfg.Sentinel.Addrs) != 0 {
		t.Fatalf("single-endpoint path must remain unchanged: %+v", cfg)
	}
}
