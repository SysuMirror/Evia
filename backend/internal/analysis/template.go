package analysis

import (
	"bytes"
	"encoding/json"
)

// Template contains the reusable, pinned configuration of an aggregate query.
// Runtime scope and time parameters are supplied only when Bind is called.
type Template struct {
	Kind          string         `json:"kind"`
	EventVersions []EventVersion `json:"event_versions,omitempty"`
	Filter        *Filter        `json:"filter,omitempty"`
	Subject       string         `json:"subject,omitempty"`
	GroupBy       []Field        `json:"group_by,omitempty"`
	Metrics       []Metric       `json:"metrics,omitempty"`
	Interval      string         `json:"interval,omitempty"`
	Funnel        *Funnel        `json:"funnel,omitempty"`
	Retention     *Retention     `json:"retention,omitempty"`
	Segment       *Segment       `json:"segment,omitempty"`
	Path          *Path          `json:"path,omitempty"`
	Attribution   *Attribution   `json:"attribution,omitempty"`
	Limit         int            `json:"limit,omitempty"`
}

// ValidateTemplate normalizes structural policy without inventing execution
// context. Published membership and property types still require the authorized
// catalog resolver. The caller's fields are replaced only after validation.
func ValidateTemplate(template *Template) error {
	if template == nil {
		return Invalid("INVALID_QUERY", "分析配置不能为空")
	}
	r, e := template.validatedRequest()
	if e != nil {
		return e
	}
	// Derived types are internal execution state, never saved configuration.
	if e = WalkFields(&r, func(field *Field) error { field.ResolvedType = ""; return nil }); e != nil {
		return e
	}
	*template = Template{Kind: r.Kind, EventVersions: r.EventVersions, Filter: r.Filter, Subject: r.Subject, GroupBy: r.GroupBy, Metrics: r.Metrics, Interval: r.Interval, Funnel: r.Funnel, Retention: r.Retention, Segment: r.Segment, Path: r.Path, Attribution: r.Attribution, Limit: r.Limit}
	return nil
}

// Bind returns an independent executable request using the supplied context.
// Catalog membership, property types and authorization remain execution checks.
func (template Template) Bind(environment string, timeRange TimeRange, timezone string) (Request, error) {
	if e := ValidateTemplate(&template); e != nil {
		return Request{}, e
	}
	r, e := template.request()
	if e != nil {
		return Request{}, e
	}
	r.Environment, r.TimeRange, r.Timezone = environment, timeRange, timezone
	if e = Validate(&r); e != nil {
		return Request{}, e
	}
	return r, nil
}

func (template Template) request() (Request, error) {
	data, e := json.Marshal(template)
	if e != nil {
		return Request{}, Invalid("INVALID_QUERY", "分析配置必须为有效的 JSON")
	}
	var r Request
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	if e = decoder.Decode(&r); e != nil {
		return Request{}, Invalid("INVALID_QUERY", "分析配置字段无效")
	}
	return r, nil
}

func (template Template) validatedRequest() (Request, error) {
	switch template.Kind {
	case QueryKindTrend:
		if len(template.EventVersions) == 0 {
			return Request{}, Invalid("INVALID_EVENT_SELECTION", "保存趋势分析必须固定事件版本", "/event_versions")
		}
	case QueryKindFunnel:
		if template.EventVersions != nil {
			return Request{}, Invalid("INVALID_EVENT_SELECTION", "保存漏斗分析通过步骤固定事件版本", "/event_versions")
		}
	case QueryKindRetention:
		if template.EventVersions != nil {
			return Request{}, Invalid("INVALID_EVENT_SELECTION", "保存留存分析通过起止事件固定事件版本", "/event_versions")
		}
	case QueryKindSegment, QueryKindPath, QueryKindAttribution:
		if template.EventVersions != nil {
			return Request{}, Invalid("INVALID_EVENT_SELECTION", "保存高级分析通过类型化步骤固定事件版本", "/event_versions")
		}
	default:
		return Request{}, Invalid("UNSUPPORTED_QUERY_KIND", "保存分析仅支持趋势、漏斗、留存、分群、路径或归因", "/kind")
	}
	r, e := template.request()
	if e != nil {
		return Request{}, e
	}
	if e = ValidateConfiguration(&r); e != nil {
		return Request{}, e
	}
	if r.Funnel != nil {
		selected := map[EventVersion]bool{}
		for _, step := range r.Funnel.Steps {
			selected[EventVersion{EventKey: step.EventKey, SchemaVersion: step.SchemaVersion}] = true
		}
		if e = WalkFields(&r, func(field *Field) error {
			if field.Kind == "property" && !selected[EventVersion{EventKey: field.EventKey, SchemaVersion: field.SchemaVersion}] {
				return Invalid("QUERY_FIELD_VERSION_NOT_SELECTED", "属性必须属于固定的漏斗步骤版本", "/funnel")
			}
			return nil
		}); e != nil {
			return Request{}, e
		}
		for _, entity := range r.Funnel.SameEntity {
			step := r.Funnel.Steps[entity.StepIndex]
			if entity.Field.Kind == "property" && (entity.Field.EventKey != step.EventKey || entity.Field.SchemaVersion != step.SchemaVersion) {
				return Request{}, Invalid("INVALID_SAME_ENTITY", "实体字段必须属于对应步骤事件及版本", "/funnel/same_entity")
			}
		}
	}
	return r, nil
}
