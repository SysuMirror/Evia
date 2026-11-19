package reporting

import (
	"encoding/json"
	"sort"
	"strings"
	"time"

	"github.com/howtomakeaname/Evia/backend/internal/analysis"
)

const (
	formulaValueColumn  = "metric_0"
	formulaBucketColumn = "bucket"
	// formulaKeySeparator delimits alignment components and is not a legal
	// content character of the scalar dimension values.
	formulaKeySeparator = "\x1f"
)

// EvaluateFormulaResult aligns the results of every referenced metric and
// evaluates the formula for the union of alignment keys. Leaves must share the
// same time bucket and grouping shape because each leaf request is built from
// the same runtime parameters. A leaf that has no row for a key contributes
// null, never zero, so missing data stays distinguishable from a real value.
func EvaluateFormulaResult(def FormulaDefinition, leaves map[MetricReference]analysis.Result) (analysis.Result, error) {
	normalized, e := NormalizeFormulaDefinition(def)
	if e != nil {
		return analysis.Result{}, e
	}
	refs := FormulaReferences(normalized.Expression)
	if len(refs) == 0 || len(leaves) != len(refs) {
		return analysis.Result{}, formulaInvalid("INVALID_FORMULA", "公式叶子结果与引用不匹配", "/formula/expression")
	}
	frame, e := newFormulaFrame(refs, leaves)
	if e != nil {
		return analysis.Result{}, e
	}
	// Preserve the shared column contract; only metric_0 values are recomputed.
	out := analysis.Result{Columns: leaves[refs[0]].Columns, Rows: make([]map[string]any, 0, len(frame.keys))}
	for _, key := range frame.keys {
		values := map[string]*float64{}
		for _, ref := range refs {
			values[formulaRefKey(ref)] = frame.numeric[formulaRefKey(ref)][key]
		}
		row := map[string]any{}
		// Dimension, bucket and presence columns come from one representative
		// leaf; the alignment key already proves they describe the same slice.
		for column, value := range frame.representative[key] {
			row[column] = value
		}
		row[formulaValueColumn] = dereferenceFormulaValue(evaluateFormula(&normalized.Expression, values))
		out.Rows = append(out.Rows, row)
	}
	out.Metadata = mergeFormulaMetadata(leaves, refs)
	return out, nil
}

type formulaFrame struct {
	keys           []string
	representative map[string]map[string]any
	// numeric is keyed first by reference key, then by alignment key.
	numeric map[string]map[string]*float64
}

func newFormulaFrame(refs []MetricReference, leaves map[MetricReference]analysis.Result) (formulaFrame, error) {
	frame := formulaFrame{representative: map[string]map[string]any{}, numeric: map[string]map[string]*float64{}}
	var identityColumns []string
	var frameColumns []string
	for index, ref := range refs {
		result := leaves[ref]
		idColumns, allColumns, e := leafShape(result)
		if e != nil {
			return frame, e
		}
		if index == 0 {
			identityColumns, frameColumns = idColumns, allColumns
		} else if !sameStrings(frameColumns, allColumns) {
			return frame, formulaInvalid("FORMULA_RESULT_SHAPE", "公式各指标的分组或时间粒度不一致", "/formula/expression")
		}
		refKey := formulaRefKey(ref)
		frame.numeric[refKey] = map[string]*float64{}
		for _, row := range result.Rows {
			key, e := alignmentKey(row, identityColumns, hasBucket(result.Columns))
			if e != nil {
				return frame, e
			}
			value, e := formulaRowNumber(row[formulaValueColumn])
			if e != nil {
				return frame, e
			}
			frame.numeric[refKey][key] = value
			if _, seen := frame.representative[key]; !seen {
				frame.representative[key] = row
				frame.keys = append(frame.keys, key)
			}
		}
	}
	sort.Strings(frame.keys)
	return frame, nil
}

