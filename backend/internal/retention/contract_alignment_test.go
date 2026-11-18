package retention

import (
	"encoding/json"
	"sort"
	"testing"

	"github.com/howtomakeaname/Evia/backend/internal/gen/wire"
)

// TestSweepStatusMatchesContract keeps the retention sweep audit status
// closed set aligned with the RetentionSweep.status enum in the embedded
// bundle.
func TestSweepStatusMatchesContract(t *testing.T) {
	var bundle struct {
		Defs map[string]map[string]any `json:"$defs"`
	}
	if err := json.Unmarshal(wire.Schemas, &bundle); err != nil {
		t.Fatal(err)
	}
	props, ok := bundle.Defs["RetentionSweep"]["properties"].(map[string]any)
	if !ok {
		t.Fatal("RetentionSweep schema missing properties")
	}
	raw, ok := props["status"].(map[string]any)["enum"].([]any)
	if !ok {
		t.Fatal("RetentionSweep.status enum missing from embedded bundle")
	}
	got := make([]string, 0, len(raw))
	for _, value := range raw {
		got = append(got, value.(string))
	}
	want := []string{SweepPlanned, SweepSucceeded, SweepFailed}
	sort.Strings(got)
	sort.Strings(want)
	if len(got) != len(want) {
		t.Fatalf("sweep status drift: contract %v != Go %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("sweep status drift: contract %v != Go %v", got, want)
		}
	}
}
