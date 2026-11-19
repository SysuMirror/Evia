package pipeline

import (
	"encoding/json"
	"sort"
	"testing"

	"github.com/howtomakeaname/Evia/backend/internal/gen/wire"
)

// TestNavigationTypeVocabularyMatchesContract guards the browser navigation
// closed set against the nested EventEnvelope.context.navigation_type enum.
func TestNavigationTypeVocabularyMatchesContract(t *testing.T) {
	var bundle struct {
		Defs map[string]map[string]any `json:"$defs"`
	}
	if err := json.Unmarshal(wire.Schemas, &bundle); err != nil {
		t.Fatal(err)
	}
	context, ok := bundle.Defs["EventEnvelope"]["properties"].(map[string]any)["context"].(map[string]any)
	if !ok {
		t.Fatal("EventEnvelope.context schema missing")
	}
	navigation, ok := context["properties"].(map[string]any)["navigation_type"].(map[string]any)
	if !ok {
		t.Fatal("EventEnvelope.context.navigation_type schema missing")
	}
	raw, ok := navigation["enum"].([]any)
	if !ok {
		t.Fatal("navigation_type enum missing")
	}
	got := make([]string, 0, len(raw))
	for _, value := range raw {
		got = append(got, value.(string))
	}
	want := append([]string(nil), NavigationTypes...)
	sort.Strings(want)
	sort.Strings(got)
	if len(want) != len(got) {
		t.Fatalf("navigation type drift: contract %v != Go %v", got, want)
	}
	for i := range want {
		if want[i] != got[i] {
			t.Fatalf("navigation type drift: contract %v != Go %v", got, want)
		}
	}
}
