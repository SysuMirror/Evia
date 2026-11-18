package reporting

import (
	"bytes"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/howtomakeaname/Evia/backend/internal/analysis"
	"github.com/howtomakeaname/Evia/backend/internal/platform/fault"
)

var dashboardUUID = regexp.MustCompile(`^[a-f0-9]{8}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{12}$`)

func NormalizeDashboardDefinition(in DashboardDefinition) (DashboardDefinition, error) {
	data, err := json.Marshal(in)
	if err != nil || len(data) > MaxDashboardDefinitionBytes {
		return DashboardDefinition{}, analysis.Invalid("INVALID_DASHBOARD", "看板定义无效或超过大小限制")
	}
	var out DashboardDefinition
	if err = json.Unmarshal(data, &out); err != nil {
		return out, err
	}
	out.Title = strings.TrimSpace(out.Title)
	if !dashboardText(out.Title, 128, false) || !dashboardText(out.Description, 2000, true) {
		return out, analysis.Invalid("INVALID_DASHBOARD", "看板标题或描述无效")
	}
	if len(out.Widgets) < 1 || len(out.Widgets) > MaxDashboardWidgets {
		return out, analysis.Invalid("INVALID_DASHBOARD_WIDGETS", "看板需要 1 到 12 个组件", "/widgets")
	}
	if e := normalizeDashboardParameters(&out); e != nil {
		return out, e
	}
	ids, orders := map[string]bool{}, map[int]bool{}
	for i := range out.Widgets {
		w := &out.Widgets[i]
		w.Title = strings.TrimSpace(w.Title)
		hasAnalysis := dashboardUUID.MatchString(w.AnalysisID) && w.AnalysisVersion >= 1 && w.AnalysisVersion <= analysis.MaxSafeInteger
		hasMetric := w.MetricRef != nil && dashboardUUID.MatchString(w.MetricRef.MetricID) && w.MetricRef.Version >= 1 && w.MetricRef.Version <= analysis.MaxSafeInteger
		hasTextCard := w.TextCard != nil
		// Exactly one source kind is allowed: analysis version, metric version
		// or a presentation-only text card. Providing none or several is
		// rejected before any layout or dependency processing.
		sourceKinds := 0
		if hasAnalysis {
			sourceKinds++
		}
		if hasMetric {
			sourceKinds++
		}
		if hasTextCard {
			sourceKinds++
		}
		if sourceKinds != 1 || !dashboardUUID.MatchString(w.ID) || ids[w.ID] || !dashboardText(w.Title, 128, false) {
			return out, analysis.Invalid("INVALID_DASHBOARD_WIDGET", "组件标识、标题或固定引用版本无效", "/widgets")
		}
		if hasMetric {
			// Dashboard metric widgets resolve only the atomic metric_versions
			// table; composite formula refs are a separate work item and must
			// fail closed here rather than reach the atomic resolver as no-row.
			kind, e := NormalizedMetricRefKind(w.MetricRef.Kind)
			if e != nil {
				return out, e
			}
			if kind == MetricRefKindComposite {
				return out, analysis.Invalid("INVALID_METRIC_REFERENCE", "看板组件暂不支持复合公式指标引用", "/widgets/metric_ref/kind")
			}
		}
		if hasTextCard {
			if e := ValidateTextCardMarkdown(w.TextCard.Content); e != nil {
				return out, e
			}
			if len(w.ParameterBindings) != 0 {
				return out, analysis.Invalid("INVALID_DASHBOARD_WIDGET", "文本卡组件不能绑定参数", "/widgets")
			}
		}
		ids[w.ID] = true
		if w.ReadingOrder < 0 || w.ReadingOrder >= len(out.Widgets) || orders[w.ReadingOrder] {
			return out, analysis.Invalid("INVALID_DASHBOARD_READING_ORDER", "阅读顺序必须唯一且从零连续排列", "/widgets")
		}
		orders[w.ReadingOrder] = true
		l := w.Layout
		if l.X < 0 || l.Y < 0 || l.Width < 1 || l.Height < 1 || l.X > 11 || l.Y > 999 || l.Width > 12 || l.Height > 24 || l.X+l.Width > 12 || l.Y+l.Height > 1000 {
			return out, analysis.Invalid("INVALID_DASHBOARD_LAYOUT", "组件必须位于 12 列有界网格内", "/widgets")
		}
		for j := 0; j < i; j++ {
			a := out.Widgets[j].Layout
			if l.X < a.X+a.Width && a.X < l.X+l.Width && l.Y < a.Y+a.Height && a.Y < l.Y+l.Height {
				return out, analysis.Invalid("DASHBOARD_LAYOUT_COLLISION", "组件布局不能重叠", "/widgets")
			}
		}
	}
	sort.Slice(out.Widgets, func(i, j int) bool { return out.Widgets[i].ReadingOrder < out.Widgets[j].ReadingOrder })
	return out, nil
}

