package reporting

import (
	"encoding/json"
	"sort"
	"testing"

	"github.com/howtomakeaname/Evia/backend/internal/gen/wire"
)

// TestMetricReferenceKindVocabularyMatchesContract guards the metric
// reference kind closed set against the MetricReference.kind OpenAPI enum.
func TestMetricReferenceKindVocabularyMatchesContract(t *testing.T) {
	var bundle struct {
		Defs map[string]map[string]any `json:"$defs"`
	}
	if err := json.Unmarshal(wire.Schemas, &bundle); err != nil {
		t.Fatal(err)
	}
	property, ok := bundle.Defs["MetricReference"]["properties"].(map[string]any)["kind"].(map[string]any)
	if !ok {
		t.Fatal("MetricReference.kind schema missing")
	}
	raw, ok := property["enum"].([]any)
	if !ok {
		t.Fatal("MetricReference.kind enum missing")
	}
	got := make([]string, 0, len(raw))
	for _, value := range raw {
		got = append(got, value.(string))
	}
	want := append([]string(nil), MetricRefKinds...)
	sort.Strings(want)
	sort.Strings(got)
	if len(want) != len(got) {
		t.Fatalf("metric reference kind drift: contract %v != Go %v", got, want)
	}
	for i := range want {
		if want[i] != got[i] {
			t.Fatalf("metric reference kind drift: contract %v != Go %v", got, want)
		}
	}
}
