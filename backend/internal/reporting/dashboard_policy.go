package reporting

import (
	"bytes"
	"encoding/json"
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
	ids, orders := map[string]bool{}, map[int]bool{}
	for i := range out.Widgets {
		w := &out.Widgets[i]
		w.Title = strings.TrimSpace(w.Title)
		hasAnalysis := dashboardUUID.MatchString(w.AnalysisID) && w.AnalysisVersion >= 1 && w.AnalysisVersion <= analysis.MaxSafeInteger
		hasMetric := w.MetricRef != nil && dashboardUUID.MatchString(w.MetricRef.MetricID) && w.MetricRef.Version >= 1 && w.MetricRef.Version <= analysis.MaxSafeInteger
		// Exactly one immutable source is allowed; providing both or neither
		// is rejected before any layout or dependency processing.
		if hasAnalysis == hasMetric || !dashboardUUID.MatchString(w.ID) || ids[w.ID] || !dashboardText(w.Title, 128, false) {
			return out, analysis.Invalid("INVALID_DASHBOARD_WIDGET", "组件标识、标题或固定引用版本无效", "/widgets")
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
func dashboardText(s string, max int, empty bool) bool {
	return utf8.ValidString(s) && (empty || s != "") && utf8.RuneCountInString(s) <= max && !strings.ContainsRune(s, 0)
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
	if deps.ProjectID != d.ProjectID || deps.Environment != d.Environment || len(deps.Analyses) != len(definition.Widgets) {
		return out, analysis.Invalid("INVALID_DASHBOARD_DEPENDENCIES", "分析依赖与看板范围不匹配")
	}
	deps.Analyses = append([]DashboardDependency(nil), deps.Analyses...)
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
	for _, w := range widgets {
		counts[w.Query.State]++
	}
	if counts["queued"] == len(widgets) {
		return "queued"
	}
	if counts["queued"]+counts["running"] > 0 {
		return "running"
	}
	if counts["succeeded"] == len(widgets) {
		return "succeeded"
	}
	if counts["succeeded"] > 0 {
		return "partial"
	}
	if counts["expired"] == len(widgets) {
		return "expired"
	}
	if counts["canceled"] == len(widgets) {
		return "canceled"
	}
	return "failed"
}
