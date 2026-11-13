//go:build integration

package budget

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/howtomakeaname/Evia/backend/internal/platform/fault"
	"github.com/howtomakeaname/Evia/backend/internal/platform/ids"
)

const clusterTestProject = "a3b09cf1-4ec3-4d54-a9c8-a18570deba40"

func realClusterLimiter(t *testing.T) (*Limiter, Config) {
	t.Helper()
	rawAddrs := os.Getenv("EVIA_TEST_REDIS_CLUSTER_ADDRS")
	if rawAddrs == "" {
		t.Fatal("EVIA_TEST_REDIS_CLUSTER_ADDRS is required for Redis Cluster integration tests")
	}
	c := DefaultConfig("")
	c.AllowInsecureLocal = true
	c.KeyPrefix = "evia:test:cluster:budget:" + strings.ReplaceAll(ids.New(), "-", "")
	// The victim keeps refill below one event cost across the drill window, so
	// the burst behaves as a fixed, exactly countable token inventory.
	c.Default = Policy{Rate: 0.02, Burst: 1000}
	c.Timeout, c.PoolSize = time.Second, 16
	c.Cluster = ClusterConfig{Addrs: strings.Split(rawAddrs, ",")}
	limiter, err := New(c)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = limiter.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := limiter.Ping(ctx); err != nil {
		t.Fatalf("cluster limiter ping: %v", err)
	}
	return limiter, c
}

// TestClusterBaselineReserve proves slot routing reaches the read-write
// master for the hash-tagged bucket and two debits land on that same slot.
func TestClusterBaselineReserve(t *testing.T) {
	ctx := context.Background()
	limiter, _ := realClusterLimiter(t)
	scope := Scope{ProjectID: clusterTestProject, Environment: "production"}
	for i := 0; i < 2; i++ {
		if err := limiter.ReserveCollection(ctx, clusterTestProject, "production", 1); err != nil {
			t.Fatalf("cluster reserve %d failed: %v", i, err)
		}
	}
	values, err := limiter.client.HGetAll(ctx, limiter.key(scope)).Result()
	if err != nil {
		t.Fatal(err)
	}
	tokens, err := strconv.ParseInt(values["tokens"], 10, 64)
	if err != nil {
		t.Fatal(err)
	}
	lower := int64(998) * tokenUnits
	upper := lower + 5000
	if tokens < lower || tokens > upper {
		t.Fatalf("bucket tokens = %d outside two-debit band [%d, %d]", tokens, lower, upper)
	}
}

// TestClusterVictimLoop drives one long-lived ClusterClient while the drill
// stops the master owning the bucket slot. Redis Cluster promotes a replica
// automatically; the client follows MOVED/ASK redirects, while node-level
// retries stay disabled so no ambiguous debit is replayed.
func TestClusterVictimLoop(t *testing.T) {
	if os.Getenv("EVIA_TEST_REDIS_CLUSTER_VICTIM") != "1" {
		t.Skip("Redis Cluster victim requires EVIA_TEST_REDIS_CLUSTER_VICTIM=1")
	}
	logPath := os.Getenv("EVIA_TEST_REDIS_CLUSTER_VICTIM_LOG")
	if logPath == "" {
		t.Fatal("EVIA_TEST_REDIS_CLUSTER_VICTIM_LOG is required")
	}
	duration := 45 * time.Second
	if raw := os.Getenv("EVIA_TEST_REDIS_CLUSTER_VICTIM_DURATION"); raw != "" {
		parsed, err := time.ParseDuration(raw)
		if err != nil || parsed < 5*time.Second || parsed > 5*time.Minute {
			t.Fatal("EVIA_TEST_REDIS_CLUSTER_VICTIM_DURATION must be between 5s and 5m")
		}
		duration = parsed
	}

	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer logFile.Close()
	encoder := json.NewEncoder(logFile)
	writeEvent := func(event map[string]string) {
		event["t"] = time.Now().UTC().Format(time.RFC3339Nano)
		if err := encoder.Encode(event); err != nil {
			t.Fatal(err)
		}
		_ = logFile.Sync()
	}

	limiter, _ := realClusterLimiter(t)
	scope := Scope{ProjectID: clusterTestProject, Environment: "production"}

	deadline := time.Now().Add(duration)
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	ok, failures := 0, 0
	for time.Now().Before(deadline) {
		<-ticker.C
		err := limiter.ReserveCollection(context.Background(), clusterTestProject, "production", 1)
		event := map[string]string{}
		if err == nil {
			event["result"] = "ok"
			ok++
		} else {
			event["result"] = "error"
			event["code"] = "other"
			var faultErr *fault.Error
			if errors.As(err, &faultErr) {
				event["code"] = faultErr.Code
			}
			failures++
		}
		writeEvent(event)
	}

	recoveryErr := limiter.ReserveCollection(context.Background(), clusterTestProject, "production", 1)
	recovery := map[string]string{"phase": "recovery", "result": "ok"}
	if recoveryErr != nil {
		recovery["result"] = "error"
	}
	writeEvent(recovery)
	if recoveryErr != nil {
		t.Fatalf("cluster client did not recover after master failover: %v", recoveryErr)
	}
	ok++

	values, err := limiter.client.HGetAll(context.Background(), limiter.key(scope)).Result()
	if err != nil {
		t.Fatal(err)
	}
	tokens, err := strconv.ParseInt(values["tokens"], 10, 64)
	if err != nil {
		t.Fatal(err)
	}
	writeEvent(map[string]string{
		"type":     "summary",
		"ok":       strconv.Itoa(ok),
		"errors":   strconv.Itoa(failures),
		"tokens":   values["tokens"],
		"capacity": values["capacity"],
		"key":      limiter.key(scope),
	})

	// Refill at 20 units/ms stays below one full event cost over the window,
	// so consumed units must fall inside a band that proves zero replay.
	maxRefill := int64(duration/time.Millisecond) * 20
	consumed := int64(1000)*tokenUnits - tokens
	lower := int64(ok)*tokenUnits - maxRefill
	upper := int64(ok) * tokenUnits
	if consumed < lower || consumed > upper {
		t.Fatalf("consumed units = %d outside exact debit band [%d, %d]", consumed, lower, upper)
	}
	if failures == 0 {
		t.Fatal("no failure observed while the slot master was unavailable")
	}
}
