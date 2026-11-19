package analysis

import (
	"encoding/json"
	"sort"
	"testing"

	"github.com/howtomakeaname/Evia/backend/internal/gen/wire"
)

// TestIntervalVocabularyMatchesContract guards the trend interval closed set
// against the QueryRequest.interval OpenAPI enum.
func TestIntervalVocabularyMatchesContract(t *testing.T) {
	var bundle struct {
		Defs map[string]map[string]any `json:"$defs"`
	}
	if err := json.Unmarshal(wire.Schemas, &bundle); err != nil {
		t.Fatal(err)
	}
	property, ok := bundle.Defs["QueryRequest"]["properties"].(map[string]any)["interval"].(map[string]any)
	if !ok {
		t.Fatal("QueryRequest.interval schema missing")
	}
	raw, ok := property["enum"].([]any)
	if !ok {
		t.Fatal("QueryRequest.interval enum missing")
	}
	got := make([]string, 0, len(raw))
	for _, value := range raw {
		got = append(got, value.(string))
	}
	want := append([]string(nil), Intervals...)
	sort.Strings(want)
	sort.Strings(got)
	if len(want) != len(got) {
		t.Fatalf("interval drift: contract %v != Go %v", got, want)
	}
	for i := range want {
		if want[i] != got[i] {
			t.Fatalf("interval drift: contract %v != Go %v", got, want)
		}
	}
}

// TestRetentionGranularityVocabularyMatchesContract guards the retention
// granularity closed set against the nested QueryRequest.retention.granularity
// OpenAPI enum.
func TestRetentionGranularityVocabularyMatchesContract(t *testing.T) {
	var bundle struct {
		Defs map[string]map[string]any `json:"$defs"`
	}
	if err := json.Unmarshal(wire.Schemas, &bundle); err != nil {
		t.Fatal(err)
	}
	retention, ok := bundle.Defs["QueryRequest"]["properties"].(map[string]any)["retention"].(map[string]any)
	if !ok {
		t.Fatal("QueryRequest.retention schema missing")
	}
	property, ok := retention["properties"].(map[string]any)["granularity"].(map[string]any)
	if !ok {
		t.Fatal("QueryRequest.retention.granularity schema missing")
	}
	raw, ok := property["enum"].([]any)
	if !ok {
		t.Fatal("QueryRequest.retention.granularity enum missing")
	}
	got := make([]string, 0, len(raw))
	for _, value := range raw {
		got = append(got, value.(string))
	}
	want := append([]string(nil), RetentionGranularities...)
	sort.Strings(want)
	sort.Strings(got)
	if len(want) != len(got) {
		t.Fatalf("retention granularity drift: contract %v != Go %v", got, want)
	}
	for i := range want {
		if want[i] != got[i] {
			t.Fatalf("retention granularity drift: contract %v != Go %v", got, want)
		}
	}
}
