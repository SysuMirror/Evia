//go:build integration

package analysis

import (
	"encoding/json"
	"strings"
	"testing"
)

// dedicatedMarkerRow is the view projection of the version 008 marker.
type dedicatedMarkerRow struct {
	DedicatedLayoutVersion uint8 `json:"dedicated_layout_version"`
}

func readDedicatedMarker(t *testing.T, client *ClickHouse, eventID string) dedicatedMarkerRow {
	t.Helper()
	data := chExec(t, client, "SELECT dedicated_layout_version "+
		"FROM event_facts WHERE event_id='"+eventID+"' FORMAT JSONEachRow")
	var row dedicatedMarkerRow
	if err := json.Unmarshal(data, &row); err != nil {
		t.Fatal(err, string(data))
	}
	return row
}

func TestClickHouseDedicatedLayoutMarkerTypesAndDefaults(t *testing.T) {
	client := clickHouseDatabase(t)

	// Raw table and view must both expose the UInt8 marker; the view slot is
	// fact.29 and keeps the exact physical type.
	raw := chExec(t, client, "SELECT type FROM system.columns WHERE database=currentDatabase() "+
		"AND table='event_facts_raw' AND name='dedicated_layout_version' FORMAT TSV")
	if strings.TrimSpace(string(raw)) != "UInt8" {
		t.Fatalf("raw marker type = %q, want UInt8", strings.TrimSpace(string(raw)))
	}
	view := chExec(t, client, "SELECT type FROM system.columns WHERE database=currentDatabase() "+
		"AND table='event_facts' AND name='dedicated_layout_version' FORMAT TSV")
	if strings.TrimSpace(string(view)) != "UInt8" {
		t.Fatalf("view marker type = %q, want UInt8", strings.TrimSpace(string(view)))
	}

	// A legacy writer that omits the field must read the structural default 0
	// from both the raw table and the deduplicated view, so old parts never
	// look like dedicated materialization.
	legacyID := "cccccccc-3333-4333-8333-333333333331"
	seedRows(t, client, []map[string]any{
		fact(legacyID, "sys.page_view", "user-one", "", "source-one", 10, map[string]any{"legacy": true}),
	})
	rawDefault := strings.TrimSpace(string(chExec(t, client,
		"SELECT toString(dedicated_layout_version) FROM event_facts_raw WHERE event_id='"+legacyID+"' FORMAT TSV")))
	if rawDefault != "0" {
		t.Fatalf("raw legacy marker = %q, want 0", rawDefault)
	}
	if marker := readDedicatedMarker(t, client, legacyID); marker.DedicatedLayoutVersion != 0 {
		t.Fatalf("view legacy marker = %d, want 0", marker.DedicatedLayoutVersion)
	}
}

func TestClickHouseDedicatedLayoutMarkerFollowsWinningRow(t *testing.T) {
	client := clickHouseDatabase(t)

	// Explicit catalog revision 7 on the winning row must survive the argMin
	// tuple and be exposed unchanged by the view.
	winnerID := "cccccccc-3333-4333-8333-333333333332"
	winner := fact(winnerID, "sys.page_view", "user-two", "", "source-one", 10, map[string]any{"score": 1})
	winner["dedicated_layout_version"] = 7
	loser := fact(winnerID, "sys.page_view", "user-two", "", "source-one", 20, map[string]any{"score": 2})
	loser["dedicated_layout_version"] = 9
	seedRows(t, client, []map[string]any{winner, loser})
	if marker := readDedicatedMarker(t, client, winnerID); marker.DedicatedLayoutVersion != 7 {
		t.Fatalf("winning marker = %d, want winner revision 7", marker.DedicatedLayoutVersion)
	}

	// When the earliest receipt is a legacy row (marker 0), the later replay's
	// marker must not leak into the winning tuple.
	legacyID := "cccccccc-3333-4333-8333-333333333333"
	legacyWinner := fact(legacyID, "sys.page_view", "user-three", "", "source-one", 10, map[string]any{"legacy": true})
	replay := fact(legacyID, "sys.page_view", "user-three", "", "source-one", 20, map[string]any{"legacy": false})
	replay["dedicated_layout_version"] = 5
	seedRows(t, client, []map[string]any{legacyWinner, replay})
	if marker := readDedicatedMarker(t, client, legacyID); marker.DedicatedLayoutVersion != 0 {
		t.Fatalf("legacy winner marker = %d, want 0 with no replay leakage", marker.DedicatedLayoutVersion)
	}
}
