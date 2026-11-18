package reporting

import (
	"bytes"
	"encoding/json"
	"math"
	"strconv"

	"github.com/howtomakeaname/Evia/backend/internal/analysis"
)

// Formula (composite) metrics combine pinned immutable metric versions with a
// restricted typed expression. There is no string grammar, SQL or JavaScript:
// every operation is an explicit tree node, so the platform can validate the
// dependency graph, units and expansion budget before anything executes.
const (
	FormulaConstant = "constant"
	FormulaMetric   = "metric"
	FormulaAdd      = "add"
	FormulaSubtract = "subtract"
	FormulaMultiply = "multiply"
	FormulaDivide   = "divide"
	// FormulaConvert is a unary node that re-expresses one physical quantity in
	// another unit of the same dimension. It carries one child (Left) and a
	// target unit; the closed conversion table rejects every other pair.
	FormulaConvert = "convert"
)

// Structural budgets keep a single formula bounded regardless of how deeply
// metrics nest. They are independent of the cross-metric dependency limits in
// formula_dag.go.
const (
	// MaxFormulaDepth is the syntactic tree depth, with the root at depth 1.
	MaxFormulaDepth = 8
	// MaxFormulaNodes bounds every node in one expression tree.
	MaxFormulaNodes = 64
	// MaxFormulaReferences bounds the distinct pinned metric versions a single
	// expression may fan out to. Each reference is one engine execution.
	MaxFormulaReferences = 8
)

// FormulaExpression is one node of the restricted formula tree.
//
//	constant: only Value is set
//	metric:   only Metric is set and pins an immutable metric version
//	add/subtract/multiply/divide: only Left and Right are set
//	convert:  only Left and TargetUnit are set; it re-expresses one physical
//	          quantity in another unit of the same dimension
type FormulaExpression struct {
	Op         string             `json:"op"`
	Value      *float64           `json:"value,omitempty"`
	Metric     *MetricReference   `json:"metric,omitempty"`
	Left       *FormulaExpression `json:"left,omitempty"`
	Right      *FormulaExpression `json:"right,omitempty"`
	TargetUnit string             `json:"target_unit,omitempty"`
}

// FormulaDefinition is the normalized composite metric configuration. Unit is
// the declared result unit; DAG validation recomputes it from the operands and
// rejects any mismatch, so a unit label can never silently change values.
type FormulaDefinition struct {
	Expression FormulaExpression `json:"expression"`
	Unit       string            `json:"unit"`
}

// formulaDecode retains numeric identity long enough to reject values that do
// not fit the safe number range before they become float64.
type formulaDecode struct {
	Op         string           `json:"op"`
	Value      json.Number      `json:"value,omitempty"`
	Metric     *MetricReference `json:"metric,omitempty"`
	Left       *formulaDecode   `json:"left,omitempty"`
	Right      *formulaDecode   `json:"right,omitempty"`
	TargetUnit string           `json:"target_unit,omitempty"`
}

// formulaDefinitionDecode mirrors the public FormulaDefinition so the strict
// decoder rejects unknown fields at both the definition and node levels.
type formulaDefinitionDecode struct {
	Expression formulaDecode `json:"expression"`
	Unit       string        `json:"unit"`
}

type formulaStats struct {
	nodes      int
	references map[string]MetricReference
}

func formulaInvalid(code, message, path string) error {
	return analysis.Invalid(code, message, path)
}

