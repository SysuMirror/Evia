package reporting

import (
	"encoding/json"
	"testing"

	"github.com/howtomakeaname/Evia/backend/internal/analysis"
)

func fResult(columns []analysis.Column, rows ...map[string]any) analysis.Result {
	return analysis.Result{Columns: columns, Rows: rows, Metadata: analysis.Metadata{Accuracy: "exact", Coverage: analysis.Coverage{State: "provisional"}}}
}

func TestEvaluateFormulaWholeSegment(t *testing.T) {
	idA, idB := fUUID(1), fUUID(2)
	refA, refB := fRef(idA, 1), fRef(idB, 1)
	columns := []analysis.Column{{Key: formulaValueColumn, Type: "number"}}
	leaves := map[MetricReference]analysis.Result{
		refA: fResult(columns, map[string]any{formulaValueColumn: json.Number("10")}),
		refB: fResult(columns, map[string]any{formulaValueColumn: json.Number("2")}),
	}
	out, err := EvaluateFormulaResult(FormulaDefinition{Expression: fRatio(idA, idB), Unit: "number"}, leaves)
	if err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	if len(out.Rows) != 1 {
		t.Fatalf("rows %+v", out.Rows)
	}
	value, ok := out.Rows[0][formulaValueColumn].(float64)
	if !ok || value != 5 {
		t.Fatalf("expected 5, got %#v", out.Rows[0][formulaValueColumn])
	}
}

func TestEvaluateFormulaAlignsTrendBuckets(t *testing.T) {
	idA, idB := fUUID(1), fUUID(2)
	refA, refB := fRef(idA, 1), fRef(idB, 1)
	columns := []analysis.Column{{Key: formulaBucketColumn, Type: "datetime"}, {Key: formulaValueColumn, Type: "number"}}
	t1, t2, t3 := "2026-01-01T00:00:00Z", "2026-01-01T01:00:00Z", "2026-01-01T02:00:00Z"
	leaves := map[MetricReference]analysis.Result{
		refA: fResult(columns,
			map[string]any{formulaBucketColumn: t1, formulaValueColumn: 10.0},
			map[string]any{formulaBucketColumn: t2, formulaValueColumn: 20.0}),
		refB: fResult(columns,
			map[string]any{formulaBucketColumn: t1, formulaValueColumn: 2.0},
			map[string]any{formulaBucketColumn: t2, formulaValueColumn: 4.0},
			map[string]any{formulaBucketColumn: t3, formulaValueColumn: 5.0}),
	}
	out, err := EvaluateFormulaResult(FormulaDefinition{Expression: fRatio(idA, idB), Unit: "number"}, leaves)
	if err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	if len(out.Rows) != 3 {
		t.Fatalf("expected union of three buckets, got %+v", out.Rows)
	}
	if out.Rows[0][formulaBucketColumn] != t1 || out.Rows[2][formulaBucketColumn] != t3 {
		t.Fatalf("bucket order %+v", out.Rows)
	}
	if out.Rows[0][formulaValueColumn] != 5.0 || out.Rows[1][formulaValueColumn] != 5.0 {
		t.Fatalf("computed ratios %+v", out.Rows)
	}
	// Leaf A has no t3 row: the operand is null, so the result is null not zero.
	if out.Rows[2][formulaValueColumn] != nil {
		t.Fatalf("missing operand must be null, got %#v", out.Rows[2][formulaValueColumn])
	}
}

func TestEvaluateFormulaAlignsGroupDimensions(t *testing.T) {
	idA, idB := fUUID(1), fUUID(2)
	refA, refB := fRef(idA, 1), fRef(idB, 1)
	columns := []analysis.Column{
		{Key: "group_0", Type: "string", Label: "channel"},
		{Key: "group_0_present", Type: "boolean"},
		{Key: formulaValueColumn, Type: "number"},
	}
	leaves := map[MetricReference]analysis.Result{
		refA: fResult(columns,
			map[string]any{"group_0": "x", "group_0_present": true, formulaValueColumn: 8.0},
			map[string]any{"group_0": "y", "group_0_present": true, formulaValueColumn: 16.0}),
		refB: fResult(columns,
			map[string]any{"group_0": "x", "group_0_present": true, formulaValueColumn: 2.0}),
	}
	out, err := EvaluateFormulaResult(FormulaDefinition{Expression: fRatio(idA, idB), Unit: "number"}, leaves)
	if err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	if len(out.Rows) != 2 {
		t.Fatalf("rows %+v", out.Rows)
	}
	if out.Rows[0]["group_0"] != "x" || out.Rows[0][formulaValueColumn] != 4.0 {
		t.Fatalf("x ratio %+v", out.Rows[0])
	}
	if out.Rows[0]["group_0_present"] != true {
		t.Fatal("presence column must be preserved")
	}
	if out.Rows[1]["group_0"] != "y" || out.Rows[1][formulaValueColumn] != nil {
		t.Fatalf("y must align to null: %+v", out.Rows[1])
	}
}

