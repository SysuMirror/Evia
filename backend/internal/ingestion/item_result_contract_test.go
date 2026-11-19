package ingestion

import (
	"encoding/json"
	"sort"
	"testing"

	"github.com/howtomakeaname/Evia/backend/internal/gen/wire"
)

// TestItemResultStatusVocabularyMatchesContract guards the collect item
// result status closed set against the CollectResponse.results oneOf const
// branches. The contract models accepted/rejected as a two-branch sum type
// rather than an enum array, so the guard collects each branch status const
// and asserts set equality with ItemStatuses.
func TestItemResultStatusVocabularyMatchesContract(t *testing.T) {
	var bundle struct {
		Defs map[string]map[string]any `json:"$defs"`
	}
	if err := json.Unmarshal(wire.Schemas, &bundle); err != nil {
		t.Fatal(err)
	}
	items, ok := bundle.Defs["CollectResponse"]["properties"].(map[string]any)["results"].(map[string]any)["items"].(map[string]any)
	if !ok {
		t.Fatal("CollectResponse.results.items schema missing")
	}
	branches, ok := items["oneOf"].([]any)
	if !ok {
		t.Fatal("CollectResponse.results.items oneOf missing")
	}
	if len(branches) != len(ItemStatuses) {
		t.Fatalf("item result status drift: contract branches %d != Go %v", len(branches), ItemStatuses)
	}
	got := make([]string, 0, len(branches))
	for _, branch := range branches {
		status, ok := branch.(map[string]any)["properties"].(map[string]any)["status"].(map[string]any)["const"].(string)
		if !ok {
			t.Fatal("CollectResponse results branch status const missing")
		}
		got = append(got, status)
	}
	want := append([]string(nil), ItemStatuses...)
	sort.Strings(want)
	sort.Strings(got)
	for i := range want {
		if want[i] != got[i] {
			t.Fatalf("item result status drift: contract %v != Go %v", got, want)
		}
	}
}
