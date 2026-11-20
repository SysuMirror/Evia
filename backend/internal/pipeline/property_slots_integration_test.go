//go:build integration

package pipeline

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/howtomakeaname/Evia/backend/internal/propertycatalog"
)

func slotsCatalog(t *testing.T) *propertycatalog.Catalog {
	t.Helper()
	raw := `{
  "revision": 1,
  "entries": [
    {"event_key": "bench_visit", "schema_version": 1, "property_key": "section", "resolved_type": "string"},
    {"event_key": "bench_visit", "schema_version": 1, "property_key": "duration_ms", "resolved_type": "number"}
  ]
}`
	catalog, e := propertycatalog.ParseConfig([]byte(raw))
	if e != nil {
		t.Fatal(e)
	}
	return catalog
}

func insertSlotRows(t *testing.T, c ClickHouseConfig) {
	t.Helper()
	row := func(id, section string, duration any, tokens map[string]string) string {
		encoded, e := json.Marshal(map[string]any{
			"source_kind": "browser", "project_id": "p1", "environment": "development",
			"source_id": "s1", "event_id": id, "event_key": "bench_visit", "schema_version": 1,
			"release_revision": 1,
			"occurred_at":      "2026-11-20 05:00:00.000", "event_time": "2026-11-20 05:00:00.000",
			"received_at": "2026-11-20 05:00:01.000000000", "canonical_hash": strings.Repeat("a", 64),
			"sdk_name": "synthetic", "sdk_version": "1", "context_json": "{}",
			"property_layout_version": 1, "property_tokens": tokens,
		})
		if e != nil {
			t.Fatal(e)
		}
		_ = duration
		return string(encoded)
	}
	sectionColumn := propertycatalog.SlotColumn("section")
	durationColumn := propertycatalog.SlotColumn("duration_ms")
	body := strings.Join([]string{
		row("00000000-0000-4000-8000-0000000000a1", "ignored", nil, map[string]string{
			sectionColumn: `"section-3"`, durationColumn: "950",
		}),
		// A row without the derived tokens stays on the document path.
		row("00000000-0000-4000-8000-0000000000a2", "ignored", nil, map[string]string{}),
	}, "\n")
	if _, e := clickhouseQuery(context.Background(), c, "INSERT INTO event_facts_raw FORMAT JSONEachRow\n"+body); e != nil {
		t.Fatal(e)
	}
}

func slotsQuery(t *testing.T, c ClickHouseConfig, sql string) string {
	t.Helper()
	body, e := clickhouseQuery(context.Background(), c, sql)
	if e != nil {
		t.Fatal(e)
	}
	return strings.TrimSpace(string(body))
}

func TestPropertySlotExecutorAppliesAndBackfills(t *testing.T) {
	c := erasureDatabase(t)
	insertSlotRows(t, c)
	executor, e := NewPropertySlotExecutor(c, nil, slotsCatalog(t))
	if e != nil {
		t.Fatal(e)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	if e = executor.ApplyMigration(ctx); e != nil {
		t.Fatal(e)
	}
	sectionColumn := propertycatalog.SlotColumn("section")
	reports, e := executor.Backfill(ctx)
	if e != nil {
		t.Fatal(e)
	}
	// Both rows of the identity carry the marker after backfill; the
	// token-less row keeps empty slots and reads from the document.
	if len(reports) != 1 || reports[0].EventKey != "bench_visit" || reports[0].Materialized != 2 {
		t.Fatalf("backfill report wrong: %+v", reports)
	}
	// The materialized row carries the exact lexical tokens (string quotes
	// included) and the revision marker; the token-less row keeps its marker
	// with empty slots so the reader falls back to the document.
	got := slotsQuery(t, c, fmt.Sprintf(
		"SELECT event_id,dedicated_layout_version,%s,%s FROM event_facts_raw ORDER BY event_id",
		sectionColumn, propertycatalog.SlotColumn("duration_ms")))
	lines := strings.Split(got, "\n")
	if len(lines) != 2 {
		t.Fatalf("expected two rows: %q", got)
	}
	if !strings.HasPrefix(lines[0], "00000000-0000-4000-8000-0000000000a1\t1\t") || !strings.Contains(lines[0], "\t\"section-3\"\t950") {
		t.Fatalf("materialized row wrong: %q", lines[0])
	}
	if lines[1] != "00000000-0000-4000-8000-0000000000a2\t1" {
		t.Fatalf("token-less row must keep empty slots: %q", lines[1])
	}
	// The recreated view exposes slots through the winner tuple.
	viewValue := slotsQuery(t, c, fmt.Sprintf(
		"SELECT %s FROM event_facts WHERE project_id='p1' AND event_id='00000000-0000-4000-8000-0000000000a1'", sectionColumn))
	if viewValue != `"section-3"` {
		t.Fatalf("view slot projection wrong: %q", viewValue)
	}
	// Re-running the whole chain is idempotent.
	if e = executor.ApplyMigration(ctx); e != nil {
		t.Fatal(e)
	}
	if reports, e = executor.Backfill(ctx); e != nil {
		t.Fatal(e)
	}
	if reports[0].Materialized != 2 {
		t.Fatalf("idempotent backfill re-marked rows: %+v", reports[0])
	}
}
