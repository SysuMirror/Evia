package reporting

import (
	"bytes"
	"encoding/json"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/howtomakeaname/Evia/backend/internal/analysis"
	"github.com/howtomakeaname/Evia/backend/internal/platform/canonical"
	"github.com/howtomakeaname/Evia/backend/internal/platform/contract"
	"github.com/howtomakeaname/Evia/backend/internal/platform/fault"
)

// ParseMetricDefinition enforces the public schema before normalization.
// Decode retains numeric/boolean scalar identity and rejects duplicate keys.
func ParseMetricDefinition(raw []byte, validator analysis.Validator) (MetricDefinition, error) {
	var out MetricDefinition
	if len(raw) > MaxMetricDefinitionBytes {
		return out, analysis.Invalid("BODY_TOO_LARGE", "指标定义超过大小限制")
	}
	value, e := contract.Decode(raw)
	if e != nil {
		return out, analysis.Invalid("INVALID_METRIC", "指标必须是单一且无重复字段的 JSON")
	}
	if validator == nil {
		return out, fault.New(fault.Unavailable, "REPORTING_VALIDATOR_UNAVAILABLE", "指标验证器不可用")
	}
	if e = validator.Validate("MetricDefinition", value); e != nil {
		return out, analysis.Invalid("INVALID_METRIC", "指标定义不符合接口约定")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	decoder.DisallowUnknownFields()
	if e = decoder.Decode(&out); e != nil {
		return out, analysis.Invalid("INVALID_METRIC", "指标定义字段无效")
	}
	return NormalizeMetricDefinition(out)
}

// NormalizeMetricDefinition returns independent canonical configuration.
// It checks structural policy; catalog publication and field types are checked
// by the authorized transactional adapter, never by fabricated runtime context.
func NormalizeMetricDefinition(in MetricDefinition) (MetricDefinition, error) {
	data, e := json.Marshal(in)
	if e != nil {
		return MetricDefinition{}, analysis.Invalid("INVALID_METRIC", "指标定义必须为有效 JSON")
	}
	if len(data) > MaxMetricDefinitionBytes {
		return MetricDefinition{}, analysis.Invalid("BODY_TOO_LARGE", "指标定义超过大小限制")
	}
	var out MetricDefinition
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	decoder.DisallowUnknownFields()
	if e = decoder.Decode(&out); e != nil {
		return out, analysis.Invalid("INVALID_METRIC", "指标定义字段无效")
	}
	out.Title = strings.TrimSpace(out.Title)
	if !utf8.ValidString(in.Title) || out.Title == "" || utf8.RuneCountInString(out.Title) > 128 || strings.ContainsRune(out.Title, 0) {
		return out, analysis.Invalid("INVALID_TITLE", "标题必须为 1 到 128 个字符", "/title")
	}
	if !utf8.ValidString(in.Description) || utf8.RuneCountInString(out.Description) > 2000 || strings.ContainsRune(out.Description, 0) {
		return out, analysis.Invalid("INVALID_DESCRIPTION", "描述格式无效或超过长度限制", "/description")
	}
	if out.Subject != "uid" && out.Subject != "anonymous" {
		return out, analysis.Invalid("INVALID_SUBJECT", "指标必须选择身份主体", "/subject")
	}
	// Strip execution-only state on the owned copy before structural checks.
	fields := analysis.Request{Filter: out.Filter, Metrics: []analysis.Metric{out.Aggregation}}
	if e = analysis.WalkFields(&fields, func(field *analysis.Field) error { field.ResolvedType = ""; return nil }); e != nil {
		return out, e
	}
	template := analysis.Template{Kind: "trend", EventVersions: out.EventVersions, Filter: fields.Filter, Subject: out.Subject, Metrics: fields.Metrics}
	if e = analysis.ValidateTemplate(&template); e != nil {
		return out, e
	}
	out.EventVersions, out.Filter, out.Aggregation = template.EventVersions, template.Filter, template.Metrics[0]
	sort.Slice(out.EventVersions, func(i, j int) bool {
		a, b := out.EventVersions[i], out.EventVersions[j]
		return a.EventKey < b.EventKey || a.EventKey == b.EventKey && a.SchemaVersion < b.SchemaVersion
	})
	validUnit := false
	switch out.Aggregation.Op {
	case "count":
		validUnit = out.Unit == "event"
	case "unique":
		if out.Aggregation.Field != nil {
			validUnit = out.Unit == "distinct_value"
		} else if out.Subject == "uid" {
			validUnit = out.Unit == "raw_uid"
		} else {
			validUnit = out.Unit == "anonymous_id"
		}
	default:
		validUnit = out.Unit == "number" || out.Unit == "millisecond" || out.Unit == "second" || out.Unit == "byte"
	}
	if !validUnit {
		return out, analysis.Invalid("INVALID_METRIC_UNIT", "单位与指标聚合或身份主体不兼容", "/unit")
	}
	// Defaults can expand otherwise valid input. Bound the persisted canonical
	// configuration as well as the caller's representation, including callers
	// that invoke this helper directly from a storage transaction.
	normalized, e := json.Marshal(out)
	if e != nil {
		return MetricDefinition{}, analysis.Invalid("INVALID_METRIC", "指标定义必须为有效 JSON")
	}
	if len(normalized) > MaxMetricDefinitionBytes {
		return MetricDefinition{}, analysis.Invalid("BODY_TOO_LARGE", "指标定义超过大小限制")
	}
	return out, nil
}

// BuildMetricValidation is pure and safe to recompute inside publication.
// Dependencies must describe exactly the pinned immutable configuration. Its
// digest is an acknowledgement of this content and baseline, never authority.
func BuildMetricValidation(snapshot MetricSnapshot) (MetricValidation, error) {
	var out MetricValidation
	draft := snapshot.Draft
	if draft.ID == "" || draft.ProjectID == "" || !metricEnvironment(draft.Environment) || draft.DraftRevision < 1 || draft.DraftRevision > analysis.MaxSafeInteger || draft.PublishedVersion < 0 || draft.PublishedVersion > analysis.MaxSafeInteger {
		return out, analysis.Invalid("INVALID_METRIC", "指标快照范围或修订无效")
	}
	definition, e := NormalizeMetricDefinition(draft.Definition)
	if e != nil {
		return out, e
	}
	var baseline *MetricDefinition
	if draft.PublishedVersion == 0 {
		if snapshot.Baseline != nil {
			return out, fault.New(fault.Conflict, "PUBLISHED_VERSION_CONFLICT", "发布基线已变化")
		}
	} else {
		version := snapshot.Baseline
		if version == nil || version.MetricID != draft.ID || version.ProjectID != draft.ProjectID || version.Environment != draft.Environment || version.Version != draft.PublishedVersion {
			return out, fault.New(fault.Conflict, "PUBLISHED_VERSION_CONFLICT", "发布基线已变化")
		}
		normalized, e := NormalizeMetricDefinition(version.Definition)
		if e != nil {
			return out, e
		}
		baseline = &normalized
	}
	dependencies, e := metricDependencies(draft, definition, snapshot.Dependencies)
	if e != nil {
		return out, e
	}
	definitionHash, e := metricHash(definition)
	if e != nil {
		return out, e
	}
	dependencyHash, e := metricHash(dependencies)
	if e != nil {
		return out, e
	}
	diff := metricDiff(baseline, definition)
	out = MetricValidation{Valid: true, DraftRevision: draft.DraftRevision, PublishedVersion: draft.PublishedVersion, NormalizedDefinition: definition, Dependencies: dependencies, DefinitionHash: definitionHash, DependencyHash: dependencyHash, Diff: diff}
	out.ValidationHash, e = metricHash(struct {
		MetricID         string             `json:"metric_id"`
		ProjectID        string             `json:"project_id"`
		Environment      string             `json:"environment"`
		DraftRevision    int64              `json:"draft_revision"`
		PublishedVersion int64              `json:"published_version"`
		DefinitionHash   string             `json:"definition_hash"`
		DependencyHash   string             `json:"dependency_hash"`
		Definition       MetricDefinition   `json:"definition"`
		Dependencies     DependencyManifest `json:"dependencies"`
		Diff             []MetricChange     `json:"diff"`
	}{draft.ID, draft.ProjectID, draft.Environment, draft.DraftRevision, draft.PublishedVersion, definitionHash, dependencyHash, definition, dependencies, diff})
	return out, e
}

func metricDependencies(draft MetricDraft, definition MetricDefinition, in DependencyManifest) (DependencyManifest, error) {
	invalid := func() (DependencyManifest, error) {
		return DependencyManifest{}, analysis.Invalid("INVALID_ANALYSIS_DEPENDENCIES", "指标依赖与固定事件版本不匹配")
	}
	mode := SelectionModeEventVersions
	selected := map[analysis.EventVersion]bool{}
	for _, v := range definition.EventVersions {
		selected[v] = true
	}
	if in.ProjectID != draft.ProjectID || in.Environment != draft.Environment || in.SelectionMode != mode || len(in.UnfixedEventKeys) > 0 || len(in.EventVersions) != len(selected) {
		return invalid()
	}
	out := DependencyManifest{ProjectID: in.ProjectID, Environment: in.Environment, SelectionMode: mode, UnfixedEventKeys: []string{}, EventVersions: make([]Dependency, 0, len(selected))}
	seen := map[analysis.EventVersion]bool{}
	for _, d := range in.EventVersions {
		key := analysis.EventVersion{EventKey: d.EventKey, SchemaVersion: d.SchemaVersion}
		if !selected[key] || seen[key] || !metricDigest(d.DefinitionHash) {
			return invalid()
		}
		seen[key] = true
		out.EventVersions = append(out.EventVersions, d)
	}
	sort.Slice(out.EventVersions, func(i, j int) bool {
		a, b := out.EventVersions[i], out.EventVersions[j]
		return a.EventKey < b.EventKey || a.EventKey == b.EventKey && a.SchemaVersion < b.SchemaVersion
	})
	return out, nil
}
func metricDiff(before *MetricDefinition, after MetricDefinition) []MetricChange {
	if before == nil {
		return []MetricChange{{Category: ChangeCategoryDefinition, Path: "/definition", After: after}}
	}
	changes := []MetricChange{}
	add := func(category, path string, a, b any) {
		if !metricEqual(a, b) {
			changes = append(changes, MetricChange{Category: category, Path: path, Before: a, After: b})
		}
	}
	add(ChangeCategoryMetadata, "/title", before.Title, after.Title)
	add(ChangeCategoryMetadata, "/description", before.Description, after.Description)
	add(ChangeCategoryEventVersions, "/event_versions", before.EventVersions, after.EventVersions)
	add(ChangeCategoryFilter, "/filter", before.Filter, after.Filter)
	add(ChangeCategorySubject, "/subject", before.Subject, after.Subject)
	add(ChangeCategoryAggregation, "/aggregation", before.Aggregation, after.Aggregation)
	add(ChangeCategoryUnit, "/unit", before.Unit, after.Unit)
	return changes
}

// MetricTemplate builds an independent runtime trend configuration. Both the
// fixed and caller filters must hold; unit annotations never change values.
func MetricTemplate(definition MetricDefinition, execute MetricExecute) (analysis.Template, error) {
	normalized, err := NormalizeMetricDefinition(definition)
	if err != nil {
		return analysis.Template{}, err
	}
	if execute.Interval != "" && execute.Interval != "minute" && execute.Interval != "hour" && execute.Interval != "day" {
		return analysis.Template{}, analysis.Invalid("INVALID_INTERVAL", "请选择时间粒度", "/interval")
	}
	filter := normalized.Filter
	if execute.Filter != nil {
		if filter == nil {
			filter = execute.Filter
		} else {
			filter = &analysis.Filter{Op: "and", Args: []*analysis.Filter{filter, execute.Filter}}
		}
	}
	template := analysis.Template{Kind: "trend", EventVersions: normalized.EventVersions, Filter: filter, Subject: normalized.Subject, Metrics: []analysis.Metric{normalized.Aggregation}, Interval: execute.Interval, GroupBy: execute.GroupBy, Limit: execute.Limit}
	if err = analysis.ValidateTemplate(&template); err != nil {
		return analysis.Template{}, err
	}
	return template, nil
}

func metricEnvironment(value string) bool {
	return value == "development" || value == "staging" || value == "production"
}
func metricDigest(value string) bool {
	return len(value) == 64 && strings.IndexFunc(value, func(r rune) bool { return r < '0' || r > '9' && r < 'a' || r > 'f' }) < 0
}
func metricHash(value any) (string, error) {
	hash, err := canonical.Hash(value)
	if err != nil {
		return "", analysis.Invalid("INVALID_METRIC", "指标定义包含不支持的 JSON 值")
	}
	return hash, nil
}
func metricEqual(a, b any) bool {
	x, err := canonical.JSON(a)
	if err != nil {
		return false
	}
	y, err := canonical.JSON(b)
	return err == nil && bytes.Equal(x, y)
}
