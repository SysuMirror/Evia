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
	if r.Kind != "events" && r.Kind != "trend" && r.Kind != "journey" && r.Kind != "funnel" && r.Kind != "retention" && r.Kind != "segment" && r.Kind != "path" && r.Kind != "attribution" {
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
	if r.Kind == "journey" || r.Kind == "events" || r.Kind == "funnel" || r.Kind == "segment" || r.Kind == "path" || r.Kind == "attribution" {
		window = 7 * 24 * time.Hour
	}
	if r.Kind == "retention" {
		// Structural policy must be settled before the window is derived from
		// granularity and periods, otherwise malformed values mask the real
		// configuration error. ValidateConfiguration is idempotent, so the
		// shared call at the end of Validate simply re-confirms it.
		if e := ValidateConfiguration(r); e != nil {
			return e
		}
		window = retentionWindow(r.Retention)
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
	return ValidateConfiguration(r)
}

// ValidateConfiguration validates structural policy and normalizes defaults and
// system field types. It does not validate environment, time range or timezone;
// execution callers must use Validate with the actual execution context.
func ValidateConfiguration(r *Request) error {
	if r.Kind != "events" && r.Kind != "trend" && r.Kind != "journey" && r.Kind != "funnel" && r.Kind != "retention" && r.Kind != "segment" && r.Kind != "path" && r.Kind != "attribution" {
		return Invalid("UNSUPPORTED_QUERY_KIND", "查询类型尚未实现", "/kind")
	}
	if r.IdentityRevision != nil || r.Subject == "resolved_person" {
		return Invalid("IDENTITY_RESOLUTION_UNAVAILABLE", "尚无可查询的身份关联版本", "/subject")
	}
	if r.Subject != "" && r.Subject != "uid" && r.Subject != "anonymous" {
		return Invalid("INVALID_SUBJECT", "统计主体无效", "/subject")
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
	if e := validateEventSelection(*r); e != nil {
		return e
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
		if m.Op != "count" && m.Op != "unique" && m.Op != "sum" && m.Op != "avg" && m.Op != "min" && m.Op != "max" && m.Op != "p50" && m.Op != "p90" && m.Op != "p95" && m.Op != "p99" {
			return Invalid("INVALID_METRIC", "指标运算无效", "/metrics")
		}
		if m.Accuracy == "" {
			m.Accuracy = "exact"
		}
		if m.Accuracy != "exact" && m.Accuracy != "approximate" {
			return Invalid("INVALID_ACCURACY", "精度无效", "/metrics")
		}
		if m.Accuracy == "approximate" && m.Op != "unique" && m.Op != "p50" && m.Op != "p90" && m.Op != "p95" && m.Op != "p99" {
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
		if r.Interval != "" && r.Interval != "minute" && r.Interval != "hour" && r.Interval != "day" && r.Interval != "week" {
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
	if r.Kind == "retention" {
		if e := validateRetention(r); e != nil {
			return e
		}
	} else if r.Retention != nil {
		return Invalid("INVALID_QUERY_OPTIONS", "只有留存查询接受 retention 参数", "/retention")
	}
	if r.Kind == "segment" {
		if e := validateSegment(r); e != nil {
			return e
		}
	} else if r.Segment != nil {
		return Invalid("INVALID_QUERY_OPTIONS", "只有分群查询接受 segment 参数", "/segment")
	}
	if r.Kind == "path" {
		if e := validatePath(r); e != nil {
			return e
		}
	} else if r.Path != nil {
		return Invalid("INVALID_QUERY_OPTIONS", "只有路径查询接受 path 参数", "/path")
	}
	if r.Kind == "attribution" {
		if e := validateAttribution(r); e != nil {
			return e
		}
	} else if r.Attribution != nil {
		return Invalid("INVALID_QUERY_OPTIONS", "只有归因查询接受 attribution 参数", "/attribution")
	}
	return nil
}

// validatePath enforces subject-level path aggregation: an explicit uid or
// anonymous subject, a fixed depth of 2..8 events, and an optional pinned
// start event. Every malformed value fails closed.
func validatePath(r *Request) error {
	if r.Subject != "uid" && r.Subject != "anonymous" {
		return Invalid("SUBJECT_REQUIRED", "路径查询必须指定 uid 或 anonymous 统计主体", "/subject")
	}
	p := r.Path
	if p == nil {
		return Invalid("INVALID_PATH", "路径配置缺失", "/path")
	}
	if p.Depth < 2 || p.Depth > 8 {
		return Invalid("INVALID_PATH_DEPTH", "路径深度必须在 2 到 8 之间", "/path/depth")
	}
	if len(r.EventKeys) > 0 {
		return Invalid("INVALID_EVENT_SELECTION", "路径查询通过可选起始事件固定事件版本", "/event_keys")
	}
	if p.StartEvent != nil {
		if !eventKey.MatchString(p.StartEvent.EventKey) || p.StartEvent.SchemaVersion < 1 || p.StartEvent.SchemaVersion > MaxSafeInteger {
			return Invalid("INVALID_PATH_START_EVENT", "路径起始事件或版本无效", "/path/start_event")
		}
		nodes := 0
		if e := validateFilter(p.StartEvent.Filter, 0, &nodes); e != nil {
			return e
		}
	}
	return nil
}

// validateAttribution enforces an explicit attribution model (single-touch
// last/first, linear equal-weight, U-shaped position-based or exponential
// time decay), an explicit subject, one pinned conversion, 1..8 distinct
// pinned touchpoints that never reuse the conversion pair, and a 1..604800
// second lookback window. Every malformed value fails closed.
func validateAttribution(r *Request) error {
	if r.Subject != "uid" && r.Subject != "anonymous" {
		return Invalid("SUBJECT_REQUIRED", "归因查询必须指定 uid 或 anonymous 统计主体", "/subject")
	}
	a := r.Attribution
	if a == nil {
		return Invalid("INVALID_ATTRIBUTION", "归因配置缺失", "/attribution")
	}
	if a.Model != "last_touch" && a.Model != "first_touch" && a.Model != "linear" && a.Model != "position_based" && a.Model != "time_decay" {
		return Invalid("INVALID_ATTRIBUTION_MODEL", "归因模型尚未实现", "/attribution/model")
	}
	if a.WindowSeconds < 1 || a.WindowSeconds > 604800 {
		return Invalid("INVALID_ATTRIBUTION_WINDOW", "归因窗口必须在 1 到 604800 秒之间", "/attribution/window_seconds")
	}
	if a.Model == "time_decay" && (a.DecayHalfLifeSeconds < 1 || a.DecayHalfLifeSeconds > 604800) {
		return Invalid("INVALID_ATTRIBUTION_HALF_LIFE", "时间衰减模型必须提供 1 到 604800 秒之间的半衰期", "/attribution/decay_half_life_seconds")
	}
	validateStep := func(step Step, code, message, pointer string) error {
		if !eventKey.MatchString(step.EventKey) || step.SchemaVersion < 1 || step.SchemaVersion > MaxSafeInteger {
			return Invalid(code, message, pointer)
		}
		nodes := 0
		return validateFilter(step.Filter, 0, &nodes)
	}
	if e := validateStep(a.Conversion, "INVALID_ATTRIBUTION_CONVERSION", "归因转化事件或版本无效", "/attribution/conversion"); e != nil {
		return e
	}
	if len(a.Touchpoints) < 1 || len(a.Touchpoints) > 8 {
		return Invalid("INVALID_ATTRIBUTION_TOUCHPOINTS", "归因触点必须在 1 到 8 个之间", "/attribution/touchpoints")
	}
	conversionPair := EventVersion{EventKey: a.Conversion.EventKey, SchemaVersion: a.Conversion.SchemaVersion}
	seen := map[EventVersion]bool{}
	for _, step := range a.Touchpoints {
		if e := validateStep(step, "INVALID_ATTRIBUTION_TOUCHPOINT", "归因触点事件或版本无效", "/attribution/touchpoints"); e != nil {
			return e
		}
		pair := EventVersion{EventKey: step.EventKey, SchemaVersion: step.SchemaVersion}
		if pair == conversionPair {
			return Invalid("INVALID_ATTRIBUTION_TOUCHPOINT", "归因触点不能与转化事件相同", "/attribution/touchpoints")
		}
		if seen[pair] {
			return Invalid("INVALID_ATTRIBUTION_TOUCHPOINT", "归因触点事件不能重复", "/attribution/touchpoints")
		}
		seen[pair] = true
	}
	if len(r.EventKeys) > 0 {
		return Invalid("INVALID_EVENT_SELECTION", "归因查询通过转化与触点固定事件版本", "/event_keys")
	}
	return nil
}

// validateSegment enforces subject-level segmentation: an explicit uid or
// anonymous subject, one pinned event, and one scalar field whose earliest
// per-subject value becomes that subject's unique group label. Arrays and
// datetimes are rejected because they do not define a unique label.
func validateSegment(r *Request) error {
	if r.Subject != "uid" && r.Subject != "anonymous" {
		return Invalid("SUBJECT_REQUIRED", "分群查询必须指定 uid 或 anonymous 统计主体", "/subject")
	}
	sg := r.Segment
	if sg == nil {
		return Invalid("INVALID_SEGMENT", "分群配置缺失", "/segment")
	}
	if len(r.EventKeys) > 0 {
		return Invalid("INVALID_EVENT_SELECTION", "分群查询通过分群事件固定事件版本", "/event_keys")
	}
	if !eventKey.MatchString(sg.Event.EventKey) || sg.Event.SchemaVersion < 1 || sg.Event.SchemaVersion > MaxSafeInteger {
		return Invalid("INVALID_SEGMENT_EVENT", "分群事件或版本无效", "/segment/event")
	}
	if e := validateField(&sg.Field); e != nil {
		return e
	}
	nodes := 0
	if e := validateFilter(sg.Event.Filter, 0, &nodes); e != nil {
		return e
	}
	return nil
}

// validateRetention enforces the closed retention surface: an explicit uid or
// anonymous subject, calendar day/week periods, at most twelve offsets, and
// pinned event versions on both boundary steps. Every malformed value fails
// closed.
func validateRetention(r *Request) error {
	if r.Subject != "uid" && r.Subject != "anonymous" {
		return Invalid("SUBJECT_REQUIRED", "留存查询必须指定 uid 或 anonymous 统计主体", "/subject")
	}
	rt := r.Retention
	if rt == nil {
		return Invalid("INVALID_RETENTION", "留存配置缺失", "/retention")
	}
	if rt.Granularity != "day" && rt.Granularity != "week" && rt.Granularity != "month" {
		return Invalid("INVALID_RETENTION_GRANULARITY", "留存粒度必须是 day、week 或 month", "/retention/granularity")
	}
	if rt.Periods < 1 || rt.Periods > 12 {
		return Invalid("INVALID_RETENTION_PERIODS", "留存期数必须在 1 到 12 之间", "/retention/periods")
	}
	if len(r.EventKeys) > 0 {
		return Invalid("INVALID_EVENT_SELECTION", "留存查询通过起止事件固定事件版本", "/event_keys")
	}
	nodes := 0
	for _, step := range []Step{rt.StartEvent, rt.ReturnEvent} {
		if !eventKey.MatchString(step.EventKey) || step.SchemaVersion < 1 || step.SchemaVersion > MaxSafeInteger {
			return Invalid("INVALID_RETENTION_STEP", "留存起止事件或版本无效", "/retention")
		}
		if e := validateFilter(step.Filter, 0, &nodes); e != nil {
			return e
		}
	}
	return nil
}

// retentionWindow bounds the scan span to the requested matrix size. A
// twelve-period month matrix is the largest v1 window at 372 days using the
// conservative 31-day period; the bounded raw pull and ClickHouse read guards
// still cap the actual work.
func retentionWindow(rt *Retention) time.Duration {
	period := 24 * time.Hour
	switch rt.Granularity {
	case "week":
		period = 7 * 24 * time.Hour
	case "month":
		// Calendar months vary in length, so the scan window uses the
		// conservative 31-day upper bound; cohort alignment itself is
		// calendar-based inside the evaluator, not duration-based.
		period = 31 * 24 * time.Hour
	}
	return time.Duration(rt.Periods) * period
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
	case "eq", "neq", "in", "not_in", "gt", "gte", "lt", "lte", "prefix", "not_prefix", "contains", "not_contains":
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
		if f.Op != "in" && f.Op != "not_in" && f.Op != "eq" && f.Op != "neq" {
			return Invalid("INVALID_FILTER_VALUE", "此运算不接受值数组")
		}
	} else {
		if _, e := scalarType(f.Value); e != nil {
			return e
		}
		if f.Op == "in" || f.Op == "not_in" {
			return Invalid("INVALID_FILTER_VALUE", "in/not_in 必须使用数组")
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

// validateEventSelection also protects direct compilation of normalized requests.
func validateEventSelection(r Request) error {
	if r.EventKeys != nil && r.EventVersions != nil {
		return Invalid("INVALID_EVENT_SELECTION", "事件标识与固定事件版本不能同时指定", "/event_keys", "/event_versions")
	}
	if len(r.EventKeys) > 20 || len(r.EventVersions) > 20 {
		return Invalid("QUERY_COST_EXCEEDED", "事件数量超过预算")
	}
	seen := map[string]bool{}
	for _, key := range r.EventKeys {
		if !eventKey.MatchString(key) || seen[key] {
			return Invalid("INVALID_EVENT_KEYS", "事件标识必须有效且不能重复", "/event_keys")
		}
		seen[key] = true
	}
	if r.EventVersions != nil && (len(r.EventVersions) == 0 || r.Kind == "funnel" || r.Kind == "retention" || r.Kind == "segment" || r.Kind == "path" || r.Kind == "attribution") {
		return Invalid("INVALID_EVENT_SELECTION", "固定事件版本不能为空且不能用于漏斗、留存、分群、路径或归因查询", "/event_versions")
	}
	for _, version := range r.EventVersions {
		if !eventKey.MatchString(version.EventKey) || version.SchemaVersion < 1 || version.SchemaVersion > MaxSafeInteger || seen[version.EventKey] {
			return Invalid("INVALID_EVENT_VERSIONS", "固定事件版本必须有效且每个事件只能选择一个版本", "/event_versions")
		}
		seen[version.EventKey] = true
	}
	if len(r.EventVersions) > 0 {
		return WalkFields(&r, func(field *Field) error {
			if field.Kind != "property" {
				return nil
			}
			for _, version := range r.EventVersions {
				if field.EventKey == version.EventKey && field.SchemaVersion == version.SchemaVersion {
					return nil
				}
			}
			return Invalid("QUERY_FIELD_VERSION_NOT_SELECTED", "属性必须属于固定选择的事件版本", "/event_versions")
		})
	}
	return nil
}
