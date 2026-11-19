package analysis

import (
	"encoding/json"
	"testing"

	"github.com/howtomakeaname/Evia/backend/internal/gen/wire"
)

// TestCoverageStateVocabularyContract guards the Go-produced coverage state
// subset against the QueryResult metadata coverage enum. The pipeline only
// certifies provisional today; settled/partial stay contract-published
// reserved states, so the assertion is subset rather than set equality. A Go
// value outside the contract or a contract that drops a produced state both
// fail here.
func TestCoverageStateVocabularyContract(t *testing.T) {
	var bundle struct {
		Defs map[string]map[string]any `json:"$defs"`
	}
	if err := json.Unmarshal(wire.Schemas, &bundle); err != nil {
		t.Fatal(err)
	}
	coverage, ok := bundle.Defs["QueryResult"]["properties"].(map[string]any)["metadata"].(map[string]any)["properties"].(map[string]any)["coverage"].(map[string]any)
	if !ok {
		t.Fatal("QueryResult.metadata.coverage schema missing")
	}
	raw, ok := coverage["properties"].(map[string]any)["state"].(map[string]any)["enum"].([]any)
	if !ok {
		t.Fatal("coverage.state enum missing")
	}
	allowed := map[string]bool{}
	for _, value := range raw {
		allowed[value.(string)] = true
	}
	for _, state := range CoverageStates {
		if !allowed[state] {
			t.Fatalf("coverage state drift: Go value %q absent from contract %v", state, raw)
		}
	}
}
