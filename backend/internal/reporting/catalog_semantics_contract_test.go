package reporting

import (
	"encoding/json"
	"sort"
	"testing"

	"github.com/howtomakeaname/Evia/backend/internal/gen/wire"
)

// TestCatalogFieldSemanticsVocabularyContract guards the single-value
// missing/null semantic marker enums on AnalysisCatalogField against the
// embedded bundle, so introducing a new semantic moves Go and contract together.
func TestCatalogFieldSemanticsVocabularyContract(t *testing.T) {
	var bundle struct {
		Defs map[string]map[string]any `json:"$defs"`
	}
	if err := json.Unmarshal(wire.Schemas, &bundle); err != nil {
		t.Fatal(err)
	}
	props, ok := bundle.Defs["AnalysisCatalogField"]["properties"].(map[string]any)
	if !ok {
		t.Fatal("AnalysisCatalogField schema missing properties")
	}
	enumOf := func(property string) []string {
		raw, ok := props[property].(map[string]any)["enum"].([]any)
		if !ok {
			t.Fatalf("AnalysisCatalogField.%s enum missing", property)
		}
		out := make([]string, 0, len(raw))
		for _, value := range raw {
			out = append(out, value.(string))
		}
		return out
	}
	for _, tc := range []struct {
		property string
		want     []string
	}{
		{"missing_semantics", CatalogMissingSemantics},
		{"null_semantics", CatalogNullSemantics},
	} {
		got := enumOf(tc.property)
		want := append([]string(nil), tc.want...)
		sort.Strings(got)
		sort.Strings(want)
		if len(got) != len(want) {
			t.Fatalf("catalog field %s drift: contract %v != Go %v", tc.property, got, want)
		}
		for i := range want {
			if got[i] != want[i] {
				t.Fatalf("catalog field %s drift: contract %v != Go %v", tc.property, got, want)
			}
		}
	}
}
