package analysis

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

type Plan struct {
	SQL        string
	Parameters map[string]string
	Columns    []Column
	Kind       string
	Limit      int
	Accuracy   string
}
type planner struct {
	params map[string]string
	next   int
}

var identifier = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_]{0,127}$`)

func (p *planner) bind(kind, value string) string {
	name := fmt.Sprintf("p%d", p.next)
	p.next++
	p.params[name] = value
	return "{" + name + ":" + kind + "}"
}
func (p *planner) scalar(kind string, value any) (string, error) {
	switch kind {
	case "string":
		s, ok := value.(string)
		if !ok {
			return "", Invalid("INVALID_FILTER_TYPE", "字符串值无效")
		}
		return p.bind("String", s), nil
	case "datetime":
		parsed, e := parseDatetime(value)
		if e != nil {
			return "", e
		}
		return p.bind("DateTime64(9, 'UTC')", parsed.UTC().Format("2006-01-02 15:04:05.000000000")), nil
	case "boolean":
		v, ok := value.(bool)
		if !ok {
			return "", Invalid("INVALID_FILTER_TYPE", "布尔值无效")
		}
		if v {
			return p.bind("UInt8", "1"), nil
		}
		return p.bind("UInt8", "0"), nil
	case "integer", "number":
		var v string
		switch n := value.(type) {
		case json.Number:
			v = n.String()
		case float64:
			v = strconv.FormatFloat(n, 'g', -1, 64)
		default:
			return "", Invalid("INVALID_FILTER_TYPE", "数值无效")
		}
		return p.bind("Float64", v), nil
	}
	return "", Invalid("UNSUPPORTED_FIELD_TYPE", "不支持的字段类型")
}

type fieldSQL struct{ value, present, scope, valid string }

func (p *planner) field(f Field) (fieldSQL, error) {
	if f.Kind == "system" {
		if _, ok := systemTypes[f.Key]; !ok {
			return fieldSQL{}, Invalid("INVALID_FIELD", "系统字段无效")
		}
		present := "1"
		if f.Key != "event_key" && f.Key != "event_time" {
			present = "(isNotNull(" + f.Key + ") AND " + f.Key + "!='')"
		}
		return fieldSQL{f.Key, present, "1", "1"}, nil
	}
	if f.Kind != "property" || !propertyKey.MatchString(f.Key) || !eventKey.MatchString(f.EventKey) {
		return fieldSQL{}, Invalid("INVALID_FIELD", "属性字段无效")
	}
	key := p.bind("String", f.Key)
	scope := "(event_key=" + p.bind("String", f.EventKey) + " AND schema_version=" + p.bind("UInt64", strconv.FormatInt(f.SchemaVersion, 10)) + ")"
	present := "JSONHas(properties_json," + key + ")"
	jsonType := "JSONType(properties_json," + key + ")"
	extract := ""
	switch f.ResolvedType {
	case "string":
		extract = "if(" + jsonType + "='String',JSONExtractString(properties_json," + key + "),NULL)"
	case "boolean":
		extract = "if(" + jsonType + "='Bool',JSONExtractBool(properties_json," + key + "),NULL)"
	case "number", "integer":
		extract = "if(" + jsonType + " IN ('Int64','UInt64','Float64'),JSONExtractFloat(properties_json," + key + "),NULL)"
	case "array:string", "array:boolean", "array:number", "array:integer":
		item := "String"
		if f.ResolvedType == "array:boolean" {
			item = "Bool"
		}
		if f.ResolvedType == "array:number" || f.ResolvedType == "array:integer" {
			item = "Float64"
		}
		extract = "JSONExtract(properties_json," + key + ",'Array(" + item + ")')"
		return fieldSQL{extract, present, scope, jsonType + "='Array'"}, nil
	default:
		return fieldSQL{}, Invalid("UNRESOLVED_FIELD", "属性字段尚未解析")
	}
	return fieldSQL{"if(" + scope + "," + extract + ",NULL)", present, scope, "1"}, nil
}
func (p *planner) filter(f *Filter) (string, error) {
	if f == nil {
		return "1", nil
	}
	if f.Op == "and" || f.Op == "or" {
		parts := make([]string, 0, len(f.Args))
		for _, a := range f.Args {
			s, e := p.filter(a)
			if e != nil {
				return "", e
			}
			parts = append(parts, s)
		}
		join := " AND "
		if f.Op == "or" {
			join = " OR "
		}
		return "(" + strings.Join(parts, join) + ")", nil
	}
	if f.Field == nil {
		return "", Invalid("INVALID_FIELD", "过滤字段缺失")
	}
	field, e := p.field(*f.Field)
	if e != nil {
		return "", e
	}
	if f.Op == "is_present" {
		return "(" + field.scope + " AND " + field.present + ")", nil
	}
	if f.Op == "is_missing" {
		return "(" + field.scope + " AND NOT " + field.present + ")", nil
	}
	guard := "(" + field.scope + " AND " + field.present + " AND " + field.valid + " AND "
	kind := f.Field.ResolvedType
	if strings.HasPrefix(kind, "array:") {
		item := strings.TrimPrefix(kind, "array:")
		if f.Op == "contains" {
			value, e := p.scalar(item, f.Value)
			if e != nil {
				return "", e
			}
			return guard + "has(" + field.value + "," + value + "))", nil
		}
		values, ok := f.Value.([]any)
		if !ok {
			return "", Invalid("INVALID_FILTER_TYPE", "数组比较值无效")
		}
		parts := []string{fmt.Sprintf("length(%s)=%d", field.value, len(values))}
		for i, v := range values {
			param, e := p.scalar(item, v)
			if e != nil {
				return "", e
			}
			parts = append(parts, fmt.Sprintf("%s[%d]=%s", field.value, i+1, param))
		}
		expression := "(" + strings.Join(parts, " AND ") + ")"
		if f.Op == "neq" {
			expression = "NOT " + expression
		}
		return guard + expression + ")", nil
	}
	if f.Op == "in" {
		values, ok := f.Value.([]any)
		if !ok {
			return "", Invalid("INVALID_FILTER_TYPE", "in 值必须为数组")
		}
		if len(values) == 0 {
			return "0", nil
		}
		parts := make([]string, 0, len(values))
		for _, v := range values {
			s, e := p.scalar(kind, v)
			if e != nil {
				return "", e
			}
			parts = append(parts, s)
		}
		return guard + field.value + " IN (" + strings.Join(parts, ",") + "))", nil
	}
	value, e := p.scalar(kind, f.Value)
	if e != nil {
		return "", e
	}
	operators := map[string]string{"eq": "=", "neq": "!=", "gt": ">", "gte": ">=", "lt": "<", "lte": "<="}
	if op, ok := operators[f.Op]; ok {
		return guard + field.value + op + value + ")", nil
	}
	if f.Op == "prefix" {
		return guard + "startsWith(" + field.value + "," + value + "))", nil
	}
	if f.Op == "contains" {
		return guard + "position(" + field.value + "," + value + ")>0)", nil
	}
	return "", Invalid("INVALID_FILTER_OPERATOR", "过滤运算无效")
}
func Compile(database, table string, scope Scope, r Request) (Plan, error) {
	if !identifier.MatchString(database) || !identifier.MatchString(table) {
		return Plan{}, Invalid("INVALID_QUERY_TABLE", "查询存储配置无效")
	}
	if scope.ProjectID == "" || scope.Environment != r.Environment {
		return Plan{}, Invalid("INVALID_QUERY_SCOPE", "查询范围无效")
	}
	if e := CheckResolved(r); e != nil {
		return Plan{}, e
	}
	p := planner{params: map[string]string{}}
	base := " FROM `" + database + "`.`" + table + "` WHERE project_id=" + p.bind("String", scope.ProjectID) + " AND environment=" + p.bind("String", scope.Environment) + " AND event_time>=" + p.bind("DateTime64(9, 'UTC')", r.TimeRange.From.UTC().Format("2006-01-02 15:04:05.000000000")) + " AND event_time<" + p.bind("DateTime64(9, 'UTC')", r.TimeRange.To.UTC().Format("2006-01-02 15:04:05.000000000"))
	if len(r.EventKeys) > 0 {
		events := make([]string, 0, len(r.EventKeys))
		for _, key := range r.EventKeys {
			events = append(events, p.bind("String", key))
		}
		base += " AND event_key IN (" + strings.Join(events, ",") + ")"
	}
	if r.IdentityFilter != nil {
		if r.IdentityFilter.UID != nil {
			base += " AND uid=" + p.bind("String", *r.IdentityFilter.UID)
		} else if r.IdentityFilter.AnonymousID != nil {
			base += " AND anonymous_id=" + p.bind("String", *r.IdentityFilter.AnonymousID)
		}
	}
	predicate, e := p.filter(r.Filter)
	if e != nil {
		return Plan{}, e
	}
	base += " AND " + predicate
	plan := Plan{Parameters: p.params, Kind: r.Kind, Limit: r.Limit, Accuracy: "not_applicable"}
	switch r.Kind {
	case "events", "journey":
		plan.SQL = "SELECT event_id,source_id,source_kind,event_key,schema_version,toUnixTimestamp64Nano(event_time) AS event_time_ns,toUnixTimestamp64Nano(received_at) AS received_at_ns,uid,anonymous_id,session_id,page_view_id,operation_id,context_json,properties_json" + base + " ORDER BY event_time,ifNull(session_id,''),ifNull(tab_id,''),ifNull(sequence,0),source_id,event_id LIMIT " + strconv.Itoa(r.Limit+1)
		plan.Columns = []Column{{Key: "event_key", Type: "string"}, {Key: "event_time", Type: "datetime"}, {Key: "uid", Type: "string"}, {Key: "anonymous_id", Type: "string"}, {Key: "context", Type: "object"}, {Key: "properties", Type: "object"}}
	case "trend":
		selectSQL := []string{}
		groups := []string{}
		if r.Interval != "" {
			bucket := map[string]string{"minute": "toStartOfMinute", "hour": "toStartOfHour", "day": "toStartOfDay"}[r.Interval]
			if bucket == "" {
				return Plan{}, Invalid("INVALID_INTERVAL", "时间间隔无效")
			}
			selectSQL = append(selectSQL, "toUnixTimestamp64Milli(toDateTime64("+bucket+"(event_time,"+p.bind("String", r.Timezone)+"),3,'UTC')) AS bucket_ms")
			groups = append(groups, "bucket_ms")
			plan.Columns = append(plan.Columns, Column{Key: "bucket", Type: "datetime"})
		}
		for i, f := range r.GroupBy {
			sql, e := p.field(f)
			if e != nil {
				return Plan{}, e
			}
			alias := fmt.Sprintf("group_%d", i)
			selectSQL = append(selectSQL, sql.value+" AS "+alias)
			groups = append(groups, alias)
			plan.Columns = append(plan.Columns, Column{Key: alias, Type: f.ResolvedType, Label: f.Key})
			if f.Kind == "property" {
				presence := alias + "_present"
				selectSQL = append(selectSQL, "("+sql.scope+" AND "+sql.present+") AS "+presence)
				groups = append(groups, presence)
				plan.Columns = append(plan.Columns, Column{Key: presence, Type: "boolean", Label: f.Key + " present"})
			}
		}
		plan.Accuracy = "exact"
		for i, m := range r.Metrics {
			expression, e := p.metric(m, r.Subject)
			if e != nil {
				return Plan{}, e
			}
			alias := fmt.Sprintf("metric_%d", i)
			selectSQL = append(selectSQL, expression+" AS "+alias)
			plan.Columns = append(plan.Columns, Column{Key: alias, Type: "number", Label: m.Op})
			if m.Accuracy == "approximate" {
				plan.Accuracy = "approximate"
			}
		}
		selectSQL = append(selectSQL, "if(count()=0,NULL,toUnixTimestamp64Nano(max(received_at))) AS watermark_ns")
		plan.SQL = "SELECT " + strings.Join(selectSQL, ",") + base
		if len(groups) > 0 {
			plan.SQL += " GROUP BY " + strings.Join(groups, ",") + " ORDER BY " + strings.Join(groups, ",")
		}
		plan.SQL += " LIMIT " + strconv.Itoa(r.Limit+1)
	case "funnel":
		subject := "uid"
		if r.Subject == "anonymous" {
			subject = "anonymous_id"
		}
		selectSQL := []string{subject + " AS subject_id", "event_id", "source_id", "session_id", "tab_id", "toUnixTimestamp64Nano(event_time) AS event_time_ns", "toUnixTimestamp64Nano(received_at) AS received_at_ns", "ifNull(sequence,0) AS sequence"}
		stepMatches := []string{}
		for i, s := range r.Funnel.Steps {
			filter, e := p.filter(s.Filter)
			if e != nil {
				return Plan{}, e
			}
			match := "(event_key=" + p.bind("String", s.EventKey) + " AND schema_version=" + p.bind("UInt64", strconv.FormatInt(s.SchemaVersion, 10)) + " AND " + filter + ")"
			selectSQL = append(selectSQL, match+fmt.Sprintf(" AS step_%d", i))
			stepMatches = append(stepMatches, match)
		}
		for _, entity := range r.Funnel.SameEntity {
			field, e := p.field(entity.Field)
			if e != nil {
				return Plan{}, e
			}
			selectSQL = append(selectSQL, field.value+fmt.Sprintf(" AS entity_%d", entity.StepIndex))
		}
		base += " AND isNotNull(" + subject + ") AND " + subject + "!='' AND (" + strings.Join(stepMatches, " OR ") + ")"
		plan.SQL = "SELECT " + strings.Join(selectSQL, ",") + base + " ORDER BY subject_id,event_time,ifNull(session_id,''),ifNull(tab_id,''),sequence,source_id,event_id LIMIT 100001"
		plan.Accuracy = "exact"
		plan.Columns = []Column{{Key: "step_index", Type: "integer"}, {Key: "event_key", Type: "string"}, {Key: "subjects", Type: "integer"}, {Key: "conversion_rate", Type: "number"}}
	default:
		return Plan{}, Invalid("UNSUPPORTED_QUERY_KIND", "查询类型尚未实现")
	}
	return plan, nil
}
func (p *planner) metric(m Metric, subject string) (string, error) {
	if m.Op == "count" {
		return "count()", nil
	}
	var f Field
	if m.Field != nil {
		f = *m.Field
	} else {
		key := "uid"
		if subject == "anonymous" {
			key = "anonymous_id"
		}
		f = Field{Kind: "system", Key: key, ResolvedType: "string"}
	}
	field, e := p.field(f)
	if e != nil {
		return "", e
	}
	valid := "(" + field.scope + " AND " + field.present + " AND isNotNull(" + field.value + "))"
	if m.Op == "unique" {
		fn := "uniqExactIf"
		if m.Accuracy == "approximate" {
			fn = "uniqCombined64If"
		}
		return fn + "(" + field.value + "," + valid + ")", nil
	}
	functions := map[string]string{"sum": "sumIf", "avg": "avgIf", "min": "minIf", "max": "maxIf"}
	fn, ok := functions[m.Op]
	if m.Op == "p95" {
		fn = "quantileExactIf(0.95)"
		if m.Accuracy == "approximate" {
			fn = "quantileTDigestIf(0.95)"
		}
		ok = true
	}
	if !ok {
		return "", Invalid("INVALID_METRIC", "指标运算无效")
	}
	return "if(countIf(" + valid + ")=0,NULL," + fn + "(" + field.value + "," + valid + "))", nil
}
