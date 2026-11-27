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

const clusterTestProject = "a3b09cf1-4ec3-4d54-a9c8-a18570deba40"

func realClusterRecorder(t *testing.T) *Recorder {
	t.Helper()
	rawAddrs := os.Getenv("EVIA_TEST_REDIS_CLUSTER_ADDRS")
	if rawAddrs == "" {
		t.Skip("EVIA_TEST_REDIS_CLUSTER_ADDRS is required for Redis Cluster integration tests")
	}
	cfg := DefaultConfig("")
	cfg.AllowInsecureLocal = true
	cfg.KeyPrefix = "evia:test:cluster:admission:" + strings.ReplaceAll(ids.New(), "-", "")
	cfg.Timeout, cfg.PoolSize = time.Second, 8
	cfg.Cluster = ClusterConfig{Addrs: strings.Split(rawAddrs, ",")}
	recorder, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = recorder.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := recorder.Ping(ctx); err != nil {
		t.Fatalf("cluster redis ping: %v", err)
	}
	return recorder
}

// TestClusterBaselineStats proves the fail-open recorder writes hash-tagged
// counters through Cluster slot routing and reads them back identically.
func TestClusterBaselineStats(t *testing.T) {
	recorder := realClusterRecorder(t)
	ctx := context.Background()
	recorder.Record(ctx, clusterTestProject, "production", map[string]int{
		"INVALID_EVENT":   2,
		"EVENT_TOO_LARGE": 1,
		"UNKNOWN_REASON":  9,
	})
	read, err := recorder.Read(ctx, clusterTestProject, 1)
	if err != nil {
		t.Fatal(err)
	}
	totals := map[string]int64{}
	for _, entry := range read.Totals {
		totals[entry.Reason] = entry.Count
	}
	if totals["INVALID_EVENT"] != 2 || totals["EVENT_TOO_LARGE"] != 1 {
		t.Fatalf("cluster-backed counters mismatch: %+v", totals)
	}
	bucket := recorder.key(clusterTestProject, "production", time.Now().UTC())
	if err := recorder.client.Del(ctx, bucket).Err(); err != nil {
		t.Fatal(err)
	}
}