// NormalizeFormulaDefinition returns an independent canonical formula and
// enforces every structural budget. Publication and metric resolution are
// separate transactional checks; this function never fabricates runtime data.
func NormalizeFormulaDefinition(in FormulaDefinition) (FormulaDefinition, error) {
	data, e := json.Marshal(in)
	if e != nil {
		return FormulaDefinition{}, formulaInvalid("INVALID_FORMULA", "公式必须为有效 JSON", "/formula")
	}
	if len(data) > MaxMetricDefinitionBytes {
		return FormulaDefinition{}, analysis.Invalid("BODY_TOO_LARGE", "公式定义超过大小限制")
	}
	var decoded formulaDefinitionDecode
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	decoder.DisallowUnknownFields()
	if e = decoder.Decode(&decoded); e != nil {
		return FormulaDefinition{}, formulaInvalid("INVALID_FORMULA", "公式字段无效", "/formula/expression")
	}
	if !formulaKnownUnit(in.Unit) {
		return FormulaDefinition{}, formulaInvalid("INVALID_FORMULA_UNIT", "公式结果单位无效", "/formula/unit")
	}
	stats := formulaStats{references: map[string]MetricReference{}}
	root, e := normalizeFormulaNode(&decoded.Expression, 1, &stats)
	if e != nil {
		return FormulaDefinition{}, e
	}
	if len(stats.references) == 0 {
		return FormulaDefinition{}, formulaInvalid("INVALID_FORMULA_REFERENCE", "公式至少需要引用一个指标", "/formula/expression")
	}
	return FormulaDefinition{Expression: *root, Unit: in.Unit}, nil
}

func normalizeFormulaNode(in *formulaDecode, depth int, stats *formulaStats) (*FormulaExpression, error) {
	if in == nil {
		return nil, formulaInvalid("INVALID_FORMULA", "公式节点不能为空", "/formula/expression")
	}
	if depth > MaxFormulaDepth {
		return nil, formulaInvalid("FORMULA_TOO_DEEP", "公式嵌套深度超过预算", "/formula/expression")
	}
	stats.nodes++
	if stats.nodes > MaxFormulaNodes {
		return nil, formulaInvalid("FORMULA_TOO_LARGE", "公式节点数量超过预算", "/formula/expression")
	}
	out := &FormulaExpression{Op: in.Op}
	switch in.Op {
	case FormulaConstant:
		if in.Left != nil || in.Right != nil || in.Metric != nil || in.Value == "" {
			return nil, formulaInvalid("INVALID_FORMULA", "常量节点只能包含数值", "/formula/expression")
		}
		value, e := in.Value.Float64()
		if e != nil || math.IsNaN(value) || math.IsInf(value, 0) || math.Abs(value) > analysis.MaxSafeInteger {
			return nil, formulaInvalid("INVALID_FORMULA_CONSTANT", "常量必须是安全范围内的有限数值", "/formula/expression/value")
		}
		out.Value = &value
	case FormulaMetric:
		if in.Left != nil || in.Right != nil || in.Value != "" || in.Metric == nil {
			return nil, formulaInvalid("INVALID_FORMULA", "指标节点只能包含指标引用", "/formula/expression")
		}
		if e := validateMetricReference(in.Metric); e != nil {
			return nil, e
		}
		ref := *in.Metric
		key := formulaRefKey(ref)
		if _, exists := stats.references[key]; !exists {
			if len(stats.references) >= MaxFormulaReferences {
				return nil, formulaInvalid("FORMULA_REFERENCE_LIMIT", "公式引用的不同指标超过预算", "/formula/expression")
			}
			stats.references[key] = ref
		}
		out.Metric = &ref
	case FormulaAdd, FormulaSubtract, FormulaMultiply, FormulaDivide:
		if in.Value != "" || in.Metric != nil || in.Left == nil || in.Right == nil {
			return nil, formulaInvalid("INVALID_FORMULA", "运算节点必须包含左右两个子表达式", "/formula/expression")
		}
		left, e := normalizeFormulaNode(in.Left, depth+1, stats)
		if e != nil {
			return nil, e
		}
		right, e := normalizeFormulaNode(in.Right, depth+1, stats)
		if e != nil {
			return nil, e
		}
		out.Left, out.Right = left, right
	case FormulaConvert:
		if in.Value != "" || in.Metric != nil || in.Right != nil || in.Left == nil || in.TargetUnit == "" {
			return nil, formulaInvalid("INVALID_FORMULA", "单位换算节点必须包含一个子表达式和目标单位", "/formula/expression")
		}
		if !formulaConvertibleTarget(in.TargetUnit) {
			return nil, formulaInvalid("FORMULA_UNIT_INCOMPATIBLE", "目标单位不支持显式换算", "/formula/expression/target_unit")
		}
		left, e := normalizeFormulaNode(in.Left, depth+1, stats)
		if e != nil {
			return nil, e
		}
		out.Left = left
		out.TargetUnit = in.TargetUnit
	default:
		return nil, formulaInvalid("INVALID_FORMULA_OP", "公式运算不受支持", "/formula/expression/op")
	}
	return out, nil
}

