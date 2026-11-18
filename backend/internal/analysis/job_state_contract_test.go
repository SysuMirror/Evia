package analysis

import (
	"encoding/json"
	"sort"
	"testing"

	"github.com/howtomakeaname/Evia/backend/internal/gen/wire"
)

// TestQueryJobStateVocabularyMatchesContract guards the analysis query job
// lifecycle closed set against the QueryJob.state OpenAPI enum. The storage
// copy lives in the migration 005 CHECK constraint, so every drift between Go
// constants and the published contract fails here before it can reach clients.
func TestQueryJobStateVocabularyMatchesContract(t *testing.T) {
	var bundle struct {
		Defs map[string]map[string]any `json:"$defs"`
	}
	if err := json.Unmarshal(wire.Schemas, &bundle); err != nil {
		t.Fatal(err)
	}
	props, ok := bundle.Defs["QueryJob"]["properties"].(map[string]any)
	if !ok {
		t.Fatal("QueryJob schema missing properties")
	}
	raw, ok := props["state"].(map[string]any)["enum"].([]any)
	if !ok {
		t.Fatal("QueryJob.state enum missing")
	}
	got := make([]string, 0, len(raw))
	for _, value := range raw {
		got = append(got, value.(string))
	}
	want := append([]string(nil), JobStates...)
	sort.Strings(want)
	sort.Strings(got)
	if len(want) != len(got) {
		t.Fatalf("query job state drift: contract %v != Go %v", got, want)
	}
	for i := range want {
		if want[i] != got[i] {
			t.Fatalf("query job state drift: contract %v != Go %v", got, want)
		}
	}
}
