package analysis

import (
	"encoding/json"
	"sort"
	"testing"

	"github.com/howtomakeaname/Evia/backend/internal/gen/wire"
)

// TestQueryKindVocabularyMatchesContract guards the query kind closed set
// against the QueryRequest.kind OpenAPI enum. The discovery advertisement
// reuses the same slice, so this is the domain-owner guard for both the
// validator and capability discovery.
func TestQueryKindVocabularyMatchesContract(t *testing.T) {
	var bundle struct {
		Defs map[string]map[string]any `json:"$defs"`
	}
	if err := json.Unmarshal(wire.Schemas, &bundle); err != nil {
		t.Fatal(err)
	}
	property, ok := bundle.Defs["QueryRequest"]["properties"].(map[string]any)["kind"].(map[string]any)
	if !ok {
		t.Fatal("QueryRequest.kind schema missing")
	}
	raw, ok := property["enum"].([]any)
	if !ok {
		t.Fatal("QueryRequest.kind enum missing")
	}
	got := make([]string, 0, len(raw))
	for _, value := range raw {
		got = append(got, value.(string))
	}
	want := append([]string(nil), QueryKinds...)
	sort.Strings(want)
	sort.Strings(got)
	if len(want) != len(got) {
		t.Fatalf("query kind drift: contract %v != Go %v", got, want)
	}
	for i := range want {
		if want[i] != got[i] {
			t.Fatalf("query kind drift: contract %v != Go %v", got, want)
		}
	}
}
