package exporting

import (
	"bytes"
	"encoding/json"
	"math"
	"testing"

	"github.com/howtomakeaname/Evia/backend/internal/analysis"
	"github.com/howtomakeaname/Evia/backend/internal/platform/fault"
	"github.com/parquet-go/parquet-go"
)

func parquetSampleResult() analysis.Result {
	return analysis.Result{
		Columns: []analysis.Column{
			{Key: "subject", Type: "string"},
			{Key: "seen_at", Type: "datetime"},
			{Key: "count", Type: "integer"},
			{Key: "ratio", Type: "number"},
			{Key: "active", Type: "boolean"},
			{Key: "tags", Type: "array:string"},
			{Key: "scores", Type: "array:number"},
			{Key: "detail", Type: "object"},
		},
		Rows: []map[string]any{
			{
				"subject": "user-1",
				"seen_at": "2026-11-19T08:00:00Z",
				"count":   json.Number("42"),
				"ratio":   json.Number("1e2"),
				"active":  true,
				"tags":    []any{"a", "b"},
				"scores":  []any{json.Number("1.5"), json.Number("2.5")},
				"detail":  map[string]any{"plan": "pro"},
			},
			{
				"subject": "user-2",
				"seen_at": "2026-11-19T09:00:00Z",
				"count":   int64(7),
				"ratio":   1.25,
				"active":  false,
				"tags":    nil,
				"scores":  []any{},
				"detail":  nil,
			},
		},
	}
}

func readParquetRowsN(t *testing.T, payload []byte, wantRows int64) []map[string]any {
	t.Helper()
	file, err := parquet.OpenFile(bytes.NewReader(payload), int64(len(payload)))
	if err != nil {
		t.Fatal(err)
	}
	var total int64
	for _, group := range file.RowGroups() {
		total += group.NumRows()
	}
	if total != wantRows {
		t.Fatalf("unexpected row count: got %d want %d", total, wantRows)
	}
	reader := parquet.NewReader(bytes.NewReader(payload))
	defer reader.Close()
	rows := make([]map[string]any, 0, 2)
	for {
		row := map[string]any{}
		if err := reader.Read(&row); err != nil {
			break
		}
		rows = append(rows, row)
	}
	return rows
}

func TestRenderParquetStrongTypesAndRoundTrip(t *testing.T) {
	t.Parallel()
	payload, err := renderParquet(parquetSampleResult())
	if err != nil {
		t.Fatal(err)
	}
	if len(payload) == 0 || !bytes.HasPrefix(payload, []byte("PAR1")) {
		t.Fatalf("payload is not a parquet file: %d bytes", len(payload))
	}
	rows := readParquetRowsN(t, payload, 2)
	if len(rows) != 2 {
		t.Fatalf("want 2 rows, got %d", len(rows))
	}
	first := rows[0]
	if first["subject"] != "user-1" || first["seen_at"] != "2026-11-19T08:00:00Z" {
		t.Fatalf("string/datetime columns wrong: %+v", first)
	}
	if first["count"] != int64(42) {
		t.Fatalf("integer column must be INT64, got %T %v", first["count"], first["count"])
	}
	if ratio, ok := first["ratio"].(float64); !ok || ratio != 100 {
		t.Fatalf("number column must be DOUBLE 100, got %T %v", first["ratio"], first["ratio"])
	}
	if first["active"] != true {
		t.Fatalf("boolean column wrong: %+v", first["active"])
	}
	tags, ok := first["tags"].([]any)
	if !ok || len(tags) != 2 || tags[0] != "a" || tags[1] != "b" {
		t.Fatalf("string LIST wrong: %+v", first["tags"])
	}
	scores, ok := first["scores"].([]any)
	if !ok || len(scores) != 2 || scores[0].(float64) != 1.5 {
		t.Fatalf("double LIST wrong: %+v", first["scores"])
	}
	detail, ok := first["detail"].(string)
	if !ok || detail != `{"plan":"pro"}` {
		t.Fatalf("object column must be compact JSON string, got %+v", first["detail"])
	}
	if rows[1]["tags"] != nil || rows[1]["detail"] != nil {
		t.Fatalf("nil cells must stay null: %+v", rows[1])
	}
}

