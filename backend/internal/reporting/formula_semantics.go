package reporting

import (
	"math"

	"github.com/howtomakeaname/Evia/backend/internal/analysis"
)

// Formula semantics are deliberately conservative. Units form a small explicit
// algebra instead of an open-ended dimension system, and every arithmetic rule
// is named so an incompatible combination is rejected rather than guessed.
const formulaDimensionless = "number"

// InferFormulaUnit computes the result unit from the operand units. The units
// map is keyed by formulaRefKey and is produced by ValidateFormulaDAG.
func InferFormulaUnit(expr FormulaExpression, units map[string]string) (string, error) {
	return inferFormulaUnitNode(&expr, units)
}

func inferFormulaUnitNode(node *FormulaExpression, units map[string]string) (string, error) {
	switch node.Op {
	case FormulaConstant:
		return formulaDimensionless, nil
	case FormulaMetric:
		unit, ok := units[formulaRefKey(*node.Metric)]
		if !ok {
			return "", formulaInvalid("INVALID_FORMULA_UNIT", "被引用指标缺少单位", "/formula/expression")
		}
		return unit, nil
	case FormulaAdd, FormulaSubtract:
		return inferSameUnit(node, units)
	case FormulaMultiply:
		left, e := inferFormulaUnitNode(node.Left, units)
		if e != nil {
			return "", e
		}
		right, e := inferFormulaUnitNode(node.Right, units)
		if e != nil {
			return "", e
		}
		// Scaling preserves the dimensioned operand; multiplying two
		// dimensioned quantities needs an explicit dimension model and is not
		// part of the restricted v1 algebra.
		switch {
		case left == formulaDimensionless:
			return right, nil
		case right == formulaDimensionless:
			return left, nil
		default:
			return "", formulaInvalid("FORMULA_UNIT_INCOMPATIBLE", "两个带单位指标相乘不受支持", "/formula/expression")
		}
	case FormulaDivide:
		left, e := inferFormulaUnitNode(node.Left, units)
		if e != nil {
			return "", e
		}
		right, e := inferFormulaUnitNode(node.Right, units)
		if e != nil {
			return "", e
		}
		switch {
		case left == right:
			// A ratio of identical units is dimensionless, e.g. a conversion rate.
			return formulaDimensionless, nil
		case right == formulaDimensionless:
			return left, nil
		default:
			return "", formulaInvalid("FORMULA_UNIT_INCOMPATIBLE", "当前单位组合的除法不受支持", "/formula/expression")
		}
	case FormulaConvert:
		child, e := inferFormulaUnitNode(node.Left, units)
		if e != nil {
			return "", e
		}
		if _, ok := formulaConversionFactor(child, node.TargetUnit); !ok {
			return "", formulaInvalid("FORMULA_UNIT_INCOMPATIBLE", "单位换算仅支持封闭表内的同量纲换算", "/formula/expression/target_unit")
		}
		return node.TargetUnit, nil
	default:
		return "", formulaInvalid("INVALID_FORMULA_OP", "公式运算不受支持", "/formula/expression/op")
	}
}

func inferSameUnit(node *FormulaExpression, units map[string]string) (string, error) {
	left, e := inferFormulaUnitNode(node.Left, units)
	if e != nil {
		return "", e
	}
	right, e := inferFormulaUnitNode(node.Right, units)
	if e != nil {
		return "", e
	}
	if left != right {
		return "", formulaInvalid("FORMULA_UNIT_INCOMPATIBLE", "加减运算要求两侧单位一致", "/formula/expression")
	}
	return left, nil
}

// VerifyFormulaUnit confirms the declared result unit matches the unit derived
// from the validated dependency graph. Unit labels never convert values.
func VerifyFormulaUnit(def FormulaDefinition, graph FormulaGraph) error {
	derived, e := InferFormulaUnit(def.Expression, graph.Units)
	if e != nil {
		return e
	}
	if derived != def.Unit {
		return formulaInvalid("INVALID_FORMULA_UNIT", "声明单位与公式推导单位不一致", "/formula/unit")
	}
	return nil
}

// evaluateFormula computes one aligned row. A nil result means null: a missing
// operand propagates null and division by zero yields null instead of zero or
// an error. Non-finite and out-of-range arithmetic is likewise null so the
// response never carries NaN, Infinity or an unsafe integer.
func evaluateFormula(node *FormulaExpression, values map[string]*float64) *float64 {
	switch node.Op {
	case FormulaConstant:
		if node.Value == nil {
			return nil
		}
		value := *node.Value
		return &value
	case FormulaMetric:
		return values[formulaRefKey(*node.Metric)]
	case FormulaAdd, FormulaSubtract, FormulaMultiply, FormulaDivide:
		left := evaluateFormula(node.Left, values)
		right := evaluateFormula(node.Right, values)
		// Null propagates through every binary operation.
		if left == nil || right == nil {
			return nil
		}
		var result float64
		switch node.Op {
		case FormulaAdd:
			result = *left + *right
		case FormulaSubtract:
			result = *left - *right
		case FormulaMultiply:
			result = *left * *right
		case FormulaDivide:
			// Zero and negative zero are explicit nulls, never a fake zero.
			if *right == 0 {
				return nil
			}
			result = *left / *right
		}
		if math.IsNaN(result) || math.IsInf(result, 0) || math.Abs(result) > analysis.MaxSafeInteger {
			return nil
		}
		return &result
	case FormulaConvert:
		child := evaluateFormula(node.Left, values)
		if child == nil {
			return nil
		}
		// Unit inference already proved the child unit converts to TargetUnit,
		// so the factor is determined by the target alone. Fail closed if the
		// factor is somehow absent rather than returning an unconverted value.
		factor, ok := formulaConvertEvalFactor(node.TargetUnit)
		if !ok {
			return nil
		}
		result := *child * factor
		if math.IsNaN(result) || math.IsInf(result, 0) || math.Abs(result) > analysis.MaxSafeInteger {
			return nil
		}
		return &result
	default:
		return nil
	}
}
