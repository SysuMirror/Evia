package analysis

import (
	"strings"
	"time"
)

// WalkFields visits every field reference, including nested step predicates.
func WalkFields(r *Request, visit func(*Field) error) error {
	var filter func(*Filter) error
	filter = func(f *Filter) error {
		if f == nil {
			return nil
		}
		if f.Field != nil {
			if e := visit(f.Field); e != nil {
				return e
			}
		}
		for _, a := range f.Args {
			if e := filter(a); e != nil {
				return e
			}
		}
		return nil
	}
	if e := filter(r.Filter); e != nil {
		return e
	}
	for i := range r.GroupBy {
		if e := visit(&r.GroupBy[i]); e != nil {
			return e
		}
	}
	for i := range r.Metrics {
		if r.Metrics[i].Field != nil {
			if e := visit(r.Metrics[i].Field); e != nil {
				return e
			}
		}
	}
	if r.Funnel != nil {
		for _, s := range r.Funnel.Steps {
			if e := filter(s.Filter); e != nil {
				return e
			}
		}
		for i := range r.Funnel.SameEntity {
			if e := visit(&r.Funnel.SameEntity[i].Field); e != nil {
				return e
			}
		}
	}
	if r.Retention != nil {
		for _, s := range []Step{r.Retention.StartEvent, r.Retention.ReturnEvent} {
			if e := filter(s.Filter); e != nil {
				return e
			}
		}
	}
	return nil
}
func CheckResolved(r Request) error {
	if e := WalkFields(&r, func(f *Field) error {
		switch f.ResolvedType {
		case "string", "boolean", "number", "integer", "datetime", "array:string", "array:boolean", "array:number", "array:integer":
			return nil
		default:
			return Invalid("UNSUPPORTED_FIELD_TYPE", "字段类型不支持查询")
		}
	}); e != nil {
		return e
	}
	var filter func(*Filter) error
	filter = func(f *Filter) error {
		if f == nil {
			return nil
		}
		for _, a := range f.Args {
			if e := filter(a); e != nil {
				return e
			}
		}
		if f.Field == nil || f.Op == "is_missing" || f.Op == "is_present" {
			return nil
		}
		kind := f.Field.ResolvedType
		values, isArray := f.Value.([]any)
		if strings.HasPrefix(kind, "array:") {
			if f.Op == "contains" {
				if isArray {
					return Invalid("INVALID_FILTER_TYPE", "数组 contains 需要标量值")
				}
				return compatibleScalar(strings.TrimPrefix(kind, "array:"), f.Value)
			}
			if (f.Op != "eq" && f.Op != "neq") || !isArray {
				return Invalid("INVALID_FILTER_TYPE", "数组字段仅支持相等、包含和存在性过滤")
			}
			for _, v := range values {
				if e := compatibleScalar(strings.TrimPrefix(kind, "array:"), v); e != nil {
					return e
				}
			}
			return nil
		}
		if f.Op == "prefix" || f.Op == "contains" {
			if kind != "string" {
				return Invalid("INVALID_FILTER_TYPE", "文本运算需要字符串字段")
			}
		}
		if f.Op == "gt" || f.Op == "gte" || f.Op == "lt" || f.Op == "lte" {
			if kind != "number" && kind != "integer" && kind != "datetime" {
				return Invalid("INVALID_FILTER_TYPE", "范围运算需要数值或时间字段")
			}
		}
		if isArray {
			if f.Op != "in" {
				return Invalid("INVALID_FILTER_TYPE", "标量字段仅支持 in 值数组")
			}
			for _, v := range values {
				if e := compatibleScalar(kind, v); e != nil {
					return e
				}
			}
			return nil
		}
		return compatibleScalar(kind, f.Value)
	}
	if e := filter(r.Filter); e != nil {
		return e
	}
	for _, g := range r.GroupBy {
		if strings.HasPrefix(g.ResolvedType, "array:") {
			return Invalid("UNSUPPORTED_GROUP_TYPE", "数组不能用作分组")
		}
	}
	for _, m := range r.Metrics {
		if m.Field != nil {
			if strings.HasPrefix(m.Field.ResolvedType, "array:") {
				return Invalid("UNSUPPORTED_METRIC_TYPE", "数组不能用作指标")
			}
			if m.Op != "unique" && m.Field.ResolvedType != "number" && m.Field.ResolvedType != "integer" {
				return Invalid("INVALID_METRIC_TYPE", "数值指标需要数值字段")
			}
		}
	}
	if r.Funnel != nil {
		kind := ""
		for _, s := range r.Funnel.Steps {
			if e := filter(s.Filter); e != nil {
				return e
			}
		}
		for _, constraint := range r.Funnel.SameEntity {
			f := constraint.Field
			step := r.Funnel.Steps[constraint.StepIndex]
			if strings.HasPrefix(f.ResolvedType, "array:") || f.ResolvedType == "datetime" {
				return Invalid("INVALID_SAME_ENTITY", "实体字段必须为标量标识")
			}
			if f.Kind == "property" && (f.EventKey != step.EventKey || f.SchemaVersion != step.SchemaVersion) {
				return Invalid("INVALID_SAME_ENTITY", "实体字段必须属于对应步骤事件及版本")
			}
			normalized := f.ResolvedType
			if normalized == "integer" {
				normalized = "number"
			}
			if kind != "" && kind != normalized {
				return Invalid("INVALID_SAME_ENTITY", "实体字段必须同类型")
			}
			kind = normalized
		}
	}
	if r.Retention != nil {
		for _, s := range []Step{r.Retention.StartEvent, r.Retention.ReturnEvent} {
			if e := filter(s.Filter); e != nil {
				return e
			}
		}
	}
	return nil
}
func compatibleScalar(kind string, value any) error {
	actual, e := scalarType(value)
	if e != nil {
		return e
	}
	if kind == "integer" {
		kind = "number"
	}
	if kind == "datetime" {
		_, e := parseDatetime(value)
		return e
	}
	if kind != actual {
		return Invalid("INVALID_FILTER_TYPE", "过滤值与字段类型不匹配")
	}
	return nil
}

// parseDatetime keeps resolved-field validation and parameter binding identical.
func parseDatetime(value any) (time.Time, error) {
	text, ok := value.(string)
	if !ok {
		return time.Time{}, Invalid("INVALID_FILTER_TYPE", "时间过滤值必须为 RFC3339 字符串")
	}
	parsed, e := time.Parse(time.RFC3339Nano, text)
	if e != nil {
		return time.Time{}, Invalid("INVALID_FILTER_TYPE", "时间过滤值必须为 RFC3339 字符串")
	}
	return parsed, nil
}
