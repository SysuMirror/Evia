package analysis

import (
	"encoding/json"
	"sort"
	"testing"

	"github.com/howtomakeaname/Evia/backend/internal/gen/wire"
)

// TestSubjectVocabularyMatchesContract guards the query subject vocabulary
// against both published enums: MetricDefinition carries only the implemented
// subjects, while QueryRequest additionally publishes the reserved
// resolved_person value that validation must reject.
func TestSubjectVocabularyMatchesContract(t *testing.T) {
	var bundle struct {
		Defs map[string]map[string]any `json:"$defs"`
	}
	if err := json.Unmarshal(wire.Schemas, &bundle); err != nil {
		t.Fatal(err)
	}
	enumOf := func(schema, field string) []string {
		property, ok := bundle.Defs[schema]["properties"].(map[string]any)[field].(map[string]any)
		if !ok {
			t.Fatalf("%s.%s schema missing", schema, field)
		}
		raw, ok := property["enum"].([]any)
		if !ok {
			t.Fatalf("%s.%s enum missing", schema, field)
		}
		out := make([]string, 0, len(raw))
		for _, value := range raw {
			out = append(out, value.(string))
		}
		sort.Strings(out)
		return out
	}
	assertSet := func(label string, want, got []string) {
		t.Helper()
		want = append([]string(nil), want...)
		sort.Strings(want)
		if len(want) != len(got) {
			t.Fatalf("%s drift: contract %v != Go %v", label, got, want)
		}
		for i := range want {
			if want[i] != got[i] {
				t.Fatalf("%s drift: contract %v != Go %v", label, got, want)
			}
		}
	}

	assertSet("metric subject", Subjects, enumOf("MetricDefinition", "subject"))
	assertSet("query subject",
		[]string{SubjectUID, SubjectAnonymous, SubjectResolvedPerson},
		enumOf("QueryRequest", "subject"))
}
