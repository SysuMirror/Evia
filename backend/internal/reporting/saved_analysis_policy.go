package reporting

import (
	"bytes"
	"encoding/json"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/howtomakeaname/Evia/backend/internal/analysis"
	"github.com/howtomakeaname/Evia/backend/internal/platform/canonical"
	"github.com/howtomakeaname/Evia/backend/internal/platform/contract"
	"github.com/howtomakeaname/Evia/backend/internal/platform/env"
	"github.com/howtomakeaname/Evia/backend/internal/platform/fault"
)

// ParseSavedAnalysisDefinition enforces the public schema before normalization.
// Decode retains numeric/boolean scalar identity and rejects duplicate keys.
func ParseSavedAnalysisDefinition(raw []byte, validator analysis.Validator) (SavedAnalysisDefinition, error) {
	var out SavedAnalysisDefinition
	if len(raw) > MaxSavedAnalysisDefinitionBytes {
		return out, analysis.Invalid("BODY_TOO_LARGE", "保存分析定义超过大小限制")
	}
	value, e := contract.Decode(raw)
	if e != nil {
		return out, analysis.Invalid("INVALID_SAVED_ANALYSIS", "保存分析必须是单一且无重复字段的 JSON")
	}
	if validator == nil {
		return out, fault.New(fault.Unavailable, "REPORTING_VALIDATOR_UNAVAILABLE", "保存分析验证器不可用")
	}
	if e = validator.Validate("SavedAnalysisDefinition", value); e != nil {
		return out, analysis.Invalid("INVALID_SAVED_ANALYSIS", "保存分析定义不符合接口约定")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	decoder.DisallowUnknownFields()
	if e = decoder.Decode(&out); e != nil {
		return out, analysis.Invalid("INVALID_SAVED_ANALYSIS", "保存分析定义字段无效")
	}
	return NormalizeSavedAnalysisDefinition(out)
}

// savedMetricIDPattern matches the canonical lowercase UUID used by ids.New.
var savedMetricIDPattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

func validateMetricReference(ref *MetricReference) error {
	if ref == nil {
		return analysis.Invalid("INVALID_METRIC_REFERENCE", "指标引用不能为空", "/metric_ref")
	}
	if !savedMetricIDPattern.MatchString(ref.MetricID) {
		return analysis.Invalid("INVALID_METRIC_REFERENCE", "指标引用的标识格式不正确", "/metric_ref/metric_id")
	}
	if ref.Version < 1 || ref.Version > analysis.MaxSafeInteger {
		return analysis.Invalid("INVALID_METRIC_REFERENCE", "指标引用版本无效", "/metric_ref/version")
	}
	// Validate the closed set but never write a default back: an omitted kind
	// stays empty in storage and JSON so historical atomic references remain
	// byte-identical; IsComposite treats both empty and "atomic" as atomic.
	if _, e := NormalizedMetricRefKind(ref.Kind); e != nil {
		return e
	}
	return nil
}

// decodeSavedDefinition round-trips the definition through JSON to obtain an
// independent canonical copy and enforce the persisted size boundary.
func decodeSavedDefinition(in SavedAnalysisDefinition) (SavedAnalysisDefinition, error) {
	data, e := json.Marshal(in)
	if e != nil {
		return SavedAnalysisDefinition{}, analysis.Invalid("INVALID_SAVED_ANALYSIS", "保存分析定义必须为有效 JSON")
	}
	if len(data) > MaxSavedAnalysisDefinitionBytes {
		return SavedAnalysisDefinition{}, analysis.Invalid("BODY_TOO_LARGE", "保存分析定义超过大小限制")
	}
	var out SavedAnalysisDefinition
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	decoder.DisallowUnknownFields()
	if e = decoder.Decode(&out); e != nil {
		return out, analysis.Invalid("INVALID_SAVED_ANALYSIS", "保存分析定义字段无效")
	}
	return out, nil
}

func validateSavedDefinitionMeta(in SavedAnalysisDefinition, out *SavedAnalysisDefinition) error {
	out.Title = strings.TrimSpace(out.Title)
	if !utf8.ValidString(in.Title) || out.Title == "" || utf8.RuneCountInString(out.Title) > 128 || strings.ContainsRune(out.Title, 0) {
		return analysis.Invalid("INVALID_TITLE", "标题必须为 1 到 128 个字符", "/title")
	}
	if !utf8.ValidString(in.Description) || utf8.RuneCountInString(out.Description) > 2000 || strings.ContainsRune(out.Description, 0) {
		return analysis.Invalid("INVALID_DESCRIPTION", "描述格式无效或超过长度限制", "/description")
	}
	return nil
}

// Visualization kind closed set, mirrored by the
// SavedAnalysisVisualization.kind OpenAPI enum and guarded by
// TestVisualizationVocabularyMatchesContract.
const (
	VisualizationTable  = "table"
	VisualizationMetric = "metric"
	VisualizationLine   = "line"
	VisualizationBar    = "bar"
	VisualizationFunnel = "funnel"
)

// VisualizationKinds is the single Go-side source of truth for the five
// visualization kinds.
var VisualizationKinds = []string{
	VisualizationTable, VisualizationMetric, VisualizationLine,
	VisualizationBar, VisualizationFunnel,
}

func validateInlineVisualization(out *SavedAnalysisDefinition) error {
	q := out.Query
	valid := false
	switch out.Visualization.Kind {
	case VisualizationTable:
		valid = true
	case VisualizationMetric:
		valid = q.Kind == analysis.QueryKindTrend && len(q.Metrics) == 1 && len(q.GroupBy) == 0 && q.Interval == ""
	case VisualizationLine:
		valid = q.Kind == analysis.QueryKindTrend && q.Interval != ""
	case VisualizationBar:
		valid = q.Kind == analysis.QueryKindTrend && (len(q.GroupBy) > 0 || q.Interval != "")
	case VisualizationFunnel:
		valid = q.Kind == analysis.QueryKindFunnel
	}
	if !valid {
		return analysis.Invalid("INVALID_VISUALIZATION", "图表类型与分析配置不兼容", "/visualization/kind")
	}
	return nil
}

func boundSavedDefinition(out SavedAnalysisDefinition) error {
	// Defaults can expand otherwise valid input. Bound the persisted canonical
	// configuration as well as the caller's representation, including callers
	// that invoke this helper directly from a storage transaction.
	normalized, e := json.Marshal(out)
	if e != nil {
		return analysis.Invalid("INVALID_SAVED_ANALYSIS", "保存分析定义必须为有效 JSON")
	}
	if len(normalized) > MaxSavedAnalysisDefinitionBytes {
		return analysis.Invalid("BODY_TOO_LARGE", "保存分析定义超过大小限制")
	}
	return nil
}

// NormalizeSavedAnalysisDefinition returns independent canonical configuration.
// It checks structural policy; catalog publication and field types are checked
// by the authorized transactional adapter, never by fabricated runtime context.
// A metric reference is accepted only without an inline query; the executable
// query is derived later by ResolveMetricReference inside a storage transaction.
func NormalizeSavedAnalysisDefinition(in SavedAnalysisDefinition) (SavedAnalysisDefinition, error) {
	out, e := decodeSavedDefinition(in)
	if e != nil {
		return out, e
	}
	if e = validateSavedDefinitionMeta(in, &out); e != nil {
		return out, e
	}
	if out.MetricRef == nil {
		if e = analysis.ValidateTemplate(&out.Query); e != nil {
			return out, e
		}
		sort.Slice(out.Query.EventVersions, func(i, j int) bool { return out.Query.EventVersions[i].EventKey < out.Query.EventVersions[j].EventKey })
		if e = validateInlineVisualization(&out); e != nil {
			return out, e
		}
	} else {
		if e = validateMetricReference(out.MetricRef); e != nil {
			return out, e
		}
		if out.Query.Kind != "" || len(out.Query.EventVersions) > 0 || out.Query.Funnel != nil {
			return out, analysis.Invalid("INVALID_METRIC_REFERENCE", "指标引用与内联查询不能同时提供", "/metric_ref")
		}
		// v1 renders a referenced metric as one whole-range metric card. Trend
		// intervals and grouping on top of a metric remain a future extension.
		if out.Visualization.Kind != VisualizationMetric {
			return out, analysis.Invalid("INVALID_VISUALIZATION", "指标引用分析当前仅支持指标卡片", "/visualization/kind")
		}
	}
	if e = boundSavedDefinition(out); e != nil {
		return out, e
	}
	return out, nil
}

// ResolveMetricReference derives the executable trend query of a metric-referencing
// saved analysis from an exact immutable metric version. The storage adapter must
// authorize and load that version in the same transaction and confirm project and
// environment ownership before calling this pure function. The derived definition
// retains the metric reference as provenance and is validated as a metric card.
func ResolveMetricReference(in SavedAnalysisDefinition, metric MetricVersion) (SavedAnalysisDefinition, error) {
	normalized, e := NormalizeSavedAnalysisDefinition(in)
	if e != nil {
		return SavedAnalysisDefinition{}, e
	}
	if normalized.MetricRef == nil {
		return SavedAnalysisDefinition{}, analysis.Invalid("INVALID_METRIC_REFERENCE", "定义没有指标引用", "/metric_ref")
	}
	ref := normalized.MetricRef
	if metric.ProjectID == "" || metric.MetricID != ref.MetricID || metric.Version != ref.Version {
		return SavedAnalysisDefinition{}, analysis.Invalid("METRIC_VERSION_NOT_FOUND", "指标版本与引用不匹配", "/metric_ref")
	}
	template, e := MetricTemplate(metric.Definition, MetricExecute{})
	if e != nil {
		return SavedAnalysisDefinition{}, e
	}
	resolved := normalized
	resolved.Query = template
	return normalizeResolvedMetricDefinition(resolved)
}

// normalizeResolvedMetricDefinition validates a definition that already carries
// both the metric reference and its derived query. It is unreachable from client
// parsing because NormalizeSavedAnalysisDefinition rejects the combination.
func normalizeResolvedMetricDefinition(in SavedAnalysisDefinition) (SavedAnalysisDefinition, error) {
	out, e := decodeSavedDefinition(in)
	if e != nil {
		return out, e
	}
	if e = validateSavedDefinitionMeta(in, &out); e != nil {
		return out, e
	}
	if out.MetricRef == nil {
		return out, analysis.Invalid("INVALID_METRIC_REFERENCE", "已解析定义缺少指标引用", "/metric_ref")
	}
	if e = validateMetricReference(out.MetricRef); e != nil {
		return out, e
	}
	if out.Query.Kind == "" {
		return out, analysis.Invalid("INVALID_METRIC_REFERENCE", "指标引用尚未解析为查询", "/metric_ref")
	}
	if e = analysis.ValidateTemplate(&out.Query); e != nil {
		return out, e
	}
	sort.Slice(out.Query.EventVersions, func(i, j int) bool { return out.Query.EventVersions[i].EventKey < out.Query.EventVersions[j].EventKey })
	if out.Visualization.Kind != VisualizationMetric {
		return out, analysis.Invalid("INVALID_VISUALIZATION", "指标引用分析当前仅支持指标卡片", "/visualization/kind")
	}
	q := out.Query
	if q.Kind != analysis.QueryKindTrend || len(q.Metrics) != 1 || len(q.GroupBy) != 0 || q.Interval != "" {
		return out, analysis.Invalid("INVALID_VISUALIZATION", "指标引用必须解析为单值指标卡片", "/visualization/kind")
	}
	if e = boundSavedDefinition(out); e != nil {
		return out, e
	}
	return out, nil
}

// ResolveCompositeMetricReference derives the stored definition for a saved
// analysis pinned to an immutable composite formula metric version. A formula
// DAG cannot be expressed as one atomic trend template, so the stored query is
// a trend marker shell; the run enqueue path attaches the internal formula_ref
// marker and the query worker expands the DAG at execution time. The storage
// adapter must authorize and load that formula version in the same transaction
// and confirm project and environment ownership before calling this function.
func ResolveCompositeMetricReference(in SavedAnalysisDefinition, formula FormulaMetricVersion) (SavedAnalysisDefinition, error) {
	normalized, e := NormalizeSavedAnalysisDefinition(in)
	if e != nil {
		return SavedAnalysisDefinition{}, e
	}
	if normalized.MetricRef == nil || !normalized.MetricRef.IsComposite() {
		return SavedAnalysisDefinition{}, analysis.Invalid("INVALID_METRIC_REFERENCE", "定义没有复合指标引用", "/metric_ref")
	}
	ref := normalized.MetricRef
	if formula.ProjectID == "" || formula.MetricID != ref.MetricID || formula.Version != ref.Version {
		return SavedAnalysisDefinition{}, analysis.Invalid("METRIC_VERSION_NOT_FOUND", "复合指标版本与引用不匹配", "/metric_ref")
	}
	resolved := normalized
	resolved.Query = analysis.Template{Kind: analysis.QueryKindTrend}
	return normalizeResolvedCompositeDefinition(resolved)
}

// normalizeResolvedCompositeDefinition validates a stored definition whose
// source is a composite formula metric. Its query is a marker shell rather than
// an independently executable atomic template, so it must not pass the atomic
// single-metric card checks; execution is driven solely by the formula marker.
func normalizeResolvedCompositeDefinition(in SavedAnalysisDefinition) (SavedAnalysisDefinition, error) {
	out, e := decodeSavedDefinition(in)
	if e != nil {
		return out, e
	}
	if e = validateSavedDefinitionMeta(in, &out); e != nil {
		return out, e
	}
	if out.MetricRef == nil || !out.MetricRef.IsComposite() {
		return out, analysis.Invalid("INVALID_METRIC_REFERENCE", "已解析定义缺少复合指标引用", "/metric_ref")
	}
	if e = validateMetricReference(out.MetricRef); e != nil {
		return out, e
	}
	if out.Query.Kind != analysis.QueryKindTrend {
		return out, analysis.Invalid("INVALID_METRIC_REFERENCE", "复合指标引用必须解析为趋势标记卡片", "/metric_ref")
	}
	if len(out.Query.EventVersions) != 0 || len(out.Query.Metrics) != 0 || out.Query.Interval != "" || len(out.Query.GroupBy) != 0 || out.Query.Filter != nil {
		return out, analysis.Invalid("INVALID_METRIC_REFERENCE", "复合指标标记卡片不能携带原子查询体", "/metric_ref")
	}
	if out.Visualization.Kind != VisualizationMetric {
		return out, analysis.Invalid("INVALID_VISUALIZATION", "指标引用分析当前仅支持指标卡片", "/visualization/kind")
	}
	if e = boundSavedDefinition(out); e != nil {
		return out, e
	}
	return out, nil
}

func savedHash(value any) (string, error) {
	hash, e := canonical.Hash(value)
	if e != nil {
		return "", analysis.Invalid("INVALID_SAVED_ANALYSIS", "保存分析定义包含不支持的 JSON 值")
	}
	return hash, nil
}
func savedEqual(a, b any) bool {
	x, e := canonical.JSON(a)
	if e != nil {
		return false
	}
	y, e := canonical.JSON(b)
	return e == nil && bytes.Equal(x, y)
}

// BuildSavedAnalysisValidation is pure and safe to recompute inside publication.
// Dependencies must describe exactly the pinned immutable configuration. Its
// digest is an acknowledgement of this content and baseline, never authority.
func BuildSavedAnalysisValidation(snapshot SavedAnalysisSnapshot) (SavedAnalysisValidation, error) {
	var out SavedAnalysisValidation
	draft := snapshot.Draft
	if draft.ID == "" || draft.ProjectID == "" || !savedEnvironment(draft.Environment) || draft.DraftRevision < 1 || draft.DraftRevision > analysis.MaxSafeInteger || draft.PublishedVersion < 0 || draft.PublishedVersion > analysis.MaxSafeInteger {
		return out, analysis.Invalid("INVALID_SAVED_ANALYSIS", "保存分析快照范围或修订无效")
	}
	definition, e := NormalizeStoredSavedDefinition(draft.Definition)
	if e != nil {
		return out, e
	}
	var baseline *SavedAnalysisDefinition
	if draft.PublishedVersion == 0 {
		if snapshot.Baseline != nil {
			return out, fault.New(fault.Conflict, "PUBLISHED_VERSION_CONFLICT", "发布基线已变化")
		}
	} else {
		version := snapshot.Baseline
		if version == nil || version.AnalysisID != draft.ID || version.ProjectID != draft.ProjectID || version.Environment != draft.Environment || version.Version != draft.PublishedVersion {
			return out, fault.New(fault.Conflict, "PUBLISHED_VERSION_CONFLICT", "发布基线已变化")
		}
		normalized, e := NormalizeStoredSavedDefinition(version.Definition)
		if e != nil {
			return out, e
		}
		baseline = &normalized
	}
	if e = validateMetricDependency(definition, snapshot.Dependencies); e != nil {
		return out, e
	}
	dependencies, e := savedDependencies(draft, definition.Query, snapshot.Dependencies)
	if e != nil {
		return out, e
	}
	// Carry the validated immutable metric pin; inline analyses always have nil.
	dependencies.Metric = snapshot.Dependencies.Metric
	definitionHash, e := savedHash(definition)
	if e != nil {
		return out, e
	}
	dependencyHash, e := savedHash(dependencies)
	if e != nil {
		return out, e
	}
	diff := savedAnalysisDiff(baseline, definition)
	out = SavedAnalysisValidation{Valid: true, DraftRevision: draft.DraftRevision, PublishedVersion: draft.PublishedVersion, NormalizedDefinition: definition, Dependencies: dependencies, DefinitionHash: definitionHash, DependencyHash: dependencyHash, Diff: diff}
	out.ValidationHash, e = savedHash(struct {
		AnalysisID       string                  `json:"analysis_id"`
		ProjectID        string                  `json:"project_id"`
		Environment      string                  `json:"environment"`
		DraftRevision    int64                   `json:"draft_revision"`
		PublishedVersion int64                   `json:"published_version"`
		Definition       SavedAnalysisDefinition `json:"definition"`
		Dependencies     DependencyManifest      `json:"dependencies"`
		Diff             []SavedAnalysisChange   `json:"diff"`
	}{draft.ID, draft.ProjectID, draft.Environment, draft.DraftRevision, draft.PublishedVersion, definition, dependencies, diff})
	return out, e
}

// NormalizeStoredSavedDefinition validates a definition read back from storage.
// Metric-referencing rows already carry their derived query, so they use the
// resolved normalizer; inline rows keep the strict public normalizer.
func NormalizeStoredSavedDefinition(in SavedAnalysisDefinition) (SavedAnalysisDefinition, error) {
	if in.MetricRef != nil {
		if in.MetricRef.IsComposite() {
			return normalizeResolvedCompositeDefinition(in)
		}
		return normalizeResolvedMetricDefinition(in)
	}
	return NormalizeSavedAnalysisDefinition(in)
}

// validateMetricDependency enforces that the manifest's metric pin, when
// required, exactly matches the referenced metric and that inline analyses
// never carry one. The storage adapter verifies the digest against the live
// immutable metric version; this checks internal manifest consistency.
func validateMetricDependency(definition SavedAnalysisDefinition, in DependencyManifest) error {
	if definition.MetricRef == nil {
		if in.Metric != nil {
			return analysis.Invalid("INVALID_ANALYSIS_DEPENDENCIES", "内联分析不能携带指标依赖")
		}
		return nil
	}
	if in.Metric == nil {
		return analysis.Invalid("INVALID_ANALYSIS_DEPENDENCIES", "指标引用分析缺少指标固定依赖")
	}
	ref := definition.MetricRef
	if in.Metric.MetricID != ref.MetricID || in.Metric.Version != ref.Version || !savedDigest(in.Metric.DefinitionHash) {
		return analysis.Invalid("INVALID_ANALYSIS_DEPENDENCIES", "指标依赖与引用版本不匹配")
	}
	return nil
}

func savedDependencies(draft SavedAnalysisDraft, template analysis.Template, in DependencyManifest) (DependencyManifest, error) {
	invalid := func() (DependencyManifest, error) {
		return DependencyManifest{}, analysis.Invalid("INVALID_ANALYSIS_DEPENDENCIES", "保存分析依赖与固定事件版本不匹配")
	}
	mode := SelectionModeEventVersions
	selected := map[analysis.EventVersion]bool{}
	for _, v := range template.EventVersions {
		selected[v] = true
	}
	if template.Kind == analysis.QueryKindFunnel {
		mode = SelectionModeFunnelSteps
		for _, step := range template.Funnel.Steps {
			selected[analysis.EventVersion{EventKey: step.EventKey, SchemaVersion: step.SchemaVersion}] = true
		}
	}
	if template.Kind == analysis.QueryKindRetention {
		mode = SelectionModeRetentionSteps
		for _, step := range []analysis.Step{template.Retention.StartEvent, template.Retention.ReturnEvent} {
			selected[analysis.EventVersion{EventKey: step.EventKey, SchemaVersion: step.SchemaVersion}] = true
		}
	}
	if template.Kind == analysis.QueryKindSegment {
		mode = SelectionModeSegmentSteps
		step := template.Segment.Event
		selected[analysis.EventVersion{EventKey: step.EventKey, SchemaVersion: step.SchemaVersion}] = true
	}
	if template.Kind == analysis.QueryKindPath {
		if template.Path.StartEvent != nil {
			mode = SelectionModePathSteps
			step := *template.Path.StartEvent
			selected[analysis.EventVersion{EventKey: step.EventKey, SchemaVersion: step.SchemaVersion}] = true
		} else {
			// No pinned start: the path reads every in-window event, so there
			// is no event-version dependency and the manifest stays all_events.
			mode = SelectionModeAllEvents
		}
	}
	if template.Kind == analysis.QueryKindAttribution {
		mode = SelectionModeAttributionSteps
		for _, step := range append([]analysis.Step{template.Attribution.Conversion}, template.Attribution.Touchpoints...) {
			selected[analysis.EventVersion{EventKey: step.EventKey, SchemaVersion: step.SchemaVersion}] = true
		}
	}
	if in.ProjectID != draft.ProjectID || in.Environment != draft.Environment || in.SelectionMode != mode || len(in.UnfixedEventKeys) > 0 || len(in.EventVersions) != len(selected) {
		return invalid()
	}
	out := DependencyManifest{ProjectID: in.ProjectID, Environment: in.Environment, SelectionMode: mode, UnfixedEventKeys: []string{}, EventVersions: make([]Dependency, 0, len(selected))}
	seen := map[analysis.EventVersion]bool{}
	for _, d := range in.EventVersions {
		key := analysis.EventVersion{EventKey: d.EventKey, SchemaVersion: d.SchemaVersion}
		if !selected[key] || seen[key] || !savedDigest(d.DefinitionHash) {
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
func savedAnalysisDiff(before *SavedAnalysisDefinition, after SavedAnalysisDefinition) []SavedAnalysisChange {
	if before == nil {
		return []SavedAnalysisChange{{Category: ChangeCategoryDefinition, Path: "/definition", After: after}}
	}
	changes := []SavedAnalysisChange{}
	add := func(category, path string, a, b any) {
		if !savedEqual(a, b) {
			changes = append(changes, SavedAnalysisChange{Category: category, Path: path, Before: a, After: b})
		}
	}
	add(ChangeCategoryMetadata, "/title", before.Title, after.Title)
	add(ChangeCategoryMetadata, "/description", before.Description, after.Description)
	add(ChangeCategoryKind, "/query/kind", before.Query.Kind, after.Query.Kind)
	add(ChangeCategoryEventVersions, "/query/event_versions", before.Query.EventVersions, after.Query.EventVersions)
	add(ChangeCategoryFilter, "/query/filter", before.Query.Filter, after.Query.Filter)
	add(ChangeCategorySubject, "/query/subject", before.Query.Subject, after.Query.Subject)
	add(ChangeCategoryMetrics, "/query/metrics", before.Query.Metrics, after.Query.Metrics)
	add(ChangeCategoryGroupBy, "/query/group_by", before.Query.GroupBy, after.Query.GroupBy)
	add(ChangeCategoryInterval, "/query/interval", before.Query.Interval, after.Query.Interval)
	add(ChangeCategoryFunnel, "/query/funnel", before.Query.Funnel, after.Query.Funnel)
	add(ChangeCategoryRetention, "/query/retention", before.Query.Retention, after.Query.Retention)
	add(ChangeCategorySegment, "/query/segment", before.Query.Segment, after.Query.Segment)
	add(ChangeCategoryPath, "/query/path", before.Query.Path, after.Query.Path)
	add(ChangeCategoryAttribution, "/query/attribution", before.Query.Attribution, after.Query.Attribution)
	add(ChangeCategoryLimit, "/query/limit", before.Query.Limit, after.Query.Limit)
	add(ChangeCategoryMetricRef, "/metric_ref", before.MetricRef, after.MetricRef)
	add(ChangeCategoryVisualization, "/visualization", before.Visualization, after.Visualization)
	return changes
}
func savedEnvironment(value string) bool {
	return env.Valid(value)
}
func savedDigest(value string) bool {
	return len(value) == 64 && strings.IndexFunc(value, func(r rune) bool { return r < '0' || r > '9' && r < 'a' || r > 'f' }) < 0
}
