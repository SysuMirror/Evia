//go:build integration

package admissionstats

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/howtomakeaname/Evia/backend/internal/platform/ids"
)

const sentinelTestProject = "a3b09cf1-4ec3-4d54-a9c8-a18570deba40"

func realSentinelRecorder(t *testing.T) *Recorder {
	t.Helper()
	rawAddrs := os.Getenv("EVIA_TEST_REDIS_SENTINEL_ADDRS")
	master := os.Getenv("EVIA_TEST_REDIS_SENTINEL_MASTER")
	if rawAddrs == "" && master == "" {
		t.Skip("EVIA_TEST_REDIS_SENTINEL_ADDRS and EVIA_TEST_REDIS_SENTINEL_MASTER are required for sentinel integration tests")
	}
	if rawAddrs == "" || master == "" {
		t.Fatal("EVIA_TEST_REDIS_SENTINEL_ADDRS and EVIA_TEST_REDIS_SENTINEL_MASTER are required for sentinel integration tests")
	}
	cfg := DefaultConfig("")
	cfg.AllowInsecureLocal = true
	cfg.KeyPrefix = "evia:test:admission:" + strings.ReplaceAll(ids.New(), "-", "")
	cfg.Timeout, cfg.PoolSize = time.Second, 8
	cfg.Sentinel = SentinelConfig{MasterName: master, Addrs: strings.Split(rawAddrs, ",")}
	recorder, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = recorder.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := recorder.Ping(ctx); err != nil {
		t.Fatalf("sentinel redis ping: %v", err)
	}
	return recorder
}

func TestSentinelBaselineStats(t *testing.T) {
	recorder := realSentinelRecorder(t)
	ctx := context.Background()
	recorder.Record(ctx, sentinelTestProject, "production", map[string]int{
		"INVALID_EVENT":   2,
		"EVENT_TOO_LARGE": 1,
		"UNKNOWN_REASON":  9,
	})
	read, err := recorder.Read(ctx, sentinelTestProject, 1)
	if err != nil {
		t.Fatal(err)
	}
	totals := map[string]int64{}
	for _, entry := range read.Totals {
		totals[entry.Reason] = entry.Count
	}
	if totals["INVALID_EVENT"] != 2 || totals["EVENT_TOO_LARGE"] != 1 {
		t.Fatalf("sentinel-backed counters mismatch: %+v", totals)
	}
	if _, ok := totals["UNKNOWN_REASON"]; ok {
		t.Fatal("reasons outside the bounded vocabulary must be dropped")
	}
	bucket := time.Now().UTC().Truncate(hourWindow)
	key := recorder.key(sentinelTestProject, "production", bucket)
	if err := recorder.client.Del(ctx, key).Err(); err != nil {
		t.Fatal(err)
	}
}
