package exporting

import (
	"encoding/json"
	"testing"

	"github.com/howtomakeaname/Evia/backend/internal/analysis"
	"github.com/howtomakeaname/Evia/backend/internal/gen/wire"
)

// TestExportBudgetsMatchContract pins the duplicated client/server limits to
// the numeric constraints published by the embedded contract. The console
// keeps its own TypeScript copies for pre-submit validation; without this
// guard, either side could change 500/20/200 independently and the divergence
// would only surface as rejected user input at runtime.
func TestExportBudgetsMatchContract(t *testing.T) {
	var bundle struct {
		Defs map[string]map[string]any `json:"$defs"`
	}
	if err := json.Unmarshal(wire.Schemas, &bundle); err != nil {
		t.Fatal(err)
	}
	maxLength := func(schema, property string) float64 {
		t.Helper()
		props, ok := bundle.Defs[schema]["properties"].(map[string]any)
		if !ok {
			t.Fatalf("%s missing properties", schema)
		}
		node, ok := props[property].(map[string]any)
		if !ok {
			t.Fatalf("%s.%s missing", schema, property)
		}
		bound, ok := node["maxLength"].(float64)
		if !ok {
			t.Fatalf("%s.%s has no maxLength", schema, property)
		}
		return bound
	}
	maximum := func(schema, property string) float64 {
		t.Helper()
		props, ok := bundle.Defs[schema]["properties"].(map[string]any)
		if !ok {
			t.Fatalf("%s missing properties", schema)
		}
		node, ok := props[property].(map[string]any)
		if !ok {
			t.Fatalf("%s.%s missing", schema, property)
		}
		bound, ok := node["maximum"].(float64)
		if !ok {
			t.Fatalf("%s.%s has no maximum", schema, property)
		}
		return bound
	}
	maxItems := func(schema, property string) float64 {
		t.Helper()
		props, ok := bundle.Defs[schema]["properties"].(map[string]any)
		if !ok {
			t.Fatalf("%s missing properties", schema)
		}
		node, ok := props[property].(map[string]any)
		if !ok {
			t.Fatalf("%s.%s missing", schema, property)
		}
		bound, ok := node["maxItems"].(float64)
		if !ok {
			t.Fatalf("%s.%s has no maxItems", schema, property)
		}
		return bound
	}

	if got := maxLength("ExportInput", "name"); got != float64(MaxNameLength) {
		t.Fatalf("ExportInput.name maxLength=%v != exporting.MaxNameLength=%d", got, MaxNameLength)
	}
	if got := maxLength("SavedAnalysisExportInput", "name"); got != float64(MaxNameLength) {
		t.Fatalf("SavedAnalysisExportInput.name maxLength=%v != exporting.MaxNameLength=%d", got, MaxNameLength)
	}
	if got := maximum("QueryRequest", "limit"); got != float64(analysis.BudgetResultRows) {
		t.Fatalf("QueryRequest.limit maximum=%v != analysis.BudgetResultRows=%d", got, analysis.BudgetResultRows)
	}
	if got := maxItems("QueryRequest", "event_keys"); got != float64(analysis.BudgetEventSelections) {
		t.Fatalf("QueryRequest.event_keys maxItems=%v != analysis.BudgetEventSelections=%d", got, analysis.BudgetEventSelections)
	}
}