// normalizeDashboardParameters validates the dashboard-level parameter
// contract and every widget binding while a definition is normalized. The
// closed set keeps the published shape sound; runtime value and required
// checks stay in the compiler, so an immutable version can never be enqueued
// with a bad predicate even if a later caller skips normalization.
func normalizeDashboardParameters(out *DashboardDefinition) error {
	if len(out.Parameters) > MaxParameterDeclarations {
		return analysis.Invalid("INVALID_PARAMETER", "参数数量超过上限", "/parameters")
	}
	declared := map[string]bool{}
	for i := range out.Parameters {
		decl := &out.Parameters[i]
		if e := ValidateDeclaration(*decl); e != nil {
			return e
		}
		if declared[decl.ParameterID] {
			return analysis.Invalid("INVALID_PARAMETER", "参数标识必须唯一", "/parameters")
		}
		declared[decl.ParameterID] = true
	}
	for i := range out.Widgets {
		w := &out.Widgets[i]
		seenParams := map[string]bool{}
		seenFields := map[string]bool{}
		for _, binding := range w.ParameterBindings {
			if !declared[binding.ParameterID] {
				return analysis.Invalid("INVALID_PARAMETER_BINDING", "组件绑定了未声明的参数", "/parameter_bindings")
			}
			if seenParams[binding.ParameterID] {
				return analysis.Invalid("INVALID_PARAMETER_BINDING", "同一参数在一个组件内只能绑定一次", "/parameter_bindings")
			}
			seenParams[binding.ParameterID] = true
			if e := validateBindingFieldShape(binding.Field); e != nil {
				return e
			}
			fieldKey := strings.Join([]string{
				binding.Field.Kind,
				binding.Field.Key,
				binding.Field.EventKey,
				fmt.Sprintf("%d", binding.Field.SchemaVersion),
			}, ":")
			if seenFields[fieldKey] {
				return analysis.Invalid("INVALID_PARAMETER_BINDING", "同一字段不能绑定多个参数", "/parameter_bindings")
			}
			seenFields[fieldKey] = true
		}
	}
	return nil
}

// validateBindingFieldShape performs structural validation without database
// access. Property existence on the pinned event version is enforced later
// through the ordinary filter validation path, so a stale binding can never
// produce a successful query.
func validateBindingFieldShape(field analysis.Field) error {
	switch field.Kind {
	case "system":
		if field.Key == "" || field.EventKey != "" || field.SchemaVersion != 0 {
			return analysis.Invalid("INVALID_PARAMETER_BINDING", "系统字段绑定形状无效", "/parameter_bindings")
		}
	case "property":
		if field.Key == "" || field.EventKey == "" || field.SchemaVersion < 1 || field.SchemaVersion > analysis.MaxSafeInteger {
			return analysis.Invalid("INVALID_PARAMETER_BINDING", "属性字段必须限定事件与已发布版本", "/parameter_bindings")
		}
	default:
		return analysis.Invalid("INVALID_PARAMETER_BINDING", "参数字段类型不受支持", "/parameter_bindings")
	}
	return nil
}

func dashboardText(s string, max int, empty bool) bool {
	return utf8.ValidString(s) && (empty || s != "") && utf8.RuneCountInString(s) <= max && !strings.ContainsRune(s, 0)
}

// CloneDashboardDefinition builds an independent copy definition: the title is
// replaced and every widget receives a fresh ID, while pinned sources, layout,
// reading order, parameter declarations and bindings are retained. The caller
// supplies the new widget IDs in widget order, keeping the domain free of ID
// plumbing. The result is re-normalized so the copy passes every current rule.
func CloneDashboardDefinition(source DashboardDefinition, title string, widgetIDs []string) (DashboardDefinition, error) {
	if len(widgetIDs) != len(source.Widgets) {
		return DashboardDefinition{}, analysis.Invalid("INVALID_DASHBOARD", "复制组件标识数量不匹配")
	}
	out := source
	out.Title = title
	out.Parameters = append([]ParameterDeclaration(nil), source.Parameters...)
	out.Widgets = make([]DashboardWidget, len(source.Widgets))
	for i, widget := range source.Widgets {
		widget.ID = widgetIDs[i]
		widget.ParameterBindings = append([]ParameterBinding(nil), widget.ParameterBindings...)
		if widget.TextCard != nil {
			textCard := *widget.TextCard
			widget.TextCard = &textCard
		}
		out.Widgets[i] = widget
	}
	return NormalizeDashboardDefinition(out)
}

