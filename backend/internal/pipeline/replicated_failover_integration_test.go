//go:build integration

package pipeline

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/howtomakeaname/Evia/backend/internal/ingestion"
	"github.com/howtomakeaname/Evia/backend/internal/platform/ids"
)

// The replicated drill uses an isolated topology (deploy/compose.ha-clickhouse.yaml)
// fronted by a TCP failover proxy, so these tests never touch the single-node
// development ClickHouse used by the other pipeline integration tests.
func haEnv(t *testing.T) (lb, node1, node2, user, password string) {
	t.Helper()
	lb = os.Getenv("EVIA_TEST_CH_HA_LB_URL")
	node1 = os.Getenv("EVIA_TEST_CH_HA_NODE1_URL")
	node2 = os.Getenv("EVIA_TEST_CH_HA_NODE2_URL")
	user = os.Getenv("EVIA_TEST_CH_HA_USERNAME")
	password = os.Getenv("EVIA_TEST_CH_HA_PASSWORD")
	if lb == "" && node1 == "" && node2 == "" && user == "" {
		t.Skip("ClickHouse HA topology env vars are unset; skipping HA integration test")
	}
	if lb == "" || node1 == "" || node2 == "" || user == "" {
		t.Fatal("EVIA_TEST_CH_HA_LB_URL, NODE1_URL, NODE2_URL and USERNAME are required for ClickHouse HA tests")
	}
	return lb, node1, node2, user, password
}

func haConfig(url, database, user, password string) ClickHouseConfig {
	return ClickHouseConfig{
		URL: url, Database: database, Username: user, Password: password,
		Client: &http.Client{Timeout: 15 * time.Second},
	}
}

func applyReplicatedDDL(t *testing.T, ctx context.Context, url, database, user, password string) {
	t.Helper()
	path, err := filepath.Abs("../../../deploy/clickhouse-ha/001_replicated_event_facts.sql")
	if err != nil {
		t.Fatal(err)
	}
	ddl, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	cfg := haConfig(url, database, user, password)
	var lines []string
	for _, line := range strings.Split(string(ddl), "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "--") {
			continue
		}
		lines = append(lines, line)
	}
	for _, statement := range strings.Split(strings.Join(lines, "\n"), ";") {
		statement = strings.TrimSpace(statement)
		if statement == "" {
			continue
		}
		if _, err := clickhouseQuery(ctx, cfg, statement); err != nil {
			t.Fatalf("replicated DDL failed on %s: %v", url, err)
		}
	}
}

func haSetup(t *testing.T, dropOnCleanup bool) (ClickHouseConfig, string) {
	t.Helper()
	lb, node1, node2, user, password := haEnv(t)
	database := "evia_ha_" + strings.ReplaceAll(ids.New(), "-", "")
	ctx := context.Background()
	for _, url := range []string{node1, node2} {
		cfg := haConfig(url, database, user, password)
		if _, err := clickhouseQuery(ctx, cfg, "CREATE DATABASE "+database); err != nil {
			t.Fatal(err)
		}
		applyReplicatedDDL(t, ctx, url, database, user, password)
	}
	if dropOnCleanup {
		t.Cleanup(func() {
			cleanup, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			for _, url := range []string{node1, node2} {
				cfg := haConfig(url, database, user, password)
				_, _ = clickhouseQuery(cleanup, cfg, "DROP DATABASE "+database+" SYNC")
			}
		})
	}
	return haConfig(lb, database, user, password), database
}

