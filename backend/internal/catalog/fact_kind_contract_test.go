package catalog

import (
	"encoding/json"
	"sort"
	"testing"

	"github.com/howtomakeaname/Evia/backend/internal/gen/wire"
)

// TestFactKindVocabularyMatchesContract guards the fact kind closed set
// against the DraftInput.fact_kind OpenAPI enum.
func TestFactKindVocabularyMatchesContract(t *testing.T) {
	var bundle struct {
		Defs map[string]map[string]any `json:"$defs"`
	}
	if err := json.Unmarshal(wire.Schemas, &bundle); err != nil {
		t.Fatal(err)
	}
	property, ok := bundle.Defs["DraftInput"]["properties"].(map[string]any)["fact_kind"].(map[string]any)
	if !ok {
		t.Fatal("DraftInput.fact_kind schema missing")
	}
	raw, ok := property["enum"].([]any)
	if !ok {
		t.Fatal("DraftInput.fact_kind enum missing")
	}
	got := make([]string, 0, len(raw))
	for _, value := range raw {
		got = append(got, value.(string))
	}
	want := append([]string(nil), FactKinds...)
	sort.Strings(want)
	sort.Strings(got)
	if len(want) != len(got) {
		t.Fatalf("fact kind drift: contract %v != Go %v", got, want)
	}
	for i := range want {
		if want[i] != got[i] {
			t.Fatalf("fact kind drift: contract %v != Go %v", got, want)
		}
	}
}
