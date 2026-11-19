package catalog

import (
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/howtomakeaname/Evia/backend/internal/ingestion"
)

func pointer(s string) string                { return strings.ReplaceAll(strings.ReplaceAll(s, "~", "~0"), "/", "~1") }
func issue(code, path, message string) Issue { return Issue{code, path, message} }
func number(v any) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, !math.IsNaN(n) && !math.IsInf(n, 0)
	case json.Number:
		f, e := n.Float64()
		return f, e == nil && !math.IsInf(f, 0)
	case int:
		return float64(n), true
	case int64:
		return float64(n), true
	}
	return 0, false
}
func object(v any) (map[string]any, bool) { m, ok := v.(map[string]any); return m, ok }
func array(v any) ([]any, bool) {
	switch a := v.(type) {
	case []any:
		return a, true
	case []string:
		r := make([]any, len(a))
		for i, s := range a {
			r[i] = s
		}
		return r, true
	}
	return nil, false
}
func sorted(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
func CheckDefinition(in DraftInput, publication bool) []Issue {
	out := CheckSchema(in.Schema)
	add := func(path, msg string) { out = append(out, issue("INVALID_DEFINITION", path, msg)) }
	if utf8.RuneCountInString(text(in.DisplayName)) > 128 || (in.DisplayName != nil || publication) && strings.TrimSpace(text(in.DisplayName)) == "" {
		add("/display_name", "显示名称必须为 1 到 128 个字符")
	}
	if utf8.RuneCountInString(text(in.Trigger)) > 2000 || publication && strings.TrimSpace(text(in.Trigger)) == "" {
		add("/trigger", "发布定义需要 1 到 2000 个字符的触发说明")
	}
	if len(in.AllowedSourceKinds) > 2 || publication && len(in.AllowedSourceKinds) == 0 {
		add("/allowed_source_kinds", "请选择事件来源")
	}
	seen := map[string]bool{}
	for _, v := range in.AllowedSourceKinds {
		if !ingestion.ValidSourceKind(v) || seen[v] {
			add("/allowed_source_kinds", "来源必须为 browser 或 server，且不能重复")
		}
		seen[v] = true
	}
	if in.FactKind != nil && !ValidFactKind(text(in.FactKind)) || publication && in.FactKind == nil {
		add("/fact_kind", "事实类型无效")
	}
	if text(in.FactKind) == FactKindServerConfirmed && seen[ingestion.SourceKindBrowser] {
		add("/allowed_source_kinds", "服务端确认事件不能允许浏览器来源")
	}
	if len(in.FieldMetadata) > 50 {
		add("/field_metadata", "字段元数据最多包含 50 个字段")
	}
	if b, e := json.Marshal(in.FieldMetadata); e != nil || len(b) > 32768 {
		add("/field_metadata", "字段元数据必须是有限大小的 JSON")
	} else {
		checkMetadata(in.FieldMetadata, "/field_metadata", 0, add)
	}
	if in.ExampleProperties != nil || publication {
		for _, v := range CheckSample(in.Schema, in.ExampleProperties) {
			v.Path = "/example_properties" + v.Path
			out = append(out, v)
		}
	}
	return out
}

func checkMetadata(value any, path string, depth int, add func(string, string)) {
	if depth > 8 {
		add(path, "字段元数据嵌套过深")
		return
	}
	switch v := value.(type) {
	case map[string]any:
		if len(v) > 50 {
			add(path, "元数据对象最多包含 50 个字段")
			return
		}
		for _, k := range sorted(v) {
			if utf8.RuneCountInString(k) > 128 {
				add(path, "元数据字段名过长")
				continue
			}
			checkMetadata(v[k], path+"/"+pointer(k), depth+1, add)
		}
	case []any:
		if len(v) > 50 {
			add(path, "元数据数组过长")
			return
		}
		for i, item := range v {
			checkMetadata(item, fmt.Sprintf("%s/%d", path, i), depth+1, add)
		}
	case string:
		if utf8.RuneCountInString(v) > 2000 {
			add(path, "元数据字符串过长")
		}
	case nil, bool:
	default:
		if n, ok := number(v); !ok || math.Abs(n) > float64(MaxInteger) {
			add(path, "元数据必须使用安全的 JSON 值")
		}
	}
}

// CheckSchema accepts a deliberately small, locally evaluated JSON Schema subset.
// Objects are flat; values are bounded scalars or homogeneous scalar arrays.
// Unknown keywords (including references and combinators) are rejected.
func CheckSchema(schema map[string]any) []Issue {
	out := make([]Issue, 0)
	add := func(path, msg string) { out = append(out, issue("INVALID_SCHEMA", path, msg)) }
	if b, e := json.Marshal(schema); e != nil || len(b) > 65536 {
		add("/schema", "Schema 大小超出限制")
		return out
	}
	allowed := map[string]bool{"$schema": true, "type": true, "properties": true, "required": true, "additionalProperties": true, "maxProperties": true, "title": true, "description": true}
	for _, k := range sorted(schema) {
		if !allowed[k] {
			add("/schema/"+pointer(k), "不支持此 JSON Schema 关键字")
		}
	}
	if version, ok := schema["$schema"]; ok && version != "https://json-schema.org/draft/2020-12/schema" {
		add("/schema/$schema", "仅支持 JSON Schema 2020-12 的本地子集")
	}
	if schema["type"] != "object" {
		add("/schema/type", "事件属性必须为 object")
	}
	if v, ok := schema["additionalProperties"].(bool); !ok || v {
		add("/schema/additionalProperties", "必须显式禁止额外属性")
	}
	properties, ok := object(schema["properties"])
	if !ok || len(properties) > 50 {
		add("/schema/properties", "必须定义最多 50 个平面属性")
		return out
	}
	checkAnnotation(schema, "/schema", add)
	if v, ok := schema["maxProperties"]; ok {
		n, valid := number(v)
		if !valid || n < 0 || n > 50 || math.Trunc(n) != n {
			add("/schema/maxProperties", "属性上限必须为 0 到 50 的整数")
		}
	}
	for _, k := range sorted(properties) {
		path := "/schema/properties/" + pointer(k)
		if !keyPattern.MatchString(k) {
			add(path, "属性名格式无效")
		}
		p, ok := object(properties[k])
		if !ok {
			add(path, "属性定义必须为对象")
			continue
		}
		checkProperty(p, path, true, add)
	}
	if v, ok := schema["required"]; ok {
		a, valid := array(v)
		if !valid || len(a) > 50 {
			add("/schema/required", "必填字段必须为有界数组")
		} else {
			seen := map[string]bool{}
			for _, v := range a {
				k, ok := v.(string)
				_, exists := properties[k]
				if !ok || !exists || seen[k] {
					add("/schema/required", "必填字段必须存在且不能重复")
				}
				seen[k] = true
			}
		}
	}
	return out
}
func checkAnnotation(s map[string]any, path string, add func(string, string)) {
	for _, k := range []string{"title", "description"} {
		if v, ok := s[k]; ok {
			str, ok := v.(string)
			if !ok || utf8.RuneCountInString(str) > 2000 {
				add(path+"/"+k, "说明必须为最多 2000 个字符的字符串")
			}
		}
	}
}
func checkProperty(p map[string]any, path string, allowArray bool, add func(string, string)) {
	if len(p) > 12 {
		add(path, "属性定义包含过多关键字")
		return
	}
	kind, _ := p["type"].(string)
	allowed := map[string]bool{"type": true, "title": true, "description": true}
	switch kind {
	case "string":
		for _, k := range []string{"minLength", "maxLength", "enum"} {
			allowed[k] = true
		}
	case "number", "integer":
		for _, k := range []string{"minimum", "maximum", "enum"} {
			allowed[k] = true
		}
	case "boolean":
		allowed["enum"] = true
	case "array":
		if !allowArray {
			add(path+"/type", "数组项必须为标量")
		}
		for _, k := range []string{"items", "minItems", "maxItems"} {
			allowed[k] = true
		}
	default:
		add(path+"/type", "只支持 string、number、integer、boolean 或标量数组")
	}
	for _, k := range sorted(p) {
		if !allowed[k] {
			add(path+"/"+pointer(k), "不支持此属性关键字")
		}
	}
	checkAnnotation(p, path, add)
	for _, bounds := range [][3]string{{"minLength", "maxLength", "1024"}, {"minItems", "maxItems", "20"}} {
		max := float64(1024)
		if bounds[0] == "minItems" {
			max = 20
		}
		lo, hi := float64(0), max
		if v, ok := p[bounds[0]]; ok {
			n, valid := number(v)
			if !valid || n < 0 || n > max || math.Trunc(n) != n {
				add(path+"/"+bounds[0], "下限必须是范围内整数")
			} else {
				lo = n
			}
		}
		if v, ok := p[bounds[1]]; ok {
			n, valid := number(v)
			if !valid || n < 0 || n > max || math.Trunc(n) != n {
				add(path+"/"+bounds[1], "上限必须是范围内整数")
			} else {
				hi = n
			}
		}
		if lo > hi {
			add(path, "下限不能大于上限")
		}
	}
	lo, hi := -float64(MaxInteger), float64(MaxInteger)
	for _, key := range []string{"minimum", "maximum"} {
		if v, ok := p[key]; ok {
			n, valid := number(v)
			if !valid || math.Abs(n) > float64(MaxInteger) || kind == "integer" && math.Trunc(n) != n {
				add(path+"/"+key, "数值边界必须在 JavaScript 安全范围内")
			} else if key == "minimum" {
				lo = n
			} else {
				hi = n
			}
		}
	}
	if lo > hi {
		add(path, "minimum 不能大于 maximum")
	}
	if v, ok := p["enum"]; ok {
		a, valid := array(v)
		if !valid || len(a) < 1 || len(a) > 100 {
			add(path+"/enum", "枚举必须包含 1 到 100 个标量")
		} else {
			seen := map[string]bool{}
			for _, value := range a {
				clean := map[string]any{"type": kind}
				if len(valueIssues(clean, value, "")) > 0 {
					add(path+"/enum", "枚举值必须符合属性类型和大小限制")
				}
				h, e := Hash(value)
				if e != nil || seen[h] {
					add(path+"/enum", "枚举值不能重复")
				}
				seen[h] = true
			}
		}
	}
	if kind == "array" {
		items, ok := object(p["items"])
		if !ok {
			add(path+"/items", "必须定义数组项类型")
		} else {
			checkProperty(items, path+"/items", false, add)
		}
	}
}
