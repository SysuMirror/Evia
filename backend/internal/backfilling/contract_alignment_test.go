package backfilling

import (
	"encoding/json"
	"sort"
	"testing"

	"github.com/howtomakeaname/Evia/backend/internal/gen/wire"
)

// TestBackfillStateMatchesContract keeps the backfill job lifecycle closed
// set aligned with the BackfillJob.state enum in the embedded bundle.
func TestBackfillStateMatchesContract(t *testing.T) {
	var bundle struct {
		Defs map[string]map[string]any `json:"$defs"`
	}
	if err := json.Unmarshal(wire.Schemas, &bundle); err != nil {
		t.Fatal(err)
	}
	props, ok := bundle.Defs["BackfillJob"]["properties"].(map[string]any)
	if !ok {
		t.Fatal("BackfillJob schema missing properties")
	}
	raw, ok := props["state"].(map[string]any)["enum"].([]any)
	if !ok {
		t.Fatal("BackfillJob.state enum missing from embedded bundle")
	}
	got := make([]string, 0, len(raw))
	for _, value := range raw {
		got = append(got, value.(string))
	}
	want := []string{StateQueued, StateRunning, StateCompleted, StateFailed, StateCancelled}
	sort.Strings(got)
	sort.Strings(want)
	if len(got) != len(want) {
		t.Fatalf("backfill state drift: contract %v != Go %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("backfill state drift: contract %v != Go %v", got, want)
		}
	}
}