// RebindDashboardDefinition builds a cross-environment copy definition. Every
// executable widget must have exactly one rebinding whose kind matches the
// source (analysis stays analysis, metric stays metric); presentation-only text
// cards carry over untouched. The store independently verifies that each target
// reference exists, is published and belongs to the target environment, so this
// function enforces only structural coverage and shape. Parameter declarations
// and bindings are retained structurally; target-side publish validation rejects
// any binding whose field does not resolve on the rebound sources.
func RebindDashboardDefinition(
	source DashboardDefinition,
	title string,
	widgetIDs []string,
	rebindings []DashboardRebinding,
) (DashboardDefinition, error) {
	if len(widgetIDs) != len(source.Widgets) {
		return DashboardDefinition{}, analysis.Invalid("INVALID_DASHBOARD", "复制组件标识数量不匹配")
	}
	byWidget := map[string]DashboardRebinding{}
	for _, rebinding := range rebindings {
		if !dashboardUUID.MatchString(rebinding.WidgetID) {
			return DashboardDefinition{}, analysis.Invalid("INVALID_DASHBOARD_REBINDING", "重绑定组件标识无效", "/rebindings")
		}
		if _, exists := byWidget[rebinding.WidgetID]; exists {
			return DashboardDefinition{}, analysis.Invalid("INVALID_DASHBOARD_REBINDING", "同一组件不能重复重绑定", "/rebindings")
		}
		hasAnalysis := dashboardUUID.MatchString(rebinding.AnalysisID) &&
			rebinding.AnalysisVersion >= 1 && rebinding.AnalysisVersion <= analysis.MaxSafeInteger
		hasMetric := dashboardUUID.MatchString(rebinding.MetricID) &&
			rebinding.MetricVersion >= 1 && rebinding.MetricVersion <= analysis.MaxSafeInteger
		if hasAnalysis == hasMetric {
			return DashboardDefinition{}, analysis.Invalid("INVALID_DASHBOARD_REBINDING", "重绑定必须且只能指定分析或指标中的一种来源", "/rebindings")
		}
		byWidget[rebinding.WidgetID] = rebinding
	}
	out := source
	out.Title = title
	out.Parameters = append([]ParameterDeclaration(nil), source.Parameters...)
	out.Widgets = make([]DashboardWidget, len(source.Widgets))
	executable := 0
	for i, widget := range source.Widgets {
		sourceID := widget.ID
		widget.ID = widgetIDs[i]
		widget.ParameterBindings = append([]ParameterBinding(nil), widget.ParameterBindings...)
		if widget.TextCard != nil {
			textCard := *widget.TextCard
			widget.TextCard = &textCard
		} else {
			executable++
			rebinding, ok := byWidget[sourceID]
			if !ok {
				return DashboardDefinition{}, analysis.Invalid("INVALID_DASHBOARD_REBINDING", "每个可执行组件都必须提供目标环境重绑定", "/rebindings")
			}
			if widget.MetricRef != nil {
				if rebinding.MetricID == "" {
					return DashboardDefinition{}, analysis.Invalid("INVALID_DASHBOARD_REBINDING", "指标组件只能重绑定到目标环境的指标版本", "/rebindings")
				}
				widget.MetricRef = &MetricReference{MetricID: rebinding.MetricID, Version: rebinding.MetricVersion}
				widget.AnalysisID, widget.AnalysisVersion = "", 0
			} else {
				if rebinding.AnalysisID == "" {
					return DashboardDefinition{}, analysis.Invalid("INVALID_DASHBOARD_REBINDING", "分析组件只能重绑定到目标环境的保存分析版本", "/rebindings")
				}
				widget.AnalysisID, widget.AnalysisVersion = rebinding.AnalysisID, rebinding.AnalysisVersion
				widget.MetricRef = nil
			}
		}
		out.Widgets[i] = widget
	}
	// Coverage must be exact: this rejects both missing entries (handled above)
	// and entries for unknown or text-card widgets.
	if len(byWidget) != executable {
		return DashboardDefinition{}, analysis.Invalid("INVALID_DASHBOARD_REBINDING", "重绑定必须且只能覆盖全部可执行组件", "/rebindings")
	}
	return NormalizeDashboardDefinition(out)
}

