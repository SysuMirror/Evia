package diagnostics

import (
	"encoding/json"
	"sort"
	"testing"

	"github.com/howtomakeaname/Evia/backend/internal/gen/wire"
)

// TestDiagnosticsStatusMatchesContract keeps the four diagnostic status
// constants aligned with the embedded OpenAPI diagnostics schemas. The
// pipeline section is the only one allowed to emit no_data; every other
// section uses the three-value subset.
func TestDiagnosticsStatusMatchesContract(t *testing.T) {
	var bundle struct {
		Defs map[string]map[string]any `json:"$defs"`
	}
	if err := json.Unmarshal(wire.Schemas, &bundle); err != nil {
		t.Fatal(err)
	}
	goStatuses := []string{StatusOK, StatusNoData, StatusUnavailable, StatusDisabled}
	allowed := map[string]bool{}
	for _, status := range goStatuses {
		allowed[status] = true
	}
	sections := map[string]bool{
		"DiagnosticsBudget":        false,
		"DiagnosticsPipeline":      true,
		"DiagnosticsRejections":    false,
		"DiagnosticsSourceSummary": false,
	}
	for schema, allowsNoData := range sections {
		props, ok := bundle.Defs[schema]["properties"].(map[string]any)
		if !ok {
			t.Fatalf("%s schema missing properties", schema)
		}
		raw, ok := props["status"].(map[string]any)["enum"].([]any)
		if !ok {
			t.Fatalf("%s.status enum missing", schema)
		}
		contract := make([]string, 0, len(raw))
		for _, value := range raw {
			contract = append(contract, value.(string))
		}
		for _, status := range contract {
			if !allowed[status] {
				t.Fatalf("%s advertises contract status %q absent from Go closed set %v", schema, status, goStatuses)
			}
		}
		hasNoData := false
		for _, status := range contract {
			if status == StatusNoData {
				hasNoData = true
			}
		}
		if hasNoData != allowsNoData {
			t.Fatalf("%s no_data allowance drift: contract=%v, expected=%v", schema, hasNoData, allowsNoData)
		}
		// Every Go-allowed status must be advertised by the pipeline section,
		// which is the full vocabulary owner in the contract.
		if allowsNoData {
			got := append([]string(nil), contract...)
			want := append([]string(nil), goStatuses...)
			sort.Strings(got)
			sort.Strings(want)
			if len(got) != len(want) {
				t.Fatalf("pipeline status drift: contract %v != Go %v", got, want)
			}
			for i := range want {
				if got[i] != want[i] {
					t.Fatalf("pipeline status drift: contract %v != Go %v", got, want)
				}
			}
		}
	}
}
