package analysis

import (
	"encoding/json"
	"sort"
	"testing"

	"github.com/howtomakeaname/Evia/backend/internal/gen/wire"
)

// TestAccuracyVocabularyMatchesContract guards the result accuracy closed set
// against the nested QueryResult.metadata.accuracy OpenAPI enum.
func TestAccuracyVocabularyMatchesContract(t *testing.T) {
	var bundle struct {
		Defs map[string]map[string]any `json:"$defs"`
	}
	if err := json.Unmarshal(wire.Schemas, &bundle); err != nil {
		t.Fatal(err)
	}
	metadata, ok := bundle.Defs["QueryResult"]["properties"].(map[string]any)["metadata"].(map[string]any)
	if !ok {
		t.Fatal("QueryResult.metadata schema missing")
	}
	accuracy, ok := metadata["properties"].(map[string]any)["accuracy"].(map[string]any)
	if !ok {
		t.Fatal("QueryResult.metadata.accuracy schema missing")
	}
	raw, ok := accuracy["enum"].([]any)
	if !ok {
		t.Fatal("QueryResult.metadata.accuracy enum missing")
	}
	got := make([]string, 0, len(raw))
	for _, value := range raw {
		got = append(got, value.(string))
	}
	want := append([]string(nil), AccuracyValues...)
	sort.Strings(want)
	sort.Strings(got)
	if len(want) != len(got) {
		t.Fatalf("accuracy drift: contract %v != Go %v", got, want)
	}
	for i := range want {
		if want[i] != got[i] {
			t.Fatalf("accuracy drift: contract %v != Go %v", got, want)
		}
	}
}
