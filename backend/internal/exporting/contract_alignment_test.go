package exporting

import (
	"encoding/json"
	"sort"
	"testing"

	"github.com/howtomakeaname/Evia/backend/internal/gen/wire"
)

func enumFromBundle(t *testing.T, schema, property string) []string {
	t.Helper()
	var bundle struct {
		Defs map[string]map[string]any `json:"$defs"`
	}
	if err := json.Unmarshal(wire.Schemas, &bundle); err != nil {
		t.Fatal(err)
	}
	definition, ok := bundle.Defs[schema]
	if !ok {
		t.Fatalf("%s missing from embedded bundle", schema)
	}
	props, ok := definition["properties"].(map[string]any)
	if !ok {
		t.Fatalf("%s has no properties", schema)
	}
	node, ok := props[property].(map[string]any)
	if !ok {
		t.Fatalf("%s.%s missing", schema, property)
	}
	raw, ok := node["enum"].([]any)
	if !ok {
		t.Fatalf("%s.%s has no enum vocabulary", schema, property)
	}
	out := make([]string, 0, len(raw))
	for _, value := range raw {
		text, ok := value.(string)
		if !ok {
			t.Fatalf("%s.%s enum contains non-string value", schema, property)
		}
		out = append(out, text)
	}
	return out
}

func assertSameSet(t *testing.T, label string, want, got []string) {
	t.Helper()
	a := append([]string(nil), want...)
	b := append([]string(nil), got...)
	sort.Strings(a)
	sort.Strings(b)
	if len(a) != len(b) {
		t.Fatalf("%s drift: contract %v != Go %v", label, b, a)
	}
	for i := range a {
		if a[i] != b[i] {
			t.Fatalf("%s drift: contract %v != Go %v", label, b, a)
		}
	}
}

// TestExportVocabularyMatchesContract guards the export format and job state
// closed sets against the embedded OpenAPI component schemas. A new format or
// lifecycle state added on only one side fails the build.
func TestExportVocabularyMatchesContract(t *testing.T) {
	formats := []string{FormatJSON, FormatCSV, FormatNDJSON, FormatParquet}
	assertSameSet(t, "export format", formats, enumFromBundle(t, "ExportInput", "format"))
	assertSameSet(t, "saved export format", formats, enumFromBundle(t, "SavedAnalysisExportInput", "format"))
	assertSameSet(t, "export job format", formats, enumFromBundle(t, "ExportJob", "format"))
	assertSameSet(t, "export job state",
		[]string{StateQueued, StateRunning, StateCompleted, StateFailed, StateCancelled},
		enumFromBundle(t, "ExportJob", "state"))
}
