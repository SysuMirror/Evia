package reporting

import (
	"testing"

	"github.com/howtomakeaname/Evia/backend/internal/analysis"
)

func fUnits(pairs ...string) map[string]string {
	units := map[string]string{}
	for i := 0; i < len(pairs); i += 2 {
		units[pairs[i]] = pairs[i+1]
	}
	return units
}

func TestInferFormulaUnit(t *testing.T) {
	idA, idB := fUUID(1), fUUID(2)
	keyA, keyB := formulaRefKey(fRef(idA, 1)), formulaRefKey(fRef(idB, 1))
	metricA, metricB := fMetric(idA, 1), fMetric(idB, 1)
	for _, tc := range []struct {
		name    string
		expr    *FormulaExpression
		units   map[string]string
		want    string
		wantErr bool
	}{
		{"constant", fConst(3), map[string]string{}, "number", false},
		{"same unit add", fBinary(FormulaAdd, metricA, metricB), fUnits(keyA, "event", keyB, "event"), "event", false},
		{"mixed unit add", fBinary(FormulaAdd, metricA, metricB), fUnits(keyA, "event", keyB, "byte"), "", true},
		{"scale multiply", fBinary(FormulaMultiply, metricA, metricB), fUnits(keyA, "number", keyB, "byte"), "byte", false},
		{"dimensioned multiply rejected", fBinary(FormulaMultiply, metricA, metricB), fUnits(keyA, "event", keyB, "byte"), "", true},
		{"ratio divide", fBinary(FormulaDivide, metricA, metricB), fUnits(keyA, "event", keyB, "event"), "number", false},
		{"scalar divide", fBinary(FormulaDivide, metricA, metricB), fUnits(keyA, "byte", keyB, "number"), "byte", false},
		{"inverse divide rejected", fBinary(FormulaDivide, metricA, metricB), fUnits(keyA, "number", keyB, "byte"), "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := InferFormulaUnit(*tc.expr, tc.units)
			if tc.wantErr {
				if err == nil {
					t.Fatal("expected unit error")
				}
				return
			}
			if err != nil || got != tc.want {
				t.Fatalf("got %q err %v want %q", got, err, tc.want)
			}
		})
	}
}

func TestVerifyFormulaUnit(t *testing.T) {
	idA, idB := fUUID(1), fUUID(2)
	graph := FormulaGraph{Units: fUnits(
		formulaRefKey(fRef(idA, 1)), "event",
		formulaRefKey(fRef(idB, 1)), "event",
	)}
	ratio := FormulaDefinition{Expression: fRatio(idA, idB), Unit: "number"}
	if err := VerifyFormulaUnit(ratio, graph); err != nil {
		t.Fatalf("ratio derives number: %v", err)
	}
	wrong := ratio
	wrong.Unit = "event"
	if err := VerifyFormulaUnit(wrong, graph); err == nil {
		t.Fatal("expected declared unit mismatch")
	}
}

func TestEvaluateFormulaArithmetic(t *testing.T) {
	idA, idB := fUUID(1), fUUID(2)
	refA, refB := fRef(idA, 1), fRef(idB, 1)
	expr := fBinary(FormulaDivide, fMetric(idA, 1), fBinary(FormulaSubtract, fMetric(idB, 1), fConst(2)))
	values := map[string]*float64{
		formulaRefKey(refA): fFloat(10),
		formulaRefKey(refB): fFloat(7),
	}
	got := evaluateFormula(expr, values)
	if got == nil || *got != 2 {
		t.Fatalf("expected 2, got %v", got)
	}
}

func TestEvaluateFormulaDivideByZeroIsNull(t *testing.T) {
	idA, idB := fUUID(1), fUUID(2)
	expr := fBinary(FormulaDivide, fMetric(idA, 1), fMetric(idB, 1))
	values := map[string]*float64{
		formulaRefKey(fRef(idA, 1)): fFloat(10),
		formulaRefKey(fRef(idB, 1)): fFloat(0),
	}
	if got := evaluateFormula(expr, values); got != nil {
		t.Fatalf("divide by zero must be null, got %v", *got)
	}
}

func TestEvaluateFormulaNullPropagation(t *testing.T) {
	idA, idB := fUUID(1), fUUID(2)
	for _, op := range []string{FormulaAdd, FormulaSubtract, FormulaMultiply, FormulaDivide} {
		expr := fBinary(op, fMetric(idA, 1), fMetric(idB, 1))
		// Operand B is absent from the row, which alignment represents as null.
		values := map[string]*float64{formulaRefKey(fRef(idA, 1)): fFloat(10)}
		if got := evaluateFormula(expr, values); got != nil {
			t.Fatalf("%s with null operand must be null, got %v", op, *got)
		}
	}
}

func TestEvaluateFormulaOverflowIsNull(t *testing.T) {
	idA, idB := fUUID(1), fUUID(2)
	expr := fBinary(FormulaMultiply, fMetric(idA, 1), fMetric(idB, 1))
	values := map[string]*float64{
		formulaRefKey(fRef(idA, 1)): fFloat(analysis.MaxSafeInteger),
		formulaRefKey(fRef(idB, 1)): fFloat(2),
	}
	if got := evaluateFormula(expr, values); got != nil {
		t.Fatalf("overflow must be null, got %v", *got)
	}
}

func TestEvaluateFormulaConstantAndNegative(t *testing.T) {
	id := fUUID(1)
	expr := fBinary(FormulaSubtract, fConst(1), fMetric(id, 1))
	values := map[string]*float64{formulaRefKey(fRef(id, 1)): fFloat(4)}
	got := evaluateFormula(expr, values)
	if got == nil || *got != -3 {
		t.Fatalf("expected -3, got %v", got)
	}
}
