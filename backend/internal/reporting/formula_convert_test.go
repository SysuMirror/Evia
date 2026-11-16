package reporting

import (
	"testing"
)

func fConvert(target string, child *FormulaExpression) *FormulaExpression {
	return &FormulaExpression{Op: FormulaConvert, Left: child, TargetUnit: target}
}

func TestNormalizeFormulaConvert(t *testing.T) {
	id := fUUID(1)
	valid := FormulaDefinition{
		Expression: *fConvert("second", fMetric(id, 1)),
		Unit:       "second",
	}
	normalized, e := NormalizeFormulaDefinition(valid)
	if e != nil {
		t.Fatalf("valid convert normalized: %v", e)
	}
	convertNode := normalized.Expression
	if convertNode.Op != FormulaConvert || convertNode.TargetUnit != "second" || convertNode.Left == nil {
		t.Fatalf("convert node malformed: %+v", convertNode)
	}
	if convertNode.Right != nil || convertNode.Metric != nil || convertNode.Value != nil {
		t.Fatalf("convert node must only carry left and target: %+v", convertNode)
	}

	for _, tc := range []struct {
		name string
		expr FormulaExpression
		unit string
	}{
		{"missing target", FormulaExpression{Op: FormulaConvert, Left: fMetric(id, 1)}, "second"},
		{"unknown target", *fConvert("byte", fMetric(id, 1)), "byte"},
		{"missing child", FormulaExpression{Op: FormulaConvert, TargetUnit: "second"}, "second"},
		{"convert carrying right", *fBinary(FormulaConvert, fMetric(id, 1), fMetric(id, 1)), "second"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, e := NormalizeFormulaDefinition(FormulaDefinition{Expression: tc.expr, Unit: tc.unit}); e == nil {
				t.Fatal("expected malformed convert rejection")
			}
		})
	}
}

func TestInferFormulaUnitConvert(t *testing.T) {
	id := fUUID(1)
	key := formulaRefKey(fRef(id, 1))
	for _, tc := range []struct {
		name    string
		target  string
		source  string
		want    string
		wantErr bool
	}{
		{"millisecond to second", "second", "millisecond", "second", false},
		{"second to millisecond", "millisecond", "second", "millisecond", false},
		{"byte cannot convert to second", "second", "byte", "", true},
		{"event cannot convert", "millisecond", "event", "", true},
		{"identity millisecond rejected", "millisecond", "millisecond", "", true},
		{"dimensionless number rejected", "second", "number", "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			expr := fConvert(tc.target, fMetric(id, 1))
			got, err := InferFormulaUnit(*expr, fUnits(key, tc.source))
			if tc.wantErr {
				if err == nil {
					t.Fatal("expected unit incompatibility")
				}
				return
			}
			if err != nil || got != tc.want {
				t.Fatalf("got %q err %v want %q", got, err, tc.want)
			}
		})
	}
}

func TestVerifyFormulaUnitConvertMatchesDeclaration(t *testing.T) {
	id := fUUID(1)
	key := formulaRefKey(fRef(id, 1))
	expr := fConvert("second", fMetric(id, 1))
	graph := FormulaGraph{Units: fUnits(key, "millisecond")}
	good := FormulaDefinition{Expression: *expr, Unit: "second"}
	if e := VerifyFormulaUnit(good, graph); e != nil {
		t.Fatalf("declared second should match derived second: %v", e)
	}
	bad := FormulaDefinition{Expression: *expr, Unit: "millisecond"}
	if e := VerifyFormulaUnit(bad, graph); e == nil {
		t.Fatal("declared millisecond must not match a second-producing convert")
	}
}

func TestEvaluateFormulaConvertFactors(t *testing.T) {
	id := fUUID(1)
	ref := fRef(id, 1)
	for _, tc := range []struct {
		name   string
		target string
		input  float64
		want   float64
	}{
		{"millisecond to second", "second", 2500, 2.5},
		{"second to millisecond", "millisecond", 3, 3000},
	} {
		t.Run(tc.name, func(t *testing.T) {
			expr := fConvert(tc.target, fMetric(id, 1))
			values := map[string]*float64{formulaRefKey(ref): fFloat(tc.input)}
			got := evaluateFormula(expr, values)
			if got == nil || *got != tc.want {
				t.Fatalf("expected %v, got %v", tc.want, got)
			}
		})
	}
}

func TestEvaluateFormulaConvertNullAndNested(t *testing.T) {
	id := fUUID(1)
	ref := fRef(id, 1)
	// A null child propagates through convert.
	if got := evaluateFormula(fConvert("second", fMetric(id, 1)), map[string]*float64{}); got != nil {
		t.Fatalf("null child must propagate, got %v", *got)
	}
	// Conversion composes with arithmetic: (1500 ms + 1500 ms) -> seconds = 3.
	expr := fConvert(
		"second",
		fBinary(FormulaAdd, fMetric(id, 1), fMetric(id, 1)),
	)
	values := map[string]*float64{formulaRefKey(ref): fFloat(1500)}
	got := evaluateFormula(expr, values)
	if got == nil || *got != 3 {
		t.Fatalf("expected nested conversion 3, got %v", got)
	}
}
