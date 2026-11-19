package analysis

import (
	"encoding/json"
	"sort"
	"testing"

	"github.com/howtomakeaname/Evia/backend/internal/gen/wire"
)

// TestAggregationVocabularyMatchesContract guards the aggregation operator
// closed set against the nested QueryRequest.metrics.items.op OpenAPI enum.
// The discovery advertisement reuses the same slice, so this is the
// domain-owner guard for both the validator and capability discovery.
func TestAggregationVocabularyMatchesContract(t *testing.T) {
	var bundle struct {
		Defs map[string]map[string]any `json:"$defs"`
	}
	if err := json.Unmarshal(wire.Schemas, &bundle); err != nil {
		t.Fatal(err)
	}
	metrics, ok := bundle.Defs["QueryRequest"]["properties"].(map[string]any)["metrics"].(map[string]any)
	if !ok {
		t.Fatal("QueryRequest.metrics schema missing")
	}
	op, ok := metrics["items"].(map[string]any)["properties"].(map[string]any)["op"].(map[string]any)
	if !ok {
		t.Fatal("QueryRequest.metrics.items.op schema missing")
	}
	raw, ok := op["enum"].([]any)
	if !ok {
		t.Fatal("QueryRequest.metrics.items.op enum missing")
	}
	got := make([]string, 0, len(raw))
	for _, value := range raw {
		got = append(got, value.(string))
	}
	want := append([]string(nil), Aggregations...)
	sort.Strings(want)
	sort.Strings(got)
	if len(want) != len(got) {
		t.Fatalf("aggregation drift: contract %v != Go %v", got, want)
	}
	for i := range want {
		if want[i] != got[i] {
			t.Fatalf("aggregation drift: contract %v != Go %v", got, want)
		}
	}
}
