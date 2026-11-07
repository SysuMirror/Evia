package analysis

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/howtomakeaname/Evia/backend/internal/platform/contract"
	"github.com/howtomakeaname/Evia/backend/internal/platform/fault"
)

const MaxSafeInteger = 9007199254740991

var eventKey = regexp.MustCompile(`^(sys\.)?[a-z][a-z0-9_]{0,63}$`)
var propertyKey = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)
var systemTypes = map[string]string{"event_key": "string", "uid": "string", "anonymous_id": "string", "session_id": "string", "page_view_id": "string", "event_time": "datetime", "operation_id": "string"}

func Invalid(code, msg string, paths ...string) error {
	return &fault.Error{Kind: fault.Invalid, Code: code, Message: msg, Fields: paths}
}
func Parse(raw []byte, validator Validator) (Request, error) {
	var out Request
	if len(raw) > 256*1024 {
		return out, Invalid("BODY_TOO_LARGE", "查询请求超过限制")
	}
	value, e := contract.Decode(raw)
	if e != nil {
		return out, Invalid("INVALID_QUERY", "查询必须是单一且无重复字段的 JSON")
	}
	if validator == nil {
		return out, fault.New(fault.Unavailable, "QUERY_VALIDATOR_UNAVAILABLE", "查询验证器不可用")
	}
	if e = validator.Validate("QueryRequest", value); e != nil {
		return out, Invalid("INVALID_QUERY", "查询不符合接口定义")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	decoder.DisallowUnknownFields()
	if e = decoder.Decode(&out); e != nil {
		return out, Invalid("INVALID_QUERY", "查询字段无效")
	}
	if e = Validate(&out); e != nil {
		return Request{}, e
	}
	return out, nil
}
func Validate(r *Request) error {
	if r.Kind != "events" && r.Kind != "trend" && r.Kind != "journey" && r.Kind != "funnel" {
		return Invalid("UNSUPPORTED_QUERY_KIND", "查询类型尚未实现", "/kind")
	}
	if r.Environment != "development" && r.Environment != "staging" && r.Environment != "production" {
		return Invalid("INVALID_ENVIRONMENT", "环境无效", "/environment")
	}
	if r.IdentityRevision != nil || r.Subject == "resolved_person" {
		return Invalid("IDENTITY_RESOLUTION_UNAVAILABLE", "尚无可查询的身份关联版本", "/subject")
	}
	if r.Subject != "" && r.Subject != "uid" && r.Subject != "anonymous" {
		return Invalid("INVALID_SUBJECT", "统计主体无效", "/subject")
	}
	if r.TimeRange.From.IsZero() || r.TimeRange.To.IsZero() || !r.TimeRange.From.Before(r.TimeRange.To) {
		return Invalid("INVALID_TIME_RANGE", "时间范围必须为非空半开区间", "/time_range")
	}
	window := 31 * 24 * time.Hour
	if r.Kind == "journey" || r.Kind == "events" || r.Kind == "funnel" {
		window = 7 * 24 * time.Hour
	}
	if r.TimeRange.To.Sub(r.TimeRange.From) > window {
		return Invalid("QUERY_COST_EXCEEDED", "查询时间范围超过预算", "/time_range")
	}
	if r.TimeRange.From.Year() < 1970 || r.TimeRange.To.Year() > 2100 {
		return Invalid("INVALID_TIME_RANGE", "查询时间超出支持范围", "/time_range")
	}
	if r.Timezone == "" || r.Timezone == "Local" || len(r.Timezone) > 64 {
		return Invalid("INVALID_TIMEZONE", "请选择 IANA 时区", "/timezone")
	}
	if _, e := time.LoadLocation(r.Timezone); e != nil {
		return Invalid("INVALID_TIMEZONE", "时区无效", "/timezone")
	}
	if r.Limit == 0 {
		r.Limit = 200
	}
	if r.Limit < 1 || r.Limit > 500 {
		return Invalid("INVALID_LIMIT", "结果大小必须在 1 到 500 之间", "/limit")
	}
	if r.Cursor != "" {
		return Invalid("UNSUPPORTED_QUERY_CURSOR", "此版本仅支持有界快照，请缩小时间范围", "/cursor")
	}
	if len(r.EventKeys) > 20 || len(r.GroupBy) > 3 || len(r.Metrics) > 10 {
		return Invalid("QUERY_COST_EXCEEDED", "事件、分组或指标数量超过预算")
	}
	seen := map[string]bool{}
	for _, k := range r.EventKeys {
		if !eventKey.MatchString(k) || seen[k] {
			return Invalid("INVALID_EVENT_KEYS", "事件标识必须有效且不能重复", "/event_keys")
		}
		seen[k] = true
	}
	nodes := 0
	if e := validateFilter(r.Filter, 0, &nodes); e != nil {
		return e
	}
	for i := range r.GroupBy {
		if e := validateField(&r.GroupBy[i]); e != nil {
			return e
		}
	}
	for i := range r.Metrics {
		m := &r.Metrics[i]
		if m.Op != "count" && m.Op != "unique" && m.Op != "sum" && m.Op != "avg" && m.Op != "min" && m.Op != "max" && m.Op != "p95" {
			return Invalid("INVALID_METRIC", "指标运算无效", "/metrics")
		}
		if m.Accuracy == "" {
			m.Accuracy = "exact"
		}
		if m.Accuracy != "exact" && m.Accuracy != "approximate" {
			return Invalid("INVALID_ACCURACY", "精度无效", "/metrics")
		}
		if m.Accuracy == "approximate" && m.Op != "unique" && m.Op != "p95" {
			return Invalid("INVALID_ACCURACY", "此指标仅支持精确结果", "/metrics")
		}
		if m.Field != nil {
			if e := validateField(m.Field); e != nil {
				return e
			}
		}
		if m.Op == "count" && m.Field != nil {
			return Invalid("INVALID_METRIC", "count 统计事件数，不接受字段", "/metrics")
		}
		if m.Op != "count" && m.Op != "unique" && m.Field == nil {
			return Invalid("INVALID_METRIC", "此指标必须指定字段", "/metrics")
		}
		if m.Op == "unique" && m.Field == nil && r.Subject == "" {
			return Invalid("SUBJECT_REQUIRED", "UV 必须指定 uid 或 anonymous 主体", "/subject")
		}
	}
	if r.Kind == "trend" {
		if len(r.Metrics) == 0 {
			r.Metrics = []Metric{{Op: "count", Accuracy: "exact"}}
		}
		if r.Interval != "" && r.Interval != "minute" && r.Interval != "hour" && r.Interval != "day" {
			return Invalid("INVALID_INTERVAL", "时间分组无效", "/interval")
		}
	} else if r.Interval != "" || len(r.Metrics) > 0 || len(r.GroupBy) > 0 {
		return Invalid("INVALID_QUERY_OPTIONS", "此查询类型不接受分组、指标或间隔")
	}
	if r.Kind == "journey" {
		if r.Subject == "" {
			return Invalid("SUBJECT_REQUIRED", "轨迹必须指定 uid 或 anonymous 主体", "/subject")
		}
		if r.IdentityFilter == nil {
			return Invalid("IDENTITY_REQUIRED", "轨迹必须限定单个身份", "/identity_filter")
		}
	}
	if r.IdentityFilter != nil {
		f := r.IdentityFilter
		if (f.UID == nil) == (f.AnonymousID == nil) {
			return Invalid("INVALID_IDENTITY_FILTER", "必须指定且仅指定一个身份", "/identity_filter")
		}
		if f.UID != nil && (!utf8.ValidString(*f.UID) || strings.ContainsRune(*f.UID, 0) || strings.TrimSpace(*f.UID) == "" || utf8.RuneCountInString(*f.UID) > 128 || r.Subject != "uid") {
			return Invalid("INVALID_IDENTITY_FILTER", "UID 过滤必须匹配 uid 主体", "/identity_filter")
		}
		if f.AnonymousID != nil && (len(*f.AnonymousID) != 36 || r.Subject != "anonymous") {
			return Invalid("INVALID_IDENTITY_FILTER", "匿名身份过滤必须匹配 anonymous 主体", "/identity_filter")
		}
	}
	if r.Kind == "funnel" {
		if r.Subject == "" {
			return Invalid("SUBJECT_REQUIRED", "漏斗必须指定统计主体", "/subject")
		}
		f := r.Funnel
		if f == nil || len(f.Steps) < 2 || len(f.Steps) > 8 || f.WindowSeconds < 1 || f.WindowSeconds > 604800 || f.Mode != "ordered_with_interleaving" {
			return Invalid("INVALID_FUNNEL", "漏斗配置无效", "/funnel")
		}
		for i := range f.Steps {
			step := &f.Steps[i]
			if !eventKey.MatchString(step.EventKey) || step.SchemaVersion < 1 || step.SchemaVersion > MaxSafeInteger {
				return Invalid("INVALID_FUNNEL_STEP", "步骤事件或版本无效", "/funnel/steps")
			}
			if e := validateFilter(step.Filter, 0, &nodes); e != nil {
				return e
			}
		}
		if len(f.SameEntity) > 8 || len(f.SameEntity) == 1 {
			return Invalid("INVALID_SAME_ENTITY", "实体约束必须包含两个以上有效步骤", "/funnel/same_entity")
		}
		seen := map[int]bool{}
		for i := range f.SameEntity {
			s := &f.SameEntity[i]
			if s.StepIndex < 0 || s.StepIndex >= len(f.Steps) || seen[s.StepIndex] {
				return Invalid("INVALID_SAME_ENTITY", "实体步骤索引无效或重复", "/funnel/same_entity")
			}
			seen[s.StepIndex] = true
			if e := validateField(&s.Field); e != nil {
				return e
			}
		}
	} else if r.Funnel != nil {
		return Invalid("INVALID_QUERY_OPTIONS", "只有漏斗查询接受 funnel 参数", "/funnel")
	}
	return nil
}
func validateField(f *Field) error {
	if f == nil {
		return Invalid("INVALID_FIELD", "字段不能为空")
	}
	if f.ResolvedType != "" {
		return Invalid("INVALID_FIELD", "不能指定服务端字段类型")
	}
	if f.Kind == "system" {
		kind, ok := systemTypes[f.Key]
		if !ok || f.EventKey != "" || f.SchemaVersion != 0 {
			return Invalid("INVALID_FIELD", "系统字段无效")
		}
		f.ResolvedType = kind
		return nil
	}
	if f.Kind != "property" || !propertyKey.MatchString(f.Key) || !eventKey.MatchString(f.EventKey) || f.SchemaVersion < 1 || f.SchemaVersion > MaxSafeInteger {
		return Invalid("INVALID_FIELD", "属性必须限定事件、已发布版本和字段")
	}
	return nil
}
func validateFilter(f *Filter, depth int, nodes *int) error {
	if f == nil {
		return nil
	}
	*nodes++
	if depth > 8 || *nodes > 100 {
		return Invalid("QUERY_COST_EXCEEDED", "过滤深度或节点数量超过预算", "/filter")
	}
	if f.Op == "and" || f.Op == "or" {
		if f.Field != nil || f.Value != nil || len(f.Args) < 1 || len(f.Args) > 20 {
			return Invalid("INVALID_FILTER", "逻辑过滤结构无效", "/filter")
		}
		for _, a := range f.Args {
			if a == nil {
				return Invalid("INVALID_FILTER", "过滤节点不能为空")
			}
			if e := validateFilter(a, depth+1, nodes); e != nil {
				return e
			}
		}
		return nil
	}
	if len(f.Args) > 0 {
		return Invalid("INVALID_FILTER", "比较过滤不能包含 args", "/filter")
	}
	if e := validateField(f.Field); e != nil {
		return e
	}
	switch f.Op {
	case "is_missing", "is_present":
		if f.Value != nil {
			return Invalid("INVALID_FILTER", "存在性过滤不能带值")
		}
		return nil
	case "eq", "neq", "in", "gt", "gte", "lt", "lte", "prefix", "contains":
	default:
		return Invalid("INVALID_FILTER_OPERATOR", "过滤运算不受支持")
	}
	if f.Value == nil {
		return Invalid("INVALID_FILTER_VALUE", "过滤值不能为 null")
	}
	if a, ok := f.Value.([]any); ok {
		if len(a) > 20 {
			return Invalid("INVALID_FILTER_VALUE", "值数组最多包含 20 个值")
		}
		kind := ""
		for _, v := range a {
			k, e := scalarType(v)
			if e != nil {
				return e
			}
			if kind != "" && kind != k {
				return Invalid("INVALID_FILTER_VALUE", "值数组必须同类型")
			}
			kind = k
		}
		if f.Op != "in" && f.Op != "eq" && f.Op != "neq" {
			return Invalid("INVALID_FILTER_VALUE", "此运算不接受值数组")
		}
	} else {
		if _, e := scalarType(f.Value); e != nil {
			return e
		}
		if f.Op == "in" {
			return Invalid("INVALID_FILTER_VALUE", "in 必须使用数组")
		}
	}
	return nil
}
func scalarType(v any) (string, error) {
	switch n := v.(type) {
	case string:
		if len([]rune(n)) <= 1024 {
			return "string", nil
		}
	case bool:
		return "boolean", nil
	case json.Number:
		f, e := n.Float64()
		if e == nil && !math.IsInf(f, 0) && math.Abs(f) <= MaxSafeInteger {
			return "number", nil
		}
	case float64:
		if !math.IsNaN(n) && !math.IsInf(n, 0) && math.Abs(n) <= MaxSafeInteger {
			return "number", nil
		}
	}
	return "", Invalid("INVALID_FILTER_VALUE", fmt.Sprintf("过滤值必须为有界标量"))
}