func BuildDashboardValidation(snapshot DashboardSnapshot) (DashboardValidation, error) {
	var out DashboardValidation
	d := snapshot.Draft
	if d.ID == "" || d.ProjectID == "" || !savedEnvironment(d.Environment) || savedCounters(d.DraftRevision, d.PublishedVersion) != nil {
		return out, analysis.Invalid("INVALID_DASHBOARD", "看板快照范围无效")
	}
	definition, err := NormalizeDashboardDefinition(d.Definition)
	if err != nil {
		return out, err
	}
	deps := snapshot.Dependencies
	executableWidgets := 0
	for _, widget := range definition.Widgets {
		if widget.TextCard == nil {
			executableWidgets++
		}
	}
	if deps.ProjectID != d.ProjectID || deps.Environment != d.Environment || len(deps.Analyses) != executableWidgets {
		return out, analysis.Invalid("INVALID_DASHBOARD_DEPENDENCIES", "分析依赖与看板范围不匹配")
	}
	// A non-nil seed keeps the manifest shape stable for presentation-only
	// boards: a text-only dashboard legitimately has zero witnesses and must
	// still serialize analyses as [], never null (the DB trigger requires an
	// array and the response contract marks analyses non-nullable).
	deps.Analyses = append([]DashboardDependency{}, deps.Analyses...)
	seen := map[string]bool{}
	for _, dep := range deps.Analyses {
		found := false
		for _, w := range definition.Widgets {
			if w.ID != dep.WidgetID {
				continue
			}
			analysisMatch := w.AnalysisID == dep.AnalysisID && w.AnalysisVersion == dep.AnalysisVersion && w.MetricRef == nil && dep.MetricID == ""
			metricMatch := w.MetricRef != nil && w.MetricRef.MetricID == dep.MetricID && w.MetricRef.Version == dep.MetricVersion && w.AnalysisID == "" && dep.AnalysisID == ""
			if analysisMatch || metricMatch {
				found = true
				break
			}
		}
		if !found || seen[dep.WidgetID] || !savedDigest(dep.DefinitionHash) || !savedDigest(dep.DependencyHash) || !savedDigest(dep.ValidationHash) {
			return out, analysis.Invalid("INVALID_DASHBOARD_DEPENDENCIES", "组件分析依赖无效")
		}
		seen[dep.WidgetID] = true
	}
	sort.Slice(deps.Analyses, func(i, j int) bool { return deps.Analyses[i].WidgetID < deps.Analyses[j].WidgetID })
	diff := []SavedAnalysisChange{}
	if d.PublishedVersion == 0 {
		if snapshot.Baseline != nil {
			return out, fault.New(fault.Conflict, "PUBLISHED_VERSION_CONFLICT", "发布基线已变化")
		}
		diff = append(diff, SavedAnalysisChange{Category: "definition", Path: "/definition", After: definition})
	} else {
		b := snapshot.Baseline
		if b == nil || b.DashboardID != d.ID || b.ProjectID != d.ProjectID || b.Environment != d.Environment || b.Version != d.PublishedVersion {
			return out, fault.New(fault.Conflict, "PUBLISHED_VERSION_CONFLICT", "发布基线已变化")
		}
		baseline, e := NormalizeDashboardDefinition(b.Definition)
		if e != nil {
			return out, e
		}
		add := func(category, path string, before, after any) {
			if !savedEqual(before, after) {
				diff = append(diff, SavedAnalysisChange{Category: category, Path: path, Before: before, After: after})
			}
		}
		add("metadata", "/title", baseline.Title, definition.Title)
		add("metadata", "/description", baseline.Description, definition.Description)
		add("parameters", "/parameters", baseline.Parameters, definition.Parameters)
		old := map[string]DashboardWidget{}
		for _, w := range baseline.Widgets {
			old[w.ID] = w
		}
		for _, w := range definition.Widgets {
			prev, exists := old[w.ID]
			if !exists {
				add("widgets", "/widgets/"+w.ID, nil, w)
				continue
			}
			add("metadata", "/widgets/"+w.ID+"/title", prev.Title, w.Title)
			add("dependencies", "/widgets/"+w.ID+"/analysis", []any{prev.AnalysisID, prev.AnalysisVersion}, []any{w.AnalysisID, w.AnalysisVersion})
			add("layout", "/widgets/"+w.ID+"/layout", prev.Layout, w.Layout)
			add("layout", "/widgets/"+w.ID+"/reading_order", prev.ReadingOrder, w.ReadingOrder)
			add("parameters", "/widgets/"+w.ID+"/parameter_bindings", prev.ParameterBindings, w.ParameterBindings)
			delete(old, w.ID)
		}
		removed := []string{}
		for id := range old {
			removed = append(removed, id)
		}
		sort.Strings(removed)
		for _, id := range removed {
			add("widgets", "/widgets/"+id, old[id], nil)
		}
	}
	out = DashboardValidation{Valid: true, DraftRevision: d.DraftRevision, PublishedVersion: d.PublishedVersion, NormalizedDefinition: definition, Dependencies: deps, Diff: diff}
	if out.DefinitionHash, err = savedHash(definition); err != nil {
		return out, err
	}
	if out.DependencyHash, err = savedHash(deps); err != nil {
		return out, err
	}
	out.ValidationHash, err = savedHash(struct {
		DashboardID, ProjectID, Environment string
		DraftRevision, PublishedVersion     int64
		Definition                          DashboardDefinition
		Dependencies                        DashboardDependencies
		Diff                                []SavedAnalysisChange
	}{d.ID, d.ProjectID, d.Environment, d.DraftRevision, d.PublishedVersion, definition, deps, diff})
	return out, err
}

