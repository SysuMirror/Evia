package reporting

import (
	"fmt"
	"strings"

	"github.com/howtomakeaname/Evia/backend/internal/analysis"
)

// Closed-set scalar types a dashboard parameter may accept.
const (
	ParameterString  = "string"
	ParameterNumber  = "number"
	ParameterBoolean = "boolean"
)

// MaxParameterValues bounds the number of declared parameters and the size of
// each in-list, mirroring the query filter budgets.
const (
	MaxParameterDeclarations = 12
	MaxParameterListValues   = 20
	MaxParameterStringLength = 1024
)

// ParameterDeclaration is the dashboard-level, versioned contract for one
// reusable runtime value. It never stores a field name: widgets bind the
// parameter to their own pinned event fields through ParameterBinding.
type ParameterDeclaration struct {
	ParameterID string `json:"parameter_id"`
	Label       string `json:"label"`
	// Type is one of the Parameter* constants.
	Type string `json:"type"`
	// Operator is the filter operator applied when the parameter is bound.
	Operator string `json:"operator"`
	Required bool   `json:"required"`
	// Default is used when the run omits the parameter; nil means no default.
	Default any `json:"default,omitempty"`
}

// ParameterBinding maps one dashboard parameter to a concrete pinned field of
// a widget's source event. The same parameter can bind to different fields on
// different widgets, but each widget binds a parameter at most once.
type ParameterBinding struct {
	ParameterID string         `json:"parameter_id"`
	Field       analysis.Field `json:"field"`
}

// ParameterValues carries the runtime values keyed by parameter_id.
type ParameterValues map[string]any

var stringOperators = map[string]bool{
	"eq": true, "neq": true, "in": true, "prefix": true, "contains": true,
}
var numberOperators = map[string]bool{
	"eq": true, "neq": true, "in": true, "gt": true, "gte": true, "lt": true, "lte": true,
}
var booleanOperators = map[string]bool{"eq": true, "neq": true}

// ValidateDeclaration checks the closed-set shape independently of any run so
// malformed parameter contracts are rejected at dashboard validation time.
func ValidateDeclaration(decl ParameterDeclaration) error {
	if !dashboardUUID.MatchString(decl.ParameterID) && !dashboardText(decl.ParameterID, 64, false) {
		return analysis.Invalid("INVALID_PARAMETER", "参数标识无效", "/parameter_id")
	}
	if !dashboardText(decl.Label, 128, false) {
		return analysis.Invalid("INVALID_PARAMETER", "参数标签长度需为 1 至 128 个字符", "/label")
	}
	var allowed map[string]bool
	switch decl.Type {
	case ParameterString:
		allowed = stringOperators
	case ParameterNumber:
		allowed = numberOperators
	case ParameterBoolean:
		allowed = booleanOperators
	default:
		return analysis.Invalid("INVALID_PARAMETER", "参数类型不受支持", "/type")
	}
	if !allowed[decl.Operator] {
		return analysis.Invalid("INVALID_PARAMETER", "参数运算与类型不匹配", "/operator")
	}
	if decl.Default != nil {
		if e := validateParameterValue(decl, decl.Default); e != nil {
			return e
		}
	}
	return nil
}

