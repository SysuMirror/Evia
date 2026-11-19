package analysis

import (
	"encoding/json"
	"testing"

	"github.com/howtomakeaname/Evia/backend/internal/gen/wire"
)

// TestFunnelModeVocabularyMatchesContract guards the funnel mode closed set
// against the nested QueryRequest.funnel.mode OpenAPI const. The contract
// publishes a single mode today, so Go and the schema must move together once
// another mode is introduced.
func TestFunnelModeVocabularyMatchesContract(t *testing.T) {
	var bundle struct {
		Defs map[string]map[string]any `json:"$defs"`
	}
	if err := json.Unmarshal(wire.Schemas, &bundle); err != nil {
		t.Fatal(err)
	}
	funnel, ok := bundle.Defs["QueryRequest"]["properties"].(map[string]any)["funnel"].(map[string]any)
	if !ok {
		t.Fatal("QueryRequest.funnel schema missing")
	}
	property, ok := funnel["properties"].(map[string]any)["mode"].(map[string]any)
	if !ok {
		t.Fatal("QueryRequest.funnel.mode schema missing")
	}
	// funnel.mode is a JSON Schema const (one published value), not an enum
	// array, so the Go closed set must equal that exact single-value set.
	mode, ok := property["const"].(string)
	if !ok {
		t.Fatal("QueryRequest.funnel.mode const missing")
	}
	if len(FunnelModes) != 1 || FunnelModes[0] != mode {
		t.Fatalf("funnel mode drift: contract const %q != Go %v", mode, FunnelModes)
	}
}
