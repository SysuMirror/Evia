//go:build integration

package pipeline

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/howtomakeaname/Evia/backend/internal/ingestion"
)

// TestKeeperQuorumSinkVictim drives the production ClickHouse sink against
// the surviving node (ch-1) while the drill removes two of three Keeper
// votes. The sink must fail closed with bounded client timeouts while no
// leader exists, then resume inserts through the same long-lived instance
// after one vote restores the quorum. ReplicatedMergeTree block
// deduplication makes every same-sequence retry idempotent.
func TestKeeperQuorumSinkVictim(t *testing.T) {
	if os.Getenv("EVIA_TEST_CH_KEEPER_VICTIM") != "1" {
		t.Skip("Keeper quorum victim requires EVIA_TEST_CH_KEEPER_VICTIM=1")
	}
	logPath := os.Getenv("EVIA_TEST_CH_KEEPER_VICTIM_LOG")
	if logPath == "" {
		t.Fatal("EVIA_TEST_CH_KEEPER_VICTIM_LOG is required")
	}
	duration := 70 * time.Second
	if raw := os.Getenv("EVIA_TEST_CH_KEEPER_VICTIM_DURATION"); raw != "" {
		parsed, err := time.ParseDuration(raw)
		if err != nil || parsed < 10*time.Second || parsed > 5*time.Minute {
			t.Fatal("EVIA_TEST_CH_KEEPER_VICTIM_DURATION must be between 10s and 5m")
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

	_, node1, _, user, password := haEnv(t)
	_, database := haSetup(t, false)
	// Bounded below the server's 120-second async insert wait so a missing
	// Keeper leader surfaces as a client-side failure within eight seconds.
	cfg := haConfig(node1, database, user, password)
	cfg.Client = &http.Client{Timeout: 8 * time.Second}
	sink, err := NewClickHouse(cfg)
	if err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	deadline := time.Now().Add(duration)
	ticker := time.NewTicker(300 * time.Millisecond)
	defer ticker.Stop()
	seq, ok, failures := 1, 0, 0
	for time.Now().Before(deadline) {
		<-ticker.C
		writeErr := sink.Write(ctx, []ingestion.Record{haRecord(seq)})
		event := map[string]string{"seq": strconv.Itoa(seq)}
		if writeErr == nil {
			event["result"] = "ok"
			ok++
			seq++
		} else {
			event["result"] = "error"
			event["error"] = writeErr.Error()
			failures++
		}
		writeEvent(event)
	}

	recoveryCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	recoveryErr := sink.Write(recoveryCtx, []ingestion.Record{haRecord(seq)})
	recovery := map[string]string{"phase": "recovery", "seq": strconv.Itoa(seq), "result": "ok"}
	if recoveryErr != nil {
		recovery["result"] = "error"
		recovery["error"] = recoveryErr.Error()
	}
	writeEvent(recovery)
	if recoveryErr != nil {
		t.Fatalf("sink did not recover after the quorum returned: %v", recoveryErr)
	}
	ok++

	// ch-1 stays up for the whole drill, so its replica is the survivor
	// cross-check; two-node convergence is asserted by the drill after ch-2
	// reattaches through Keeper.
	waitDeadline := time.Now().Add(60 * time.Second)
	var survivor int
	for time.Now().Before(waitDeadline) {
		survivor = haCount(t, ctx, haConfig(node1, database, user, password))
		if survivor == ok {
			break
		}
		time.Sleep(time.Second)
	}
	writeEvent(map[string]string{
		"type":     "summary",
		"ok":       strconv.Itoa(ok),
		"errors":   strconv.Itoa(failures),
		"survivor": strconv.Itoa(survivor),
		"database": database,
	})
	if survivor != ok {
		t.Fatalf("surviving replica count = %d, successes = %d", survivor, ok)
	}
	if failures == 0 {
		t.Fatal("no failure observed while the Keeper quorum was down")
	}
}