// validateParameterValue enforces the scalar/array type and engine bounds.
func validateParameterValue(decl ParameterDeclaration, value any) error {
	switch decl.Type {
	case ParameterString:
		switch v := value.(type) {
		case string:
			if len([]rune(v)) > MaxParameterStringLength {
				return analysis.Invalid("INVALID_PARAMETER_VALUE", "参数字符串超出长度限制", "/parameters")
			}
		case []any:
			if len(v) == 0 || len(v) > MaxParameterListValues {
				return analysis.Invalid("INVALID_PARAMETER_VALUE", "参数数组长度无效", "/parameters")
			}
			if decl.Operator != "in" && decl.Operator != "eq" && decl.Operator != "neq" {
				return analysis.Invalid("INVALID_PARAMETER_VALUE", "该运算不接受数组参数", "/parameters")
			}
			for _, item := range v {
				text, ok := item.(string)
				if !ok || len([]rune(text)) > MaxParameterStringLength {
					return analysis.Invalid("INVALID_PARAMETER_VALUE", "参数数组必须为同类型字符串", "/parameters")
				}
			}
		default:
			return analysis.Invalid("INVALID_PARAMETER_VALUE", "参数值必须为字符串或字符串数组", "/parameters")
		}
	case ParameterNumber:
		switch v := value.(type) {
		case float64:
			// JSON numbers decode to float64; reject non-finite values.
			if v != v {
				return analysis.Invalid("INVALID_PARAMETER_VALUE", "参数数值无效", "/parameters")
			}
		case []any:
			if len(v) == 0 || len(v) > MaxParameterListValues || decl.Operator != "in" {
				return analysis.Invalid("INVALID_PARAMETER_VALUE", "数值数组参数无效", "/parameters")
			}
			for _, item := range v {
				if _, ok := item.(float64); !ok {
					return analysis.Invalid("INVALID_PARAMETER_VALUE", "参数数组必须为同类型数值", "/parameters")
				}
			}
		default:
			return analysis.Invalid("INVALID_PARAMETER_VALUE", "参数值必须为数值", "/parameters")
		}
	case ParameterBoolean:
		if _, ok := value.(bool); !ok {
			return analysis.Invalid("INVALID_PARAMETER_VALUE", "参数值必须为布尔值", "/parameters")
		}
	}
	return nil
}

// CompileWidgetParameterFilters turns dashboard parameter values into concrete
// analysis.Filter predicates for one widget. It fails closed:
//   - unknown parameter values with no matching declaration are rejected;
//   - a required parameter without value/default is rejected;
//   - a binding to an undeclared parameter or a duplicated field is rejected;
//   - type/operator/bounds mismatches are rejected.
//
// The output is an ordinary filter AST the existing query engine already
// executes, so parameters never introduce a new engine execution path.
func CompileWidgetParameterFilters(
	declarations []ParameterDeclaration,
	bindings []ParameterBinding,
	values ParameterValues,
) ([]*analysis.Filter, error) {
	if len(declarations) > MaxParameterDeclarations {
		return nil, analysis.Invalid("INVALID_PARAMETER", "参数数量超过上限", "/parameters")
	}
	declared := make(map[string]ParameterDeclaration, len(declarations))
	for _, decl := range declarations {
		if e := ValidateDeclaration(decl); e != nil {
			return nil, e
		}
		if _, exists := declared[decl.ParameterID]; exists {
			return nil, analysis.Invalid("INVALID_PARAMETER", "参数标识必须唯一", "/parameters")
		}
		declared[decl.ParameterID] = decl
	}
	for key := range values {
		if _, exists := declared[key]; !exists {
			return nil, analysis.Invalid("INVALID_PARAMETER", fmt.Sprintf("未知参数 %q", key), "/parameters")
		}
	}
	seenFields := map[string]bool{}
	filters := make([]*analysis.Filter, 0, len(bindings))
	for _, binding := range bindings {
		decl, exists := declared[binding.ParameterID]
		if !exists {
			return nil, analysis.Invalid("INVALID_PARAMETER_BINDING", "组件绑定了未声明的参数", "/parameter_bindings")
		}
		fieldKey := strings.Join([]string{
			binding.Field.Kind,
			binding.Field.Key,
			binding.Field.EventKey,
			fmt.Sprintf("%d", binding.Field.SchemaVersion),
		}, ":")
		if seenFields[fieldKey] {
			return nil, analysis.Invalid("INVALID_PARAMETER_BINDING", "同一字段不能绑定多个参数", "/parameter_bindings")
		}
		seenFields[fieldKey] = true

		value, provided := values[decl.ParameterID]
		if !provided {
			if decl.Default != nil {
				value = decl.Default
			} else if decl.Required {
				return nil, analysis.Invalid("MISSING_PARAMETER_VALUE", "缺少必填参数值", "/parameters")
			} else {
				// Optional parameter with neither value nor default: skip it.
				continue
			}
		}
		if e := validateParameterValue(decl, value); e != nil {
			return nil, e
		}
		filters = append(filters, &analysis.Filter{
			Op:    decl.Operator,
			Field: &binding.Field,
			Value: value,
		})
	}
	return filters, nil
}