// NormalizeDashboardExecution clones execution input and binds each runtime
// filter to an explicit widget. Field types and selected event versions are
// checked when binding each immutable analysis and resolving it transactionally.
func NormalizeDashboardExecution(in DashboardExecute, definition DashboardDefinition) (DashboardExecute, error) {
	data, err := json.Marshal(in)
	if err != nil || len(data) > MaxDashboardDefinitionBytes {
		return DashboardExecute{}, analysis.Invalid("INVALID_DASHBOARD_EXECUTION", "看板运行参数无效或过大")
	}
	var out DashboardExecute
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	if err = decoder.Decode(&out); err != nil {
		return out, err
	}
	out.TimeRange.From = out.TimeRange.From.UTC()
	out.TimeRange.To = out.TimeRange.To.UTC()
	if len(out.WidgetFilters) > len(definition.Widgets) {
		return out, analysis.Invalid("INVALID_WIDGET_FILTER", "组件筛选数量超过组件数量", "/widget_filters")
	}
	if out.Comparison != nil && !out.Comparison.Valid() {
		return out, analysis.Invalid("INVALID_COMPARISON_SPEC", "比较期单位或数量无效", "/comparison")
	}
	if e := ValidateParameterValues(definition.Parameters, out.ParameterValues); e != nil {
		return out, e
	}
	ids := map[string]bool{}
	for _, w := range definition.Widgets {
		ids[w.ID] = true
	}
	seen := map[string]bool{}
	for _, f := range out.WidgetFilters {
		if !ids[f.WidgetID] || seen[f.WidgetID] || f.Filter == nil {
			return out, analysis.Invalid("INVALID_WIDGET_FILTER", "筛选必须引用唯一且存在的组件", "/widget_filters")
		}
		seen[f.WidgetID] = true
	}
	sort.Slice(out.WidgetFilters, func(i, j int) bool { return out.WidgetFilters[i].WidgetID < out.WidgetFilters[j].WidgetID })
	return out, nil
}

// BindDashboardWidget preserves every fixed predicate. Combined filter depth,
// node and field/version budgets are validated together rather than separately.
func BindDashboardWidget(version SavedAnalysisVersion, filter *analysis.Filter, in DashboardExecute) (analysis.Request, error) {
	template := version.Definition.Query
	if filter != nil {
		if template.Filter != nil {
			template.Filter = &analysis.Filter{Op: "and", Args: []*analysis.Filter{template.Filter, filter}}
		} else {
			template.Filter = filter
		}
	}
	return template.Bind(version.Environment, in.TimeRange, in.Timezone)
}

func DashboardRunState(widgets []DashboardWidgetRun) string {
	if len(widgets) == 0 {
		return "expired"
	}
	counts := map[string]int{}
	total := 0
	for _, w := range widgets {
		counts[w.Query.State]++
		total++
		// Comparison jobs are independent work units: a failed comparison makes
		// the run partial/failed even when the current window succeeded.
		if w.Comparison != nil {
			counts[w.Comparison.State]++
			total++
		}
	}
	if counts["queued"] == total {
		return "queued"
	}
	if counts["queued"]+counts["running"] > 0 {
		return "running"
	}
	if counts["succeeded"] == total {
		return "succeeded"
	}
	if counts["succeeded"] > 0 {
		return "partial"
	}
	if counts["expired"] == total {
		return "expired"
	}
	if counts["canceled"] == total {
		return "canceled"
	}
	return "failed"
}
