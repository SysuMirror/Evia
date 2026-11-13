//go:build integration

package postgres

import (
	"context"
	"encoding/json"
	"os"
	"strconv"
	"testing"
	"time"
)

const failoverTable = "evia_ha_drill"

func failoverDSN(t *testing.T) string {
	t.Helper()
	dsn := os.Getenv("EVIA_TEST_PG_FAILOVER_DSN")
	if dsn == "" {
		t.Fatal("EVIA_TEST_PG_FAILOVER_DSN is required for PostgreSQL failover integration tests")
	}
	return dsn
}

// TestFailoverBaselineWrite proves the multi-host DSN reaches the read-write
// primary through the production Open path while a standby is in recovery.
func TestFailoverBaselineWrite(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, failoverDSN(t))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Pool.Close()

	if _, err = store.Pool.Exec(ctx, "DROP TABLE IF EXISTS "+failoverTable); err != nil {
		t.Fatal(err)
	}
	if _, err = store.Pool.Exec(ctx, "CREATE TABLE "+failoverTable+" (seq bigint PRIMARY KEY, committed_at timestamptz NOT NULL DEFAULT now())"); err != nil {
		t.Fatal(err)
	}
	if _, err = store.Pool.Exec(ctx, "INSERT INTO "+failoverTable+" (seq) VALUES (1), (2)"); err != nil {
		t.Fatal(err)
	}
	var count, maxSeq int
	if err = store.Pool.QueryRow(ctx, "SELECT count(*), max(seq) FROM "+failoverTable).Scan(&count, &maxSeq); err != nil {
		t.Fatal(err)
	}
	if count != 2 || maxSeq != 2 {
		t.Fatalf("multi-host baseline write mismatch: count=%d max=%d", count, maxSeq)
	}
	var inRecovery bool
	if err = store.Pool.QueryRow(ctx, "SELECT pg_is_in_recovery()").Scan(&inRecovery); err != nil {
		t.Fatal(err)
	}
	if inRecovery {
		t.Fatal("target_session_attrs=read-write connected to a recovery standby")
	}
}

// TestFailoverVictimLoop keeps inserting monotonic sequences through one
// long-lived pool while the drill stops the primary and promotes the standby.
// Every failed attempt retries the same sequence, so a surviving committed
// set must be exactly 1..N with no gaps or duplicates.
func TestFailoverVictimLoop(t *testing.T) {
	if os.Getenv("EVIA_TEST_PG_VICTIM") != "1" {
		t.Skip("PostgreSQL failover victim requires EVIA_TEST_PG_VICTIM=1")
	}
	logPath := os.Getenv("EVIA_TEST_PG_VICTIM_LOG")
	if logPath == "" {
		t.Fatal("EVIA_TEST_PG_VICTIM_LOG is required")
	}
	duration := 45 * time.Second
	if raw := os.Getenv("EVIA_TEST_PG_VICTIM_DURATION"); raw != "" {
		parsed, err := time.ParseDuration(raw)
		if err != nil || parsed < 5*time.Second || parsed > 5*time.Minute {
			t.Fatal("EVIA_TEST_PG_VICTIM_DURATION must be between 5s and 5m")
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

	rootCtx := context.Background()
	store, err := Open(rootCtx, failoverDSN(t))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Pool.Close()

	if _, err = store.Pool.Exec(rootCtx, "DROP TABLE IF EXISTS "+failoverTable); err != nil {
		t.Fatal(err)
	}
	if _, err = store.Pool.Exec(rootCtx, "CREATE TABLE "+failoverTable+" (seq bigint PRIMARY KEY, committed_at timestamptz NOT NULL DEFAULT now())"); err != nil {
		t.Fatal(err)
	}

	deadline := time.Now().Add(duration)
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	seq, ok, failures := 1, 0, 0
	for time.Now().Before(deadline) {
		<-ticker.C
		ctx, cancel := context.WithTimeout(rootCtx, 5*time.Second)
		_, insertErr := store.Pool.Exec(ctx, "INSERT INTO "+failoverTable+" (seq) VALUES ($1)", seq)
		cancel()
		event := map[string]string{"seq": strconv.Itoa(seq)}
		if insertErr == nil {
			event["result"] = "ok"
			ok++
			seq++
		} else {
			event["result"] = "error"
			event["error"] = insertErr.Error()
			failures++
		}
		writeEvent(event)
	}

	// The same pool must accept writes again after the standby is promoted.
	recoveryCtx, cancel := context.WithTimeout(rootCtx, 10*time.Second)
	defer cancel()
	_, recoveryErr := store.Pool.Exec(recoveryCtx, "INSERT INTO "+failoverTable+" (seq) VALUES ($1)", seq)
	recovery := map[string]string{"phase": "recovery", "seq": strconv.Itoa(seq), "result": "ok"}
	if recoveryErr != nil {
		recovery["result"] = "error"
		recovery["error"] = recoveryErr.Error()
	}
	writeEvent(recovery)
	if recoveryErr != nil {
		t.Fatalf("pool did not recover after failover: %v", recoveryErr)
	}
	ok++
	lastSeq := seq

	var count, distinct, maxSeq, gaps int
	if err = store.Pool.QueryRow(rootCtx,
		"SELECT count(*), count(DISTINCT seq), max(seq) FROM "+failoverTable,
	).Scan(&count, &distinct, &maxSeq); err != nil {
		t.Fatal(err)
	}
	if err = store.Pool.QueryRow(rootCtx,
		"SELECT count(*) FROM generate_series(1, $1) AS expected(seq) WHERE expected.seq NOT IN (SELECT seq FROM "+failoverTable+")",
		maxSeq,
	).Scan(&gaps); err != nil {
		t.Fatal(err)
	}
	writeEvent(map[string]string{
		"type":     "summary",
		"ok":       strconv.Itoa(ok),
		"errors":   strconv.Itoa(failures),
		"count":    strconv.Itoa(count),
		"distinct": strconv.Itoa(distinct),
		"max_seq":  strconv.Itoa(maxSeq),
		"gaps":     strconv.Itoa(gaps),
	})

	if count != ok || distinct != ok || maxSeq != lastSeq || gaps != 0 {
		t.Fatalf("committed sequence is not contiguous: count=%d distinct=%d max=%d gaps=%d ok=%d", count, distinct, maxSeq, gaps, ok)
	}
	if failures == 0 {
		t.Fatal("no failure observed while the primary was unavailable")
	}
}