func TestRenderParquetStringColumnKeepsNumberLexicalForm(t *testing.T) {
	t.Parallel()
	result := analysis.Result{
		Columns: []analysis.Column{{Key: "token", Type: "string"}},
		Rows: []map[string]any{
			{"token": json.Number("1e2")},
			{"token": json.Number("1.00")},
			{"token": json.Number("-0.0")},
		},
	}
	payload, err := renderParquet(result)
	if err != nil {
		t.Fatal(err)
	}
	rows := readParquetRowsN(t, payload, 3)
	for i, want := range []string{"1e2", "1.00", "-0.0"} {
		if rows[i]["token"] != want {
			t.Fatalf("lexical form %d lost: got %v", i, rows[i]["token"])
		}
	}
}

func TestRenderParquetIntegerOverflowAndMismatchFailClosed(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		column analysis.Column
		cell   any
	}{
		{"float form in integer column", analysis.Column{Key: "c", Type: "integer"}, json.Number("-0.0")},
		{"uint64 overflow", analysis.Column{Key: "c", Type: "integer"}, uint64(math.MaxUint64)},
		{"string in boolean column", analysis.Column{Key: "c", Type: "boolean"}, "true"},
		{"structured in string column", analysis.Column{Key: "c", Type: "string"}, []any{"x"}},
		{"unknown column type", analysis.Column{Key: "c", Type: "account_disabled"}, "x"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			result := analysis.Result{
				Columns: []analysis.Column{tc.column},
				Rows:    []map[string]any{{"c": tc.cell}},
			}
			if _, err := renderParquet(result); err == nil || fault.Public(err).Code != "EXPORT_PARQUET_UNSUPPORTED" {
				t.Fatalf("want EXPORT_PARQUET_UNSUPPORTED, got %v", err)
			}
		})
	}
}

func TestRenderParquetMixedGenericArrayFallsBackToJSON(t *testing.T) {
	t.Parallel()
	result := analysis.Result{
		Columns: []analysis.Column{{Key: "mix", Type: "array"}},
		Rows: []map[string]any{
			{"mix": []any{"a", int64(1)}},
			{"mix": []any{true, "b"}},
		},
	}
	payload, err := renderParquet(result)
	if err != nil {
		t.Fatal(err)
	}
	rows := readParquetRowsN(t, payload, 2)
	if rows[0]["mix"] != `["a",1]` || rows[1]["mix"] != `[true,"b"]` {
		t.Fatalf("mixed array must degrade to JSON strings, got %+v", rows)
	}
}

func TestRenderParquetNoColumnsAndEmptyRows(t *testing.T) {
	t.Parallel()
	if _, err := renderParquet(analysis.Result{}); err == nil || fault.Public(err).Code != "EXPORT_PARQUET_UNSUPPORTED" {
		t.Fatalf("empty columns must fail closed, got %v", err)
	}
	// Schema-only file for zero rows stays valid and reads back no rows.
	payload, err := renderParquet(analysis.Result{Columns: []analysis.Column{{Key: "k", Type: "string"}}})
	if err != nil {
		t.Fatal(err)
	}
	file, err := parquet.OpenFile(bytes.NewReader(payload), int64(len(payload)))
	if err != nil {
		t.Fatal(err)
	}
	if len(file.RowGroups()) != 0 {
		t.Fatalf("zero-row result must produce no row groups, got %d", len(file.RowGroups()))
	}
}

func TestNormalizeFormatAcceptsParquet(t *testing.T) {
	t.Parallel()
	got, err := NormalizeFormat(FormatParquet)
	if err != nil || got != FormatParquet {
		t.Fatalf("parquet normalization wrong: %q %v", got, err)
	}
}
