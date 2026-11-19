package catalog

import (
	"encoding/json"
	"sort"
	"testing"

	"github.com/howtomakeaname/Evia/backend/internal/gen/wire"
)

// TestCompatibilityVocabularyMatchesContract guards the schema compatibility
// verdict closed set against the ValidationResult.compatibility OpenAPI enum.
func TestCompatibilityVocabularyMatchesContract(t *testing.T) {
	var bundle struct {
		Defs map[string]map[string]any `json:"$defs"`
	}
	if err := json.Unmarshal(wire.Schemas, &bundle); err != nil {
		t.Fatal(err)
	}
	property, ok := bundle.Defs["ValidationResult"]["properties"].(map[string]any)["compatibility"].(map[string]any)
	if !ok {
		t.Fatal("ValidationResult.compatibility schema missing")
	}
	raw, ok := property["enum"].([]any)
	if !ok {
		t.Fatal("ValidationResult.compatibility enum missing")
	}
	got := make([]string, 0, len(raw))
	for _, value := range raw {
		got = append(got, value.(string))
	}
	want := append([]string(nil), CompatibilityValues...)
	sort.Strings(want)
	sort.Strings(got)
	if len(want) != len(got) {
		t.Fatalf("compatibility drift: contract %v != Go %v", got, want)
	}
	for i := range want {
		if want[i] != got[i] {
			t.Fatalf("compatibility drift: contract %v != Go %v", got, want)
		}
	}
}
