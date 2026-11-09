//go:build integration

package analysis

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/howtomakeaname/Evia/backend/internal/platform/ids"
)

func compactMigrationStatements(t *testing.T) []string {
	t.Helper()
	data, err := os.ReadFile("../../../deploy/clickhouse/004_compact_winner_columns.sql")
	if err != nil {
		t.Fatal(err)
	}
	lines := []string{}
	for _, line := range strings.Split(string(data), "\n") {
		if !strings.HasPrefix(strings.TrimSpace(line), "--") {
			lines = append(lines, line)
		}
	}
	statements := []string{}
	for _, statement := range strings.Split(strings.Join(lines, "\n"), ";") {
		if strings.TrimSpace(statement) != "" {
			statements = append(statements, statement)
		}
	}
	return statements
}

func compactCommand(t *testing.T, client *ClickHouse, query string) (int, string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	response, err := client.request(ctx, query, "evia-compact-test-"+ids.New(), nil, false)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, 1024*1024))
	if err != nil {
		t.Fatal(err)
	}
	return response.StatusCode, string(body)
}

func removeCompactLayout(t *testing.T, client *ClickHouse) {
	t.Helper()
	chExec(t, client, "ALTER TABLE event_facts_raw DROP COLUMN canonical_hash_bytes, DROP COLUMN occurred_at_effective, DROP COLUMN event_time_effective, DROP CONSTRAINT canonical_hash_lower_hex")
}

func TestClickHouseCompactWinnerMigrationAndBackfill(t *testing.T) {
	client := clickHouseDatabase(t)
	removeCompactLayout(t, client)
	legacy := fact("legacy", "opened", "legacy", "", "source-one", 1, nil)
	oldWriterNulls := fact("old-null", "opened", "", "", "source-one", 2, nil)
	oldWriterNulls["occurred_at_precise"], oldWriterNulls["event_time_precise"] = nil, nil
	seedRows(t, client, []map[string]any{legacy, oldWriterNulls})
	for _, statement := range compactMigrationStatements(t) {
		chExec(t, client, statement)
	}
	assertValues := func(want string) {
		t.Helper()
		got := string(chExec(t, client, `SELECT count(), countIf(
 canonical_hash_bytes != unhex(canonical_hash)
 OR occurred_at_effective != coalesce(occurred_at_precise, toDateTime64(occurred_at, 9, 'UTC'))
 OR event_time_effective != coalesce(event_time_precise, toDateTime64(event_time, 9, 'UTC')))
 FROM event_facts_raw FORMAT TSV`))
		if got != want+"\t0\n" {
			t.Fatal("compact columns differ from original payload", got)
		}
	}
	// These parts predate the columns. Defaults must work without rewriting them.
	assertValues("2")
	precise := fact("precise", "opened", "precise", "", "source-one", 3, nil)
	precise["occurred_at_precise"] = "2026-11-07 10:00:03.000000001"
	precise["event_time_precise"] = "2026-11-07 10:00:03.000000002"
	seedRows(t, client, []map[string]any{precise})
	assertValues("3")
	// New writers and omitted/null legacy fields share the original insert path.
	seedRows(t, client, []map[string]any{legacy, oldWriterNulls})
	assertValues("5")
	for _, statement := range compactMigrationStatements(t) {
		chExec(t, client, statement)
	}
	for _, column := range []string{"canonical_hash_bytes", "occurred_at_effective", "event_time_effective"} {
		chExec(t, client, "ALTER TABLE event_facts_raw MATERIALIZE COLUMN "+column+" IN PARTITION ID '202611' SETTINGS mutations_sync=1")
	}
	assertValues("5")
	if got := string(chExec(t, client, "SELECT count() FROM system.mutations WHERE database=currentDatabase() AND table='event_facts_raw' AND NOT is_done FORMAT TSV")); got != "0\n" {
		t.Fatal("compact backfill is unfinished", got)
	}
	// Comparison must be unsigned bytewise order, including byte 0x00/0x80/0xff.
	if got := string(chExec(t, client, `WITH ['09ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff',
 '0a00000000000000000000000000000000000000000000000000000000000000',
 '7fffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff',
 '8000000000000000000000000000000000000000000000000000000000000000',
 'ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff',
 '0000000000000000000000000000000000000000000000000000000000000000'] AS hashes
 SELECT arraySort(hashes) = arrayMap(value -> lower(hex(value)), arraySort(arrayMap(value -> toFixedString(unhex(value), 32), hashes))) FORMAT TSV`)); got != "1\n" {
		t.Fatal("packed hash changes lexical ordering", got)
	}
	for _, hash := range []string{strings.Repeat("A", 64), strings.Repeat("g", 64), strings.Repeat("a", 63)} {
		invalid := fact("invalid", "opened", "", "", "source-one", 4, nil)
		invalid["canonical_hash"] = hash
		encoded, err := json.Marshal(invalid)
		if err != nil {
			t.Fatal(err)
		}
		status, body := compactCommand(t, client, "INSERT INTO event_facts_raw FORMAT JSONEachRow\n"+string(encoded))
		if status == http.StatusOK || !strings.Contains(body, "VIOLATED_CONSTRAINT") {
			t.Fatalf("invalid hash was not rejected by future-insert constraint: status=%d body=%s", status, body)
		}
	}
	assertValues("5")
}

func TestClickHouseCompactWinnerRejectsMalformedHistory(t *testing.T) {
	client := clickHouseDatabase(t)
	removeCompactLayout(t, client)
	invalid := fact("invalid-history", "opened", "", "", "source-one", 1, nil)
	invalid["canonical_hash"] = strings.Repeat("A", 64)
	seedRows(t, client, []map[string]any{invalid})
	statements := compactMigrationStatements(t)
	chExec(t, client, statements[0])
	status, body := compactCommand(t, client, statements[1])
	if status == http.StatusOK || !strings.Contains(body, "canonical hash must contain exactly 64 lowercase hexadecimal bytes") {
		t.Fatalf("malformed history passed compact migration preflight: status=%d body=%s", status, body)
	}
	if got := string(chExec(t, client, "SELECT count() FROM system.columns WHERE database=currentDatabase() AND table='event_facts_raw' AND name IN ('canonical_hash_bytes','occurred_at_effective','event_time_effective') FORMAT TSV")); got != "0\n" {
		t.Fatal("compact columns were created despite failed preflight", got)
	}
}
