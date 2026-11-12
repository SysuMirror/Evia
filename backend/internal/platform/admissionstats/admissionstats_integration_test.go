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

func realRecorder(t *testing.T) *Recorder {
	t.Helper()
	endpoint := os.Getenv("EVIA_TEST_REDIS_URL")
	if endpoint == "" {
		t.Fatal("EVIA_TEST_REDIS_URL is required for admission stats integration tests")
	}
	cfg := DefaultConfig(endpoint)
	cfg.AllowInsecureLocal = true
	cfg.KeyPrefix = "evia:test:admission:" + strings.ReplaceAll(ids.New(), "-", "")
	cfg.Timeout, cfg.PoolSize = time.Second, 8
	recorder, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = recorder.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := recorder.Ping(ctx); err != nil {
		t.Fatalf("redis ping: %v", err)
	}
	return recorder
}

func TestRecordAndReadRoundTrip(t *testing.T) {
	recorder := realRecorder(t)
	project := "22345678-1234-1234-1234-123456789abc"
	ctx := context.Background()
	recorder.Record(ctx, project, "production", map[string]int{
		"INVALID_EVENT":   2,
		"EVENT_TOO_LARGE": 1,
		"NOT_A_REASON":    9,
	})
	recorder.Record(ctx, project, "production", map[string]int{"INVALID_EVENT": 3})
	recorder.Record(ctx, project, "staging", map[string]int{"SCHEMA_NOT_READY": 4})

	read, err := recorder.Read(ctx, project, DefaultWindows)
	if err != nil {
		t.Fatal(err)
	}
	if read.ProjectID != project || len(read.Environments) != 3 {
		t.Fatalf("unexpected read envelope: %+v", read)
	}
	totals := map[string]int64{}
	for _, item := range read.Totals {
		totals[item.Reason] = item.Count
	}
	if totals["INVALID_EVENT"] != 5 || totals["EVENT_TOO_LARGE"] != 1 || totals["SCHEMA_NOT_READY"] != 4 {
		t.Fatalf("unexpected totals: %+v", totals)
	}
	if _, ok := totals["NOT_A_REASON"]; ok {
		t.Fatal("unknown rejection reason must be dropped")
	}
	prod := read.Environments[2]
	if prod.Environment != "production" || len(prod.Windows) != 1 {
		t.Fatalf("unexpected production windows: %+v", prod)
	}
	if !prod.Windows[0].BucketAt.Equal(time.Now().UTC().Truncate(time.Hour)) {
		t.Fatalf("window bucket must be the current truncated hour: %+v", prod.Windows[0].BucketAt)
	}
}

func TestReadSeparatesHourlyWindows(t *testing.T) {
	recorder := realRecorder(t)
	project := "32345678-1234-1234-1234-123456789abc"
	fixed := time.Date(2026, 11, 12, 10, 30, 0, 0, time.UTC)
	recorder.now = func() time.Time { return fixed }
	recorder.Record(context.Background(), project, "development", map[string]int{"EVENT_TOO_OLD": 1})
	recorder.now = func() time.Time { return fixed.Add(2 * time.Hour) }
	recorder.Record(context.Background(), project, "development", map[string]int{"EVENT_TOO_OLD": 2})

	read, err := recorder.Read(context.Background(), project, 3)
	if err != nil {
		t.Fatal(err)
	}
	dev := read.Environments[0]
	if dev.Environment != "development" || len(dev.Windows) != 2 {
		t.Fatalf("expected two hourly windows, got %+v", dev)
	}
	if dev.Windows[0].BucketAt.Equal(dev.Windows[1].BucketAt) {
		t.Fatal("windows must be distinct buckets")
	}
	var sum int64
	for _, window := range dev.Windows {
		for _, reason := range window.Reasons {
			sum += reason.Count
		}
	}
	if sum != 3 {
		t.Fatalf("window counts must aggregate to 3, got %d", sum)
	}
}

func TestRecordAppliesRetentionTTL(t *testing.T) {
	recorder := realRecorder(t)
	project := "42345678-1234-1234-1234-123456789abc"
	recorder.Record(context.Background(), project, "production", map[string]int{"INVALID_EVENT": 1})
	key := recorder.key(project, "production", recorder.now())
	ttl, err := recorder.client.TTL(context.Background(), key).Result()
	if err != nil {
		t.Fatal(err)
	}
	if ttl <= 24*time.Hour || ttl > windowRetention {
		t.Fatalf("unexpected key TTL: %s", ttl)
	}
}

func TestRecordIsFailOpenWhenRedisIsUnavailable(t *testing.T) {
	recorder := realRecorder(t)
	project := "52345678-1234-1234-1234-123456789abc"
	if err := recorder.Close(); err != nil {
		t.Fatal(err)
	}
	// Must not panic and must not alter the caller's admission decision.
	recorder.Record(context.Background(), project, "production", map[string]int{"INVALID_EVENT": 1})
	if recorder.DroppedWrites() != 1 {
		t.Fatalf("expected one dropped write, got %d", recorder.DroppedWrites())
	}
}