// leafShape returns the identity columns used for alignment and the full
// ordered non-value column keys that every leaf must share.
func leafShape(result analysis.Result) ([]string, []string, error) {
	identity := []string{}
	columns := []string{}
	hasValue := false
	for _, column := range result.Columns {
		if column.Key == formulaValueColumn {
			hasValue = true
			continue
		}
		columns = append(columns, column.Key)
		if strings.HasPrefix(column.Key, "group_") && !strings.HasSuffix(column.Key, "_present") {
			identity = append(identity, column.Key)
		}
	}
	if !hasValue {
		return nil, nil, formulaInvalid("FORMULA_RESULT_SHAPE", "指标结果缺少数值列", "/formula/expression")
	}
	return identity, columns, nil
}

func hasBucket(columns []analysis.Column) bool {
	for _, column := range columns {
		if column.Key == formulaBucketColumn {
			return true
		}
	}
	return false
}

func alignmentKey(row map[string]any, identityColumns []string, bucket bool) (string, error) {
	parts := make([]string, 0, len(identityColumns)+1)
	if bucket {
		encoded, e := encodeKeyComponent(row[formulaBucketColumn])
		if e != nil {
			return "", e
		}
		parts = append(parts, encoded)
	}
	for _, column := range identityColumns {
		encoded, e := encodeKeyComponent(row[column])
		if e != nil {
			return "", e
		}
		parts = append(parts, encoded)
	}
	return strings.Join(parts, formulaKeySeparator), nil
}

func encodeKeyComponent(value any) (string, error) {
	if value == nil {
		return "null", nil
	}
	data, e := json.Marshal(value)
	if e != nil {
		return "", formulaInvalid("FORMULA_RESULT_SHAPE", "分组值无法用于对齐", "/formula/expression")
	}
	return string(data), nil
}

// dereferenceFormulaValue stores a computed scalar as float64 and keeps an
// explicit null instead of a pointer, matching the engine's nullable number
// columns so JSON emits a number or null rather than an opaque pointer.
func dereferenceFormulaValue(value *float64) any {
	if value == nil {
		return nil
	}
	return *value
}

// formulaRowNumber converts a trend metric scalar to a nullable float64.
// json.Number is the wire representation, while tests and in-memory engines may
// supply native numeric types. Booleans and strings are never silently coerced.
func formulaRowNumber(value any) (*float64, error) {
	switch n := value.(type) {
	case nil:
		return nil, nil
	case float64:
		return &n, nil
	case float32:
		v := float64(n)
		return &v, nil
	case int:
		v := float64(n)
		return &v, nil
	case int64:
		v := float64(n)
		return &v, nil
	case json.Number:
		v, e := n.Float64()
		if e != nil {
			return nil, formulaInvalid("FORMULA_RESULT_SHAPE", "指标数值无法解析", "/formula/expression")
		}
		return &v, nil
	default:
		return nil, formulaInvalid("FORMULA_RESULT_SHAPE", "指标数值列类型无效", "/formula/expression")
	}
}

func sameStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// mergeFormulaMetadata keeps the most conservative accuracy and coverage. A
// composite is approximate as soon as one operand is approximate, and the
// reported watermark never exceeds the latest observed across leaves.
func mergeFormulaMetadata(leaves map[MetricReference]analysis.Result, refs []MetricReference) analysis.Metadata {
	metadata := analysis.Metadata{Accuracy: analysis.AccuracyExact, SampleRate: 1, CacheHit: false, GeneratedAt: time.Now().UTC()}
	var coverage analysis.Coverage
	for _, ref := range refs {
		leaf := leaves[ref].Metadata
		if leaf.Accuracy == analysis.AccuracyApproximate {
			metadata.Accuracy = analysis.AccuracyApproximate
		}
		if leaf.DataWatermark != nil && (metadata.DataWatermark == nil || leaf.DataWatermark.After(*metadata.DataWatermark)) {
			metadata.DataWatermark = leaf.DataWatermark
		}
		coverage = leaf.Coverage
	}
	metadata.Coverage = coverage
	if metadata.Coverage.State == "" {
		metadata.Coverage.State = analysis.CoverageStateProvisional
	}
	return metadata
}