// FormulaReferences returns the distinct pinned metric references in canonical
// (metric id, then version) order. The normalized expression is walked without
// recursion limits beyond the budgets already enforced at normalization.
func FormulaReferences(expr FormulaExpression) []MetricReference {
	collect := map[string]MetricReference{}
	var walk func(*FormulaExpression)
	walk = func(node *FormulaExpression) {
		if node == nil {
			return
		}
		if node.Op == FormulaMetric && node.Metric != nil {
			collect[formulaRefKey(*node.Metric)] = *node.Metric
		}
		walk(node.Left)
		walk(node.Right)
	}
	walk(&expr)
	out := make([]MetricReference, 0, len(collect))
	for _, ref := range collect {
		out = append(out, ref)
	}
	sortReferences(out)
	return out
}

func formulaRefKey(ref MetricReference) string {
	return ref.MetricID + "#" + strconv.FormatInt(ref.Version, 10)
}

func sortReferences(refs []MetricReference) {
	for i := 1; i < len(refs); i++ {
		for j := i; j > 0; j-- {
			if refs[j].MetricID < refs[j-1].MetricID || refs[j].MetricID == refs[j-1].MetricID && refs[j].Version < refs[j-1].Version {
				refs[j], refs[j-1] = refs[j-1], refs[j]
			} else {
				break
			}
		}
	}
}

// Measurement unit closed set, mirrored by the MetricDefinition.unit and
// FormulaDefinition.unit OpenAPI enums and guarded by
// TestFormulaUnitVocabularyMatchesContract.
const (
	UnitEvent         = "event"
	UnitRawUID        = "raw_uid"
	UnitAnonymousID   = "anonymous_id"
	UnitDistinctValue = "distinct_value"
	UnitNumber        = "number"
	UnitMillisecond   = "millisecond"
	UnitSecond        = "second"
	UnitByte          = "byte"
)

// KnownUnits is the single Go-side source of truth for the eight measurement
// units accepted by metrics and formula expressions.
var KnownUnits = []string{
	UnitEvent, UnitRawUID, UnitAnonymousID, UnitDistinctValue,
	UnitNumber, UnitMillisecond, UnitSecond, UnitByte,
}

func formulaKnownUnit(unit string) bool {
	for _, known := range KnownUnits {
		if unit == known {
			return true
		}
	}
	return false
}

// formulaConversions is the single closed table of explicit same-dimension
// unit conversions: source unit -> target unit -> multiplier applied to the
// source value. Only quantities with a defined linear factor are reachable, so
// a convert node can never invent a relationship between unrelated dimensions
// (no byte↔millisecond, no currency because no currency unit exists yet).
var formulaConversions = map[string]map[string]float64{
	UnitMillisecond: {UnitSecond: 0.001},
	UnitSecond:      {UnitMillisecond: 1000},
}

// formulaConvertibleTarget reports whether a unit may appear as the target of
// an explicit convert node at normalization time, before source units are
// resolved.
func formulaConvertibleTarget(unit string) bool {
	for _, targets := range formulaConversions {
		if _, ok := targets[unit]; ok {
			return true
		}
	}
	return false
}

// formulaConversionFactor returns the multiplier for a validated source→target
// pair. Unit inference calls it and fails closed when the pair is not in the
// table.
func formulaConversionFactor(source, target string) (float64, bool) {
	if targets, ok := formulaConversions[source]; ok {
		factor, ok := targets[target]
		return factor, ok
	}
	return 0, false
}

// formulaConvertEvalFactor returns the numeric multiplier for a target unit.
// Each convertible target has exactly one source in the closed table, and unit
// inference guarantees the child unit is that source, so the evaluator does not
// need to thread static units through numeric evaluation.
func formulaConvertEvalFactor(target string) (float64, bool) {
	for _, targets := range formulaConversions {
		if factor, ok := targets[target]; ok {
			return factor, true
		}
	}
	return 0, false
}
