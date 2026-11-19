package erasure

import (
	"encoding/json"
	"sort"
	"testing"

	"github.com/howtomakeaname/Evia/backend/internal/gen/wire"
)

// TestErasureStateVocabularyMatchesContract guards the erasure job lifecycle
// closed set against the ErasureJob.state OpenAPI enum. The storage copy lives
// in the migration 015 CHECK constraint, so any drift between the Go constants
// and the published contract fails here before it can reach clients.
func TestErasureStateVocabularyMatchesContract(t *testing.T) {
	var bundle struct {
		Defs map[string]map[string]any `json:"$defs"`
	}
	if err := json.Unmarshal(wire.Schemas, &bundle); err != nil {
		t.Fatal(err)
	}
	props, ok := bundle.Defs["ErasureJob"]["properties"].(map[string]any)
	if !ok {
		t.Fatal("ErasureJob schema missing properties")
	}
	raw, ok := props["state"].(map[string]any)["enum"].([]any)
	if !ok {
		t.Fatal("ErasureJob.state enum missing")
	}
	got := make([]string, 0, len(raw))
	for _, value := range raw {
		got = append(got, value.(string))
	}
	want := append([]string(nil), ErasureStates...)
	sort.Strings(want)
	sort.Strings(got)
	if len(want) != len(got) {
		t.Fatalf("erasure state drift: contract %v != Go %v", got, want)
	}
	for i := range want {
		if want[i] != got[i] {
			t.Fatalf("erasure state drift: contract %v != Go %v", got, want)
		}
	}
}
