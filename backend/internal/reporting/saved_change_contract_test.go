package reporting

import (
	"encoding/json"
	"testing"

	"github.com/howtomakeaname/Evia/backend/internal/gen/wire"
	"github.com/howtomakeaname/Evia/backend/internal/platform/contract"
)

// validateChange mirrors the HTTP boundary: marshal the wire struct, decode
// it through the same strict JSON decoder, then validate the embedded schema.
func validateChange(t *testing.T, validator *contract.Validator, change wire.SavedAnalysisChange) error {
	t.Helper()
	raw, err := json.Marshal(change)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := contract.Decode(raw)
	if err != nil {
		t.Fatal(err)
	}
	return validator.Validate("SavedAnalysisChange", decoded)
}

// TestSavedAnalysisChangeCategoriesSatisfyContract proves every diff category
// the Go policy can emit is accepted by the embedded OpenAPI enum. Advanced
// analysis kinds (retention/segment/path/attribution) and metric references
// were added after the original enum and must not make validation responses
// fail contract encoding at the HTTP boundary.
func TestSavedAnalysisChangeCategoriesSatisfyContract(t *testing.T) {
	validator, err := contract.New()
	if err != nil {
		t.Fatal(err)
	}
	// Categories emitted by savedAnalysisDiff beyond the original query block.
	drifted := []string{"retention", "segment", "path", "attribution", "metric_ref"}
	for _, category := range drifted {
		change := wire.SavedAnalysisChange{Category: category, Path: "/query/" + category}
		if err := validateChange(t, validator, change); err != nil {
			t.Fatalf("Go-emitted category %q rejected by contract: %v", category, err)
		}
	}
}
