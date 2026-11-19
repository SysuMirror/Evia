package ingestion

import (
	"encoding/json"
	"testing"

	"github.com/howtomakeaname/Evia/backend/internal/gen/wire"
)

// TestApplicationStateVocabularyContract guards the Go-observable application
// state subset against the identical enums carried by Source, SourceIssued and
// Retention. failed stays a contract-only reserved state, so the assertion is
// subset rather than set equality.
func TestApplicationStateVocabularyContract(t *testing.T) {
	var bundle struct {
		Defs map[string]map[string]any `json:"$defs"`
	}
	if err := json.Unmarshal(wire.Schemas, &bundle); err != nil {
		t.Fatal(err)
	}
	for _, schemaName := range []string{"Source", "SourceIssued", "Retention"} {
		props, ok := bundle.Defs[schemaName]["properties"].(map[string]any)
		if !ok {
			t.Fatalf("%s schema missing properties", schemaName)
		}
		raw, ok := props["application_state"].(map[string]any)["enum"].([]any)
		if !ok {
			t.Fatalf("%s.application_state enum missing", schemaName)
		}
		allowed := map[string]bool{}
		for _, value := range raw {
			allowed[value.(string)] = true
		}
		for _, state := range ApplicationStates {
			if !allowed[state] {
				t.Fatalf("application state drift on %s: Go value %q absent from contract %v", schemaName, state, raw)
			}
		}
	}
}
