//go:build integration

package pipeline

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/howtomakeaname/Evia/backend/internal/identity"
)

func identityScanner(t *testing.T, config ClickHouseConfig) *IdentityLinkScanner {
	t.Helper()
	scanner, e := NewIdentityLinkScanner(config, &http.Client{Timeout: 30 * time.Second})
	if e != nil {
		t.Fatal(e)
	}
	return scanner
}

func linkFactRow(eventID, uid, anonymous, receivedAt string) factRow {
	return factRow{
		SourceKind:      "browser",
		ProjectID:       "00000000-0000-4000-8000-0000000000aa",
		Environment:     "development",
		SourceID:        "00000000-0000-4000-8000-0000000000bb",
		EventID:         eventID,
		EventKey:        "sys.identity_link",
		SchemaVersion:   1,
		ReleaseRevision: 1,
		OccurredAt:      receivedAt,
		EventTime:       receivedAt,
		ReceivedAt:      receivedAt,
		CanonicalHash:   canonicalHashForEvent(eventID),
		UID:             &uid,
		AnonymousID:     &anonymous,
		SessionID:       nil,
		SDKName:         "evia-browser",
		SDKVersion:      "0.1.0",
		ContextJSON:     "{}",
		PropertiesJSON:  "{}",
	}
}

func canonicalHashForEvent(eventID string) string {
	sum := sha256.Sum256([]byte("scanner:" + eventID))
	return hex.EncodeToString(sum[:])
}

func insertScannerRows(t *testing.T, c ClickHouseConfig, rows ...factRow) {
	t.Helper()
	encodedRows := make([]string, 0, len(rows))
	for _, row := range rows {
		encoded, e := json.Marshal(row)
		if e != nil {
			t.Fatal(e)
		}
		var values map[string]any
		if e = json.Unmarshal(encoded, &values); e != nil {
			t.Fatal(e)
		}
		delete(values, "occurred_at_precise")
		delete(values, "event_time_precise")
		values["occurred_at"] = row.OccurredAt[:23]
		values["event_time"] = row.EventTime[:23]
		encoded, e = json.Marshal(values)
		if e != nil {
			t.Fatal(e)
		}
		encodedRows = append(encodedRows, string(encoded))
	}
	if _, e := clickhouseQuery(context.Background(), c, "INSERT INTO event_facts_raw FORMAT JSONEachRow\n"+strings.Join(encodedRows, "\n")); e != nil {
		t.Fatal(e)
	}
}

func TestIdentityLinkScannerAggregatesPairsBehindTheWatermark(t *testing.T) {
	c := erasureDatabase(t)
	scanner := identityScanner(t, c)
	ctx := context.Background()
	scope := identity.Scope{
		ProjectID:   "00000000-0000-4000-8000-0000000000aa",
		Environment: "development",
	}
	// Same pair seen twice plus one unrelated event and one incomplete link;
	// a second pair arrives later than the first watermark candidate.
	insertScannerRows(t, c,
		linkFactRow("00000000-0000-4000-8000-0000000000e1", "u1", "11111111-1111-4111-8111-111111111111", "2026-11-20 03:00:00.100000000"),
		linkFactRow("00000000-0000-4000-8000-0000000000e2", "u1", "11111111-1111-4111-8111-111111111111", "2026-11-20 03:00:05.900000000"),
		linkFactRow("00000000-0000-4000-8000-0000000000e3", "u2", "22222222-2222-4222-8222-222222222222", "2026-11-20 03:00:02.000000000"),
		func() factRow {
			row := linkFactRow("00000000-0000-4000-8000-0000000000e4", "", "33333333-3333-4333-8333-333333333333", "2026-11-20 03:00:03.000000000")
			row.UID = nil
			row.EventKey = "sys.identity_link"
			return row
		}(),
		func() factRow {
			row := linkFactRow("00000000-0000-4000-8000-0000000000e5", "u3", "44444444-4444-4444-8444-444444444444", "2026-11-20 03:00:04.000000000")
			row.EventKey = "page_opened"
			return row
		}(),
	)
	links, exhausted, e := scanner.ScanLinks(ctx, scope, time.Date(2026, 11, 20, 2, 0, 0, 0, time.UTC), 100)
	if e != nil || !exhausted {
		t.Fatalf("scan failed: %v exhausted=%v", e, exhausted)
	}
	if len(links) != 2 {
		t.Fatalf("two complete pairs expected (aggregated), got %d: %+v", len(links), links)
	}
	// Ordered by latest reception, the u2 pair precedes the u1 pair.
	if links[0].UID != "u2" || links[1].UID != "u1" {
		t.Fatalf("scan order wrong: %+v", links)
	}
	first := links[1]
	// Aggregation keeps the earliest and the latest reception of the pair.
	if !first.FirstSeenAt.Equal(time.Date(2026, 11, 20, 3, 0, 0, 100000000, time.UTC)) ||
		!first.LastSeenAt.Equal(time.Date(2026, 11, 20, 3, 0, 5, 900000000, time.UTC)) {
		t.Fatalf("pair window wrong: %+v", first)
	}
	// The watermark partition: everything at or before it is excluded.
	links, exhausted, e = scanner.ScanLinks(ctx, scope, time.Date(2026, 11, 20, 3, 0, 2, 0, time.UTC), 100)
	if e != nil || !exhausted {
		t.Fatalf("rescan failed: %v", e)
	}
	if len(links) != 1 || links[0].UID != "u1" {
		t.Fatalf("watermark must exclude earlier pairs, got %+v", links)
	}
	// A foreign environment never leaks into the scan.
	other := scope
	other.Environment = "production"
	links, exhausted, e = scanner.ScanLinks(ctx, other, time.Date(2026, 11, 20, 2, 0, 0, 0, time.UTC), 100)
	if e != nil || !exhausted || len(links) != 0 {
		t.Fatalf("environment isolation wrong: %+v %v", links, e)
	}
	// The limit partitions a window and reports continuation.
	links, exhausted, e = scanner.ScanLinks(ctx, scope, time.Date(2026, 11, 20, 2, 0, 0, 0, time.UTC), 1)
	if e != nil || exhausted || len(links) != 1 {
		t.Fatalf("bounded scan must continue, got %+v exhausted=%v %v", links, exhausted, e)
	}
}
