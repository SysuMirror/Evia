package ingestion

import (
	"encoding/json"
	"sort"
	"testing"

	"github.com/howtomakeaname/Evia/backend/internal/gen/wire"
)

// TestSourceVocabularyMatchesContract guards the source kind and status
// closed sets against the Source/SourceInput/SourceIssued/SourceUpdate
// OpenAPI enums. Every source schema publishes the same pair of vocabularies,
// so a single walk covers all six enum properties and fails on any drift.
func TestSourceVocabularyMatchesContract(t *testing.T) {
	var bundle struct {
		Defs map[string]map[string]any `json:"$defs"`
	}
	if err := json.Unmarshal(wire.Schemas, &bundle); err != nil {
		t.Fatal(err)
	}
	wantKind := append([]string(nil), SourceKinds...)
	sort.Strings(wantKind)
	wantStatus := append([]string(nil), SourceStatuses...)
	sort.Strings(wantStatus)

	checkedKind, checkedStatus := 0, 0
	for _, schemaName := range []string{"Source", "SourceInput", "SourceIssued", "SourceUpdate"} {
		props, ok := bundle.Defs[schemaName]["properties"].(map[string]any)
		if !ok {
			t.Fatalf("%s schema missing properties", schemaName)
		}
		for field, want := range map[string][]string{
			"kind":   wantKind,
			"status": wantStatus,
		} {
			property, ok := props[field].(map[string]any)
			if !ok {
				continue
			}
			raw, ok := property["enum"].([]any)
			if !ok {
				continue
			}
			got := make([]string, 0, len(raw))
			for _, value := range raw {
				got = append(got, value.(string))
			}
			sort.Strings(got)
			if len(got) != len(want) {
				t.Fatalf("%s.%s source vocabulary drift: contract %v != Go %v", schemaName, field, got, want)
			}
			for i := range want {
				if got[i] != want[i] {
					t.Fatalf("%s.%s source vocabulary drift: contract %v != Go %v", schemaName, field, got, want)
				}
			}
			if field == "kind" {
				checkedKind++
			} else {
				checkedStatus++
			}
		}
	}
	if checkedKind != 3 || checkedStatus != 3 {
		t.Fatalf("expected 3 kind and 3 status enums across source schemas, got %d/%d", checkedKind, checkedStatus)
	}
}
