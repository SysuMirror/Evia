package reporting

import (
	"encoding/json"
	"sort"
	"testing"

	"github.com/howtomakeaname/Evia/backend/internal/gen/wire"
	"github.com/howtomakeaname/Evia/backend/internal/platform/contract"
)

// TestChangeCategoryVocabularyMatchesContract keeps the four semantic diff
// category vocabularies aligned with the embedded *Change.category OpenAPI
// enums. This guard would have caught the retention/segment/path/attribution/
// metric_ref and dashboard parameters enum drift before release.
func TestChangeCategoryVocabularyMatchesContract(t *testing.T) {
	var bundle struct {
		Defs map[string]map[string]any `json:"$defs"`
	}
	if err := json.Unmarshal(wire.Schemas, &bundle); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		schema string
		goSet  []string
	}{
		{"SavedAnalysisChange", SavedAnalysisChangeCategories},
		{"MetricChange", MetricChangeCategories},
		{"DashboardChange", DashboardChangeCategories},
		{"FormulaMetricChange", FormulaMetricChangeCategories},
	}
	for _, tc := range cases {
		props, ok := bundle.Defs[tc.schema]["properties"].(map[string]any)
		if !ok {
			t.Fatalf("%s schema missing properties", tc.schema)
		}
		raw, ok := props["category"].(map[string]any)["enum"].([]any)
		if !ok {
			t.Fatalf("%s.category enum missing", tc.schema)
		}
		contractSet := make([]string, 0, len(raw))
		for _, value := range raw {
			contractSet = append(contractSet, value.(string))
		}
		want := append([]string(nil), tc.goSet...)
		got := append([]string(nil), contractSet...)
		sort.Strings(want)
		sort.Strings(got)
		if len(want) != len(got) {
			t.Fatalf("%s category drift: contract %v != Go %v", tc.schema, got, want)
		}
		for i := range want {
			if want[i] != got[i] {
				t.Fatalf("%s category drift: contract %v != Go %v", tc.schema, got, want)
			}
		}
	}
}

// TestEveryChangeCategoryEncodesThroughBoundary proves each Go vocabulary
// value actually passes the strict HTTP boundary validator, not just set
// equality on paper.
func TestEveryChangeCategoryEncodesThroughBoundary(t *testing.T) {
	validator, err := contract.New()
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		schema string
		goSet  []string
	}{
		{"SavedAnalysisChange", SavedAnalysisChangeCategories},
		{"MetricChange", MetricChangeCategories},
		{"DashboardChange", DashboardChangeCategories},
		{"FormulaMetricChange", FormulaMetricChangeCategories},
	} {
		for _, category := range tc.goSet {
			raw, err := json.Marshal(wire.SavedAnalysisChange{Category: category, Path: "/" + category})
			if err != nil {
				t.Fatal(err)
			}
			decoded, err := contract.Decode(raw)
			if err != nil {
				t.Fatal(err)
			}
			// SavedAnalysisChange is the wire struct used here; each schema
			// shares the same category/path shape so encoding success is the
			// boundary guarantee, and the enum guard above handles exact sets.
			if err := validator.Validate(tc.schema, decoded); err != nil {
				t.Fatalf("%s category %q fails boundary validation: %v", tc.schema, category, err)
			}
		}
	}
}
