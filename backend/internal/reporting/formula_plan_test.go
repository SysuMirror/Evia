package reporting

import (
	"testing"

	"github.com/howtomakeaname/Evia/backend/internal/analysis"
)

func TestEvaluateFormulaPlanNestedAndSharedLeaves(t *testing.T) {
	idA, idB, idC := fUUID(1), fUUID(2), fUUID(3)
	refA, refB, refC := fRef(idA, 1), fRef(idB, 1), fRef(idC, 1)
	root := fRef(fUUID(9), 1)
	columns := []analysis.Column{{Key: formulaValueColumn, Type: "number"}}
	atomicResults := map[MetricReference]analysis.Result{
		refA: fResult(columns, map[string]any{formulaValueColumn: 10.0}),
		refB: fResult(columns, map[string]any{formulaValueColumn: 2.0}),
	}
	// C = A / B = 5; root = C * A = 50. A is shared across both levels.
	plan := FormulaExecutionPlan{
		Root: root,
		Composite: map[MetricReference]FormulaDefinition{
			root: {Expression: *fBinary(FormulaMultiply, fMetric(idC, 1), fMetric(idA, 1)), Unit: "event"},
			refC: {Expression: fRatio(idA, idB), Unit: "number"},
		},
		AtomicRequests: map[MetricReference]analysis.Request{
			refA: {Kind: "trend"},
			refB: {Kind: "trend"},
		},
	}
	out, e := EvaluateFormulaPlan(plan, atomicResults)
	if e != nil {
		t.Fatal(e)
	}
	if out.Rows[0][formulaValueColumn] != 50.0 {
		t.Fatalf("expected nested evaluation 50, got %#v", out.Rows[0][formulaValueColumn])
	}
}

func TestEvaluateFormulaPlanRejectsMissingNode(t *testing.T) {
	idA, idC := fUUID(1), fUUID(3)
	refA := fRef(idA, 1)
	root := fRef(fUUID(9), 1)
	columns := []analysis.Column{{Key: formulaValueColumn, Type: "number"}}
	plan := FormulaExecutionPlan{
		Root: root,
		// Root references C, but the composite node for C is absent.
		Composite: map[MetricReference]FormulaDefinition{
			root: {Expression: *fMetric(idC, 1), Unit: "event"},
		},
		AtomicRequests: map[MetricReference]analysis.Request{},
	}
	atomicResults := map[MetricReference]analysis.Result{
		refA: fResult(columns, map[string]any{formulaValueColumn: 1.0}),
	}
	if _, e := EvaluateFormulaPlan(plan, atomicResults); e == nil {
		t.Fatal("expected missing plan node rejection")
	}
}

func TestEvaluateFormulaPlanRejectsCycle(t *testing.T) {
	idA, idB := fUUID(1), fUUID(2)
	refA, refB := fRef(idA, 1), fRef(idB, 1)
	root := fRef(fUUID(9), 1)
	// A corrupt plan where two composite nodes reference each other. Publish
	// validation prevents this, but evaluation must still fail closed.
	plan := FormulaExecutionPlan{
		Root: root,
		Composite: map[MetricReference]FormulaDefinition{
			root: {Expression: *fMetric(idA, 1), Unit: "event"},
			refA: {Expression: *fMetric(idB, 1), Unit: "event"},
			refB: {Expression: *fMetric(idA, 1), Unit: "event"},
		},
		AtomicRequests: map[MetricReference]analysis.Request{},
	}
	if _, e := EvaluateFormulaPlan(plan, map[MetricReference]analysis.Result{}); e == nil {
		t.Fatal("expected cycle rejection")
	}
}
