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

const sentinelTestProject = "a3b09cf1-4ec3-4d54-a9c8-a18570deba40"

func realSentinelLimiter(t *testing.T, burst int, rate float64) (*Limiter, Config) {
	t.Helper()
	rawAddrs := os.Getenv("EVIA_TEST_REDIS_SENTINEL_ADDRS")
	master := os.Getenv("EVIA_TEST_REDIS_SENTINEL_MASTER")
	if rawAddrs == "" || master == "" {
		t.Fatal("EVIA_TEST_REDIS_SENTINEL_ADDRS and EVIA_TEST_REDIS_SENTINEL_MASTER are required for sentinel integration tests")
	}
	c := DefaultConfig("")
	c.AllowInsecureLocal = true
	c.KeyPrefix = "evia:test:budget:" + strings.ReplaceAll(ids.New(), "-", "")
	// The victim keeps refill below one event cost across the drill window, so
	// the burst behaves as a fixed, exactly countable token inventory.
	c.Default = Policy{Rate: rate, Burst: burst}
	c.Timeout, c.PoolSize = time.Second, 16
	c.Sentinel = SentinelConfig{MasterName: master, Addrs: strings.Split(rawAddrs, ",")}
	limiter, err := New(c)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = limiter.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := limiter.Ping(ctx); err != nil {
		t.Fatalf("sentinel redis ping: %v", err)
	}
	return limiter, c
}

func TestSentinelBaselineReserve(t *testing.T) {
	limiter, c := realSentinelLimiter(t, 50, 0.001)
	ctx := context.Background()
	for i := 0; i < 2; i++ {
		if err := limiter.ReserveCollection(ctx, sentinelTestProject, "production", 1); err != nil {
			t.Fatalf("reserve %d through sentinel failed: %v", i, err)
		}
	}
	scope := Scope{ProjectID: sentinelTestProject, Environment: "production"}
	values, err := limiter.client.HGetAll(ctx, limiter.key(scope)).Result()
	if err != nil {
		t.Fatal(err)
	}
	tokens, err := strconv.ParseInt(values["tokens"], 10, 64)
	if err != nil {
		t.Fatal(err)
	}
	// Floor-rate refill between the two reserves adds a handful of units; a
	// missing or duplicated debit would move the balance by a full event cost,
	// so the tight interval below still proves exactly two charges.
	lower := int64(48) * tokenUnits
	upper := lower + 5000
	if tokens < lower || tokens > upper {
		t.Fatalf("bucket tokens = %d outside two-debit band [%d, %d]", tokens, lower, upper)
	}
	if err := limiter.client.Del(ctx, limiter.key(scope)).Err(); err != nil {
		t.Fatal(err)
	}
	_ = c
}

