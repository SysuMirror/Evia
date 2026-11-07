package catalog

import (
	"fmt"
	"math"
	"unicode/utf8"
)

func bound(s map[string]any, k string, fallback float64) float64 {
	if v, ok := number(s[k]); ok {
		return v
	}
	return fallback
}
func CheckSample(schema map[string]any, sample map[string]any) []Issue {
	out := make([]Issue, 0)
	properties, _ := object(schema["properties"])
	if len(sample) > 50 || float64(len(sample)) > bound(schema, "maxProperties", 50) {
		out = append(out, issue("TOO_MANY_PROPERTIES", "", "属性数量超出限制"))
		return out
	}
	required, _ := array(schema["required"])
	for _, v := range required {
		k, _ := v.(string)
		if _, ok := sample[k]; !ok {
			out = append(out, issue("REQUIRED", "/"+pointer(k), "缺少必填属性"))
		}
	}
	for _, k := range sorted(sample) {
		p, exists := properties[k]
		if !exists {
			out = append(out, issue("UNKNOWN_PROPERTY", "/"+pointer(k), "不允许额外属性"))
			continue
		}
		def, _ := object(p)
		out = append(out, valueIssues(def, sample[k], "/"+pointer(k))...)
	}
	return out
}
func valueIssues(p map[string]any, value any, path string) []Issue {
	out := make([]Issue, 0)
	add := func(code, msg string) { out = append(out, issue(code, path, msg)) }
	kind, _ := p["type"].(string)
	switch kind {
	case "string":
		s, ok := value.(string)
		if !ok {
			add("TYPE", "必须为字符串")
			return out
		}
		n := float64(utf8.RuneCountInString(s))
		if n < bound(p, "minLength", 0) || n > bound(p, "maxLength", 1024) || n > 1024 {
			add("STRING_LENGTH", "字符串长度超出限制")
		}
	case "number", "integer":
		n, ok := number(value)
		if !ok {
			add("TYPE", "必须为数值")
			return out
		}
		if math.Abs(n) > float64(MaxInteger) || kind == "integer" && math.Trunc(n) != n {
			add("UNSAFE_NUMBER", "数值超出安全范围或不是整数")
		}
		if n < bound(p, "minimum", -float64(MaxInteger)) || n > bound(p, "maximum", float64(MaxInteger)) {
			add("NUMBER_RANGE", "数值超出允许范围")
		}
	case "boolean":
		if _, ok := value.(bool); !ok {
			add("TYPE", "必须为布尔值")
			return out
		}
	case "array":
		a, ok := array(value)
		if !ok {
			add("TYPE", "必须为数组")
			return out
		}
		if len(a) > 20 || float64(len(a)) < bound(p, "minItems", 0) || float64(len(a)) > bound(p, "maxItems", 20) {
			add("ARRAY_LENGTH", "数组长度超出限制")
		}
		item, _ := object(p["items"])
		for i, v := range a {
			if i >= 20 {
				break
			}
			out = append(out, valueIssues(item, v, fmt.Sprintf("%s/%d", path, i))...)
		}
	default:
		add("TYPE", "不支持的属性类型")
	}
	if values, ok := array(p["enum"]); ok {
		match := false
		h, e := Hash(value)
		if e == nil {
			for _, v := range values {
				other, _ := Hash(v)
				match = match || h == other
			}
		}
		if !match {
			add("ENUM", "值不在允许的枚举中")
		}
	}
	return out
}
