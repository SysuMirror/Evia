package reporting

import (
	"math"
	"testing"

	"github.com/howtomakeaname/Evia/backend/internal/analysis"
)

func fFloat(v float64) *float64 { return &v }

func fRef(id string, version int64) MetricReference {
	return MetricReference{MetricID: id, Version: version}
}

func fMetric(id string, version int64) *FormulaExpression {
	ref := fRef(id, version)
	return &FormulaExpression{Op: FormulaMetric, Metric: &ref}
}

func fConst(v float64) *FormulaExpression {
	return &FormulaExpression{Op: FormulaConstant, Value: fFloat(v)}
}

func fBinary(op string, left, right *FormulaExpression) *FormulaExpression {
	return &FormulaExpression{Op: op, Left: left, Right: right}
}

func fRatio(idA string, idB string) FormulaExpression {
	return *fBinary(FormulaDivide, fMetric(idA, 1), fMetric(idB, 1))
}

func TestNormalizeFormulaAcceptsTypedTree(t *testing.T) {
	def := FormulaDefinition{Expression: fRatio("11111111-1111-4111-8111-111111111111", "22222222-2222-4222-8222-222222222222"), Unit: "number"}
	normalized, err := NormalizeFormulaDefinition(def)
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	refs := FormulaReferences(normalized.Expression)
	if len(refs) != 2 || refs[0].MetricID > refs[1].MetricID {
		t.Fatalf("canonical references %+v", refs)
	}
	if normalized.Unit != "number" {
		t.Fatalf("unit %s", normalized.Unit)
	}
}

func TestNormalizeFormulaOwnsItsCopy(t *testing.T) {
	def := FormulaDefinition{Expression: fRatio("11111111-1111-4111-8111-111111111111", "22222222-2222-4222-8222-222222222222"), Unit: "number"}
	normalized, err := NormalizeFormulaDefinition(def)
	if err != nil {
		t.Fatal(err)
	}
	def.Expression.Left.Metric.Version = 99
	if normalized.Expression.Left.Metric.Version == 99 {
		t.Fatal("normalized formula shares input reference")
	}
}

func TestNormalizeFormulaRejectsBadShapes(t *testing.T) {
	goodID := "11111111-1111-4111-8111-111111111111"
	for _, tc := range []struct {
		name string
		expr *FormulaExpression
		unit string
	}{
		{"unknown op", &FormulaExpression{Op: "pow", Left: fMetric(goodID, 1), Right: fConst(2)}, "number"},
		{"constant only", fConst(3), "number"},
		{"metric carrying child", &FormulaExpression{Op: FormulaMetric, Metric: &MetricReference{MetricID: goodID, Version: 1}, Left: fConst(1)}, "number"},
		{"binary missing right", fBinary(FormulaAdd, fMetric(goodID, 1), nil), "number"},
		{"constant carrying metric", &FormulaExpression{Op: FormulaConstant, Value: fFloat(1), Metric: &MetricReference{MetricID: goodID, Version: 1}}, "number"},
		{"bad metric id", fMetric("not-a-uuid", 1), "number"},
		{"bad metric version", fMetric(goodID, 0), "number"},
		{"bad unit", fMetric(goodID, 1), "parsec"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := NormalizeFormulaDefinition(FormulaDefinition{Expression: *tc.expr, Unit: tc.unit}); err == nil {
				t.Fatal("expected rejection")
			}
		})
	}
}

func TestNormalizeFormulaRejectsUnsafeConstants(t *testing.T) {
	goodID := "11111111-1111-4111-8111-111111111111"
	for _, value := range []float64{math.NaN(), math.Inf(1), math.Inf(-1), analysis.MaxSafeInteger + 1, -(analysis.MaxSafeInteger + 1)} {
		expr := fBinary(FormulaMultiply, fMetric(goodID, 1), fConst(value))
		if _, err := NormalizeFormulaDefinition(FormulaDefinition{Expression: *expr, Unit: "event"}); err == nil {
			t.Fatalf("expected constant rejection for %v", value)
		}
	}
}

func TestNormalizeFormulaDepthBudget(t *testing.T) {
	goodID := "11111111-1111-4111-8111-111111111111"
	// Build a left-leaning chain. Depth 9 exceeds MaxFormulaDepth (root is 1).
	node := fMetric(goodID, 1)
	for i := 0; i < MaxFormulaDepth; i++ {
		node = fBinary(FormulaAdd, node, fConst(1))
	}
	if _, err := NormalizeFormulaDefinition(FormulaDefinition{Expression: *node, Unit: "event"}); err == nil {
		t.Fatal("expected depth budget rejection")
	}
}

func TestNormalizeFormulaNodeBudget(t *testing.T) {
	goodID := "11111111-1111-4111-8111-111111111111"
	root := fMetric(goodID, 1)
	// A balanced tree accumulates nodes quickly without breaching depth first.
	for i := 0; i < 6; i++ {
		root = fBinary(FormulaAdd, root, root)
	}
	if _, err := NormalizeFormulaDefinition(FormulaDefinition{Expression: *root, Unit: "event"}); err == nil {
		t.Fatal("expected node budget rejection")
	}
}

func TestNormalizeFormulaReferenceBudget(t *testing.T) {
	var left *FormulaExpression
	for i := 0; i < MaxFormulaReferences+1; i++ {
		id := fUUID(i + 1)
		node := fMetric(id, 1)
		if left == nil {
			left = node
			continue
		}
		left = fBinary(FormulaAdd, left, node)
	}
	if _, err := NormalizeFormulaDefinition(FormulaDefinition{Expression: *left, Unit: "event"}); err == nil {
		t.Fatal("expected reference budget rejection")
	}
}

func TestFormulaReferencesDeduplicatesAndSorts(t *testing.T) {
	id := "11111111-1111-4111-8111-111111111111"
	expr := fBinary(FormulaDivide, fMetric(id, 1), fBinary(FormulaMultiply, fMetric(id, 1), fConst(2)))
	refs := FormulaReferences(*expr)
	if len(refs) != 1 || refs[0].Version != 1 {
		t.Fatalf("expected single deduplicated reference, got %+v", refs)
	}
}

// fUUID returns a deterministic valid lowercase UUID for fixture metrics.
func fUUID(n int) string {
	const hex = "0123456789abcdef"
	b := make([]byte, 32)
	for i := range b {
		b[i] = hex[(n>>uint(i))&0xf]
	}
	return string(b[0:8]) + "-" + string(b[8:12]) + "-4" + string(b[13:16]) + "-8" + string(b[17:20]) + "-" + string(b[20:32])
}