func TestEvaluateFormulaRejectsMismatchedShape(t *testing.T) {
	idA, idB := fUUID(1), fUUID(2)
	leaves := map[MetricReference]analysis.Result{
		fRef(idA, 1): fResult([]analysis.Column{{Key: formulaValueColumn, Type: "number"}}, map[string]any{formulaValueColumn: 1.0}),
		fRef(idB, 1): fResult([]analysis.Column{{Key: "group_0", Type: "string"}, {Key: formulaValueColumn, Type: "number"}}, map[string]any{"group_0": "x", formulaValueColumn: 1.0}),
	}
	if _, err := EvaluateFormulaResult(FormulaDefinition{Expression: fRatio(idA, idB), Unit: "number"}, leaves); err == nil {
		t.Fatal("expected shape mismatch rejection")
	}
}

func TestEvaluateFormulaRejectsMissingValueColumn(t *testing.T) {
	idA, idB := fUUID(1), fUUID(2)
	leaves := map[MetricReference]analysis.Result{
		fRef(idA, 1): fResult([]analysis.Column{{Key: "other", Type: "number"}}, map[string]any{"other": 1.0}),
		fRef(idB, 1): fResult([]analysis.Column{{Key: formulaValueColumn, Type: "number"}}, map[string]any{formulaValueColumn: 1.0}),
	}
	if _, err := EvaluateFormulaResult(FormulaDefinition{Expression: fRatio(idA, idB), Unit: "number"}, leaves); err == nil {
		t.Fatal("expected missing value column rejection")
	}
}

func TestEvaluateFormulaRejectsNonNumericValue(t *testing.T) {
	idA, idB := fUUID(1), fUUID(2)
	columns := []analysis.Column{{Key: formulaValueColumn, Type: "number"}}
	leaves := map[MetricReference]analysis.Result{
		fRef(idA, 1): fResult(columns, map[string]any{formulaValueColumn: "not-a-number"}),
		fRef(idB, 1): fResult(columns, map[string]any{formulaValueColumn: 1.0}),
	}
	if _, err := EvaluateFormulaResult(FormulaDefinition{Expression: fRatio(idA, idB), Unit: "number"}, leaves); err == nil {
		t.Fatal("expected non-numeric rejection")
	}
}

func TestEvaluateFormulaNullValuePropagates(t *testing.T) {
	idA, idB := fUUID(1), fUUID(2)
	columns := []analysis.Column{{Key: formulaValueColumn, Type: "number"}}
	leaves := map[MetricReference]analysis.Result{
		fRef(idA, 1): fResult(columns, map[string]any{formulaValueColumn: nil}),
		fRef(idB, 1): fResult(columns, map[string]any{formulaValueColumn: 2.0}),
	}
	out, err := EvaluateFormulaResult(FormulaDefinition{Expression: fRatio(idA, idB), Unit: "number"}, leaves)
	if err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	if out.Rows[0][formulaValueColumn] != nil {
		t.Fatalf("null operand must propagate, got %#v", out.Rows[0][formulaValueColumn])
	}
}

func TestEvaluateFormulaMergesConservativeMetadata(t *testing.T) {
	idA, idB := fUUID(1), fUUID(2)
	columns := []analysis.Column{{Key: formulaValueColumn, Type: "number"}}
	leaves := map[MetricReference]analysis.Result{
		fRef(idA, 1): {Columns: columns, Rows: []map[string]any{{formulaValueColumn: 1.0}}, Metadata: analysis.Metadata{Accuracy: "exact"}},
		fRef(idB, 1): {Columns: columns, Rows: []map[string]any{{formulaValueColumn: 2.0}}, Metadata: analysis.Metadata{Accuracy: "approximate"}},
	}
	out, err := EvaluateFormulaResult(FormulaDefinition{Expression: fRatio(idA, idB), Unit: "number"}, leaves)
	if err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	if out.Metadata.Accuracy != "approximate" {
		t.Fatalf("metadata %+v", out.Metadata)
	}
}

func TestEvaluateFormulaRejectsLeafCountMismatch(t *testing.T) {
	idA, idB := fUUID(1), fUUID(2)
	columns := []analysis.Column{{Key: formulaValueColumn, Type: "number"}}
	leaves := map[MetricReference]analysis.Result{
		fRef(idA, 1): fResult(columns, map[string]any{formulaValueColumn: 1.0}),
	}
	if _, err := EvaluateFormulaResult(FormulaDefinition{Expression: fRatio(idA, idB), Unit: "number"}, leaves); err == nil {
		t.Fatal("expected leaf count mismatch rejection")
	}
}
