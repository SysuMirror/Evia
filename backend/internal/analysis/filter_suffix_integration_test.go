//go:build integration

package analysis

import (
	"encoding/json"
	"testing"
)

// TestClickHouseSuffixFiltersKeepMissingExcluded verifies suffix/not_suffix
// against the real migrated view, including the guard invariant: a negated
// suffix must only invert the inner endsWith test while still excluding rows
// whose key is absent, never folding missing rows back into the result.
func TestClickHouseSuffixFiltersKeepMissingExcluded(t *testing.T) {
	client := clickHouseDatabase(t)
	rows := []map[string]any{
		fact("suf-1", "suffixkey", "u1", "", "source-one", 0, map[string]any{"file": "report.csv"}),
		fact("suf-2", "suffixkey", "u2", "", "source-one", 1, map[string]any{"file": "data.csv"}),
		fact("suf-3", "suffixkey", "u3", "", "source-one", 2, map[string]any{"file": "extra.csv"}),
		fact("suf-4", "suffixkey", "u4", "", "source-one", 3, map[string]any{"file": "notes.txt"}),
		fact("suf-5", "suffixkey", "u5", "", "source-one", 4, map[string]any{"file": "report.pdf"}),
		// suf-6 omits the file key: both suffix and not_suffix must exclude it.
		fact("suf-6", "suffixkey", "u6", "", "source-one", 5, map[string]any{}),
	}
	seedRows(t, client, rows)

	base := request(t, baseRequest)
	base.EventVersions = []EventVersion{{EventKey: "suffixkey", SchemaVersion: 1}}
	base.Metrics = []Metric{{Op: OpCount, Accuracy: AccuracyExact}}
	field := &Field{Kind: "property", EventKey: "suffixkey", SchemaVersion: 1, Key: "file", ResolvedType: "string"}
	count := func(op string) float64 {
		r := base
		r.Filter = &Filter{Op: op, Field: field, Value: ".csv"}
		result := execute(t, client, r)
		if len(result.Rows) != 1 {
			t.Fatalf("%s expected one aggregate row: %v", op, result.Rows)
		}
		number, ok := result.Rows[0]["metric_0"].(json.Number)
		if !ok {
			t.Fatalf("%s missing metric_0: %v", op, result.Rows[0])
		}
		value, e := number.Float64()
		if e != nil {
			t.Fatal(e)
		}
		return value
	}

	for _, tc := range []struct {
		name, op string
		want     float64
	}{
		{"suffix matches the three csv files", "suffix", 3},
		{"not_suffix keeps the two present non-csv but excludes missing", "not_suffix", 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := count(tc.op); got != tc.want {
				t.Fatalf("%s = %v want %v", tc.name, got, tc.want)
			}
		})
	}
}
