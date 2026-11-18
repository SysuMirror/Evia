package analysis

import (
	"encoding/json"
	"sort"
	"testing"

	"github.com/howtomakeaname/Evia/backend/internal/gen/wire"
)

// TestAttributionModelVocabularyMatchesContract guards the attribution model
// closed set against the nested QueryRequest.attribution.model OpenAPI enum.
func TestAttributionModelVocabularyMatchesContract(t *testing.T) {
	var bundle struct {
		Defs map[string]map[string]any `json:"$defs"`
	}
	if err := json.Unmarshal(wire.Schemas, &bundle); err != nil {
		t.Fatal(err)
	}
	attribution, ok := bundle.Defs["QueryRequest"]["properties"].(map[string]any)["attribution"].(map[string]any)
	if !ok {
		t.Fatal("QueryRequest.attribution schema missing")
	}
	model, ok := attribution["properties"].(map[string]any)["model"].(map[string]any)
	if !ok {
		t.Fatal("QueryRequest.attribution.model schema missing")
	}
	raw, ok := model["enum"].([]any)
	if !ok {
		t.Fatal("QueryRequest.attribution.model enum missing")
	}
	got := make([]string, 0, len(raw))
	for _, value := range raw {
		got = append(got, value.(string))
	}
	want := append([]string(nil), AttributionModels...)
	sort.Strings(want)
	sort.Strings(got)
	if len(want) != len(got) {
		t.Fatalf("attribution model drift: contract %v != Go %v", got, want)
	}
	for i := range want {
		if want[i] != got[i] {
			t.Fatalf("attribution model drift: contract %v != Go %v", got, want)
		}
	}
}