// TestSentinelVictimLoop drives a fixed-burst limiter from one long-lived
// process while the drill stops the master externally. The JSONL log lets the
// drill prove fail-closed behavior and same-process recovery; the final bucket
// read proves no debit was ever applied twice.
func TestSentinelVictimLoop(t *testing.T) {
	if os.Getenv("EVIA_TEST_SENTINEL_VICTIM") != "1" {
		t.Skip("sentinel victim loop requires EVIA_TEST_SENTINEL_VICTIM=1")
	}
	logPath := os.Getenv("EVIA_TEST_SENTINEL_VICTIM_LOG")
	if logPath == "" {
		t.Fatal("EVIA_TEST_SENTINEL_VICTIM_LOG is required")
	}
	burst := 1000
	if raw := os.Getenv("EVIA_TEST_SENTINEL_VICTIM_BURST"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		// With the default rate, 1500 events still fully refill within 24h,
		// the policy normalize() ceiling.
		if err != nil || parsed < 10 || parsed > 1500 {
			t.Fatal("EVIA_TEST_SENTINEL_VICTIM_BURST must be between 10 and 1500")
		}
		burst = parsed
	}
	// 0.02 events/s refunds at most 0.9 event across the 45s window, so the
	// inventory can never fund an extra accepted reserve beyond the burst.
	rate := 0.02
	if raw := os.Getenv("EVIA_TEST_SENTINEL_VICTIM_RATE"); raw != "" {
		parsed, err := strconv.ParseFloat(raw, 64)
		if err != nil || parsed < 0.001 || parsed > 1_000_000 {
			t.Fatal("EVIA_TEST_SENTINEL_VICTIM_RATE must be between 0.001 and 1000000")
		}
		rate = parsed
	}
	duration := 45 * time.Second
	if raw := os.Getenv("EVIA_TEST_SENTINEL_VICTIM_DURATION"); raw != "" {
		parsed, err := time.ParseDuration(raw)
		if err != nil || parsed < 5*time.Second || parsed > 5*time.Minute {
			t.Fatal("EVIA_TEST_SENTINEL_VICTIM_DURATION must be between 5s and 5m")
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

	limiter, _ := realSentinelLimiter(t, burst, rate)
	deadline := time.Now().Add(duration)
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	ok, failures := 0, 0
	for time.Now().Before(deadline) {
		<-ticker.C
		err := limiter.ReserveCollection(context.Background(), sentinelTestProject, "production", 1)
		event := map[string]string{}
		if err == nil {
			ok++
			event["result"] = "ok"
		} else {
			failures++
			event["result"] = "error"
			var faultErr *fault.Error
			if errors.As(err, &faultErr) {
				event["code"] = faultErr.Code
			}
		}
		writeEvent(event)
	}

	// The same process must recover after Sentinel promotes the replica.
	recoveryErr := limiter.ReserveCollection(context.Background(), sentinelTestProject, "production", 1)
	recovery := map[string]string{"phase": "recovery", "result": "ok"}
	if recoveryErr != nil {
		recovery["result"] = "error"
	}
	writeEvent(recovery)
	if recoveryErr != nil {
		t.Fatalf("limiter did not recover after failover: %v", recoveryErr)
	}
	ok++

	scope := Scope{ProjectID: sentinelTestProject, Environment: "production"}
	values, err := limiter.client.HGetAll(context.Background(), limiter.key(scope)).Result()
	if err != nil {
		t.Fatal(err)
	}
	tokens, err := strconv.ParseInt(values["tokens"], 10, 64)
	if err != nil {
		t.Fatal(err)
	}
	capacity, err := strconv.ParseInt(values["capacity"], 10, 64)
	if err != nil {
		t.Fatal(err)
	}
	refillPerMS := int64(rate * 1000)
	writeEvent(map[string]string{
		"type":          "summary",
		"ok":            strconv.Itoa(ok),
		"errors":        strconv.Itoa(failures),
		"tokens":        strconv.FormatInt(tokens, 10),
		"capacity":      strconv.FormatInt(capacity, 10),
		"refill_per_ms": strconv.FormatInt(refillPerMS, 10),
		"key":           limiter.key(scope),
	})

	if ok > burst {
		t.Fatalf("accepted %d reserves beyond burst %d: ambiguous debits were replayed", ok, burst)
	}
	if capacity != int64(burst)*tokenUnits {
		t.Fatalf("capacity = %d, want %d", capacity, int64(burst)*tokenUnits)
	}
	// Refill adds at most refillPerMS token-units per millisecond of the
	// window. A replayed debit would shift the balance by a full event cost,
	// orders of magnitude beyond that bound, so the band is an exact
	// no-double-spend proof.
	consumed := capacity - tokens
	maxRefill := int64(duration/time.Millisecond) * refillPerMS
	lower := int64(ok)*tokenUnits - maxRefill
	upper := int64(ok) * tokenUnits
	if consumed < lower || consumed > upper {
		t.Fatalf("consumed units = %d outside exact debit band [%d, %d]", consumed, lower, upper)
	}
}