func haCount(t *testing.T, ctx context.Context, cfg ClickHouseConfig) int {
	t.Helper()
	body, err := clickhouseQuery(ctx, cfg, "SELECT count() AS n FROM event_facts_raw FORMAT JSONEachRow")
	if err != nil {
		t.Fatal(err)
	}
	var out struct {
		N json.Number `json:"n"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatal(err, string(body))
	}
	n, err := out.N.Int64()
	if err != nil {
		t.Fatal(err)
	}
	return int(n)
}

func haRecord(seq int) ingestion.Record {
	now := time.Date(2026, 11, 13, 4, 0, 0, 0, time.UTC).Add(time.Duration(seq) * time.Second)
	record := testRecord()
	// Keep the sink's UUID contract while embedding the sequence in the final
	// 12 hex digits so every attempt stays uniquely identifiable in logs.
	record.EventID = "00000000-0000-4000-8000-" + fmt.Sprintf("%012x", seq)
	record.OccurredAt = now
	record.ReceivedAt = now
	return record
}

// TestReplicatedSinkBaseline proves the production sink inserts unchanged
// payloads through the proxy into a ReplicatedMergeTree table and that both
// keeper-coordinated replicas converge to identical contents.
func TestReplicatedSinkBaseline(t *testing.T) {
	ctx := context.Background()
	cfg, database := haSetup(t, true)
	sink, err := NewClickHouse(cfg)
	if err != nil {
		t.Fatal(err)
	}
	records := []ingestion.Record{haRecord(1), haRecord(2), haRecord(3)}
	if err := sink.Write(ctx, records); err != nil {
		t.Fatalf("sink write through HA proxy failed: %v", err)
	}
	_, node1, node2, user, password := haEnv(t)
	deadline := time.Now().Add(30 * time.Second)
	var c1, c2 int
	for time.Now().Before(deadline) {
		c1 = haCount(t, ctx, haConfig(node1, database, user, password))
		c2 = haCount(t, ctx, haConfig(node2, database, user, password))
		if c1 == len(records) && c2 == len(records) {
			break
		}
		time.Sleep(time.Second)
	}
	if c1 != len(records) || c2 != len(records) {
		t.Fatalf("replicas did not converge: node1=%d node2=%d want=%d", c1, c2, len(records))
	}
	// The read view must resolve identically through the proxy and per node.
	proxyCount := haCount(t, ctx, cfg)
	if proxyCount != len(records) {
		t.Fatalf("proxy read count = %d, want %d", proxyCount, len(records))
	}
}

// TestReplicatedSinkFailoverVictim keeps inserting through one long-lived
// sink while the drill stops one ClickHouse node. Failed batches retry with
// the same event identity; after recovery both replicas must hold exactly the
// successful event set with no gaps or duplicates.
func TestReplicatedSinkFailoverVictim(t *testing.T) {
	if os.Getenv("EVIA_TEST_CH_HA_VICTIM") != "1" {
		t.Skip("ClickHouse HA victim requires EVIA_TEST_CH_HA_VICTIM=1")
	}
	logPath := os.Getenv("EVIA_TEST_CH_HA_VICTIM_LOG")
	if logPath == "" {
		t.Fatal("EVIA_TEST_CH_HA_VICTIM_LOG is required")
	}
	duration := 45 * time.Second
	if raw := os.Getenv("EVIA_TEST_CH_HA_VICTIM_DURATION"); raw != "" {
		parsed, err := time.ParseDuration(raw)
		if err != nil || parsed < 5*time.Second || parsed > 5*time.Minute {
			t.Fatal("EVIA_TEST_CH_HA_VICTIM_DURATION must be between 5s and 5m")
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

	ctx := context.Background()
	cfg, database := haSetup(t, false)
	sink, err := NewClickHouse(cfg)
	if err != nil {
		t.Fatal(err)
	}
	_, _, node2, user, password := haEnv(t)

	deadline := time.Now().Add(duration)
	ticker := time.NewTicker(150 * time.Millisecond)
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

	recoveryCtx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	recoveryErr := sink.Write(recoveryCtx, []ingestion.Record{haRecord(seq)})
	recovery := map[string]string{"phase": "recovery", "seq": strconv.Itoa(seq), "result": "ok"}
	if recoveryErr != nil {
		recovery["result"] = "error"
		recovery["error"] = recoveryErr.Error()
	}
	writeEvent(recovery)
	if recoveryErr != nil {
		t.Fatalf("sink did not recover through the proxy: %v", recoveryErr)
	}
	ok++

	// The drill stops ch-1 for the whole victim window, so only the surviving
	// node can be checked here; two-node convergence is asserted by the drill
	// after the stopped replica reattaches through Keeper.
	waitDeadline := time.Now().Add(45 * time.Second)
	var survivor int
	for time.Now().Before(waitDeadline) {
		survivor = haCount(t, ctx, haConfig(node2, database, user, password))
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
		t.Fatal("no failure observed while a ClickHouse node was stopped")
	}
}
