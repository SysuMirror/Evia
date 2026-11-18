//go:build integration

package analysis

import (
	"encoding/json"
	"testing"
)

// TestClickHouseNegationFiltersKeepMissingExcluded verifies the negation
// operators against the real migrated view, focusing on the guard invariant:
// a negated predicate must only invert the inner comparison while still
// excluding absent/null/non-typed values, never folding missing rows back in.
func TestClickHouseNegationFiltersKeepMissingExcluded(t *testing.T) {
	client := clickHouseDatabase(t)
	rows := []map[string]any{
		fact("neg-1", "negated", "u1", "", "source-one", 0, map[string]any{"name": "apple", "score": 10, "tags": []string{"red", "sweet"}}),
		fact("neg-2", "negated", "u2", "", "source-one", 1, map[string]any{"name": "apricot", "score": 20, "tags": []string{"red"}}),
		fact("neg-3", "negated", "u3", "", "source-one", 2, map[string]any{"name": "banana", "score": 30, "tags": []string{"yellow", "sweet"}}),
		fact("neg-4", "negated", "u4", "", "source-one", 3, map[string]any{"name": "cherry", "score": 10, "tags": []string{}}),
		// neg-5 omits name/score/tags entirely: every negated predicate must exclude it.
		fact("neg-5", "negated", "u5", "", "source-one", 4, map[string]any{}),
		// neg-6 has name/score but no tags: array negation must exclude it via the presence guard.
		fact("neg-6", "negated", "u6", "", "source-one", 5, map[string]any{"name": "avocado", "score": 20}),
	}
	seedRows(t, client, rows)

	base := request(t, baseRequest)
	base.EventVersions = []EventVersion{{EventKey: "negated", SchemaVersion: 1}}
	base.Metrics = []Metric{{Op: "count", Accuracy: "exact"}}
	field := func(key, kind string) *Field {
		return &Field{Kind: "property", EventKey: "negated", SchemaVersion: 1, Key: key, ResolvedType: kind}
	}
	count := func(op string, f *Field, value any) float64 {
		r := base
		r.Filter = &Filter{Op: op, Field: f, Value: value}
		got := trendPlanRows(t, client, r, "event_facts")
		if len(got) != 1 {
			t.Fatalf("%s expected one aggregate row: %v", op, got)
		}
		number, ok := got[0]["metric_0"].(json.Number)
		if !ok {
			t.Fatalf("%s missing metric_0: %v", op, got[0])
		}
		valueFloat, e := number.Float64()
		if e != nil {
			t.Fatal(e)
		}
		return valueFloat
	}

	for _, tc := range []struct {
		name  string
		op    string
		field *Field
		value any
		want  float64
	}{
		// Positive controls first, proving the seeded shape and the counterpart semantics.
		{"string contains baseline", "contains", field("name", "string"), "ap", 2},
		{"number in baseline", "in", field("score", "number"), []any{json.Number("10"), json.Number("20")}, 4},
		{"array contains baseline", "contains", field("tags", "array:string"), "sweet", 2},
		// Negations.
		{"string not_contains", "not_contains", field("name", "string"), "ap", 3},
		{"string not_prefix", "not_prefix", field("name", "string"), "ap", 3},
		{"number not_in", "not_in", field("score", "number"), []any{json.Number("10"), json.Number("20")}, 1},
		{"array not_contains includes empty array but excludes missing tag", "not_contains", field("tags", "array:string"), "sweet", 2},
		{"empty not_in matches every present row, never missing", "not_in", field("score", "number"), []any{}, 5},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := count(tc.op, tc.field, tc.value); got != tc.want {
				t.Fatalf("%s = %v want %v", tc.name, got, tc.want)
			}
		})
	}
}
