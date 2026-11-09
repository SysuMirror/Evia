package reporting

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/howtomakeaname/Evia/backend/internal/analysis"
	"github.com/howtomakeaname/Evia/backend/internal/platform/contract"
)

const dashboardWidgetOne = "11111111-1111-4111-8111-111111111111"
const dashboardWidgetTwo = "22222222-2222-4222-8222-222222222222"
const dashboardAnalysisID = "33333333-3333-4333-8333-333333333333"

func dashboardDefinition() DashboardDefinition {
	return DashboardDefinition{Title: "  Overview  ", Widgets: []DashboardWidget{{ID: dashboardWidgetOne, AnalysisID: dashboardAnalysisID, AnalysisVersion: 1, Title: "Visits", Layout: DashboardLayout{Width: 6, Height: 3}}, {ID: dashboardWidgetTwo, AnalysisID: dashboardAnalysisID, AnalysisVersion: 1, Title: "Readers", ReadingOrder: 1, Layout: DashboardLayout{X: 6, Width: 6, Height: 3}}}}
}
func dashboardSnapshot(t *testing.T) DashboardSnapshot {
	t.Helper()
	def, e := NormalizeDashboardDefinition(dashboardDefinition())
	if e != nil {
		t.Fatal(e)
	}
	d := DashboardDraft{Dashboard: Dashboard{ID: "dashboard-one", ProjectID: "project-one", Environment: "development", DraftRevision: 1}, Definition: def}
	deps := DashboardDependencies{ProjectID: d.ProjectID, Environment: d.Environment, Analyses: []DashboardDependency{}}
	for _, w := range def.Widgets {
		deps.Analyses = append(deps.Analyses, DashboardDependency{WidgetID: w.ID, AnalysisID: w.AnalysisID, AnalysisVersion: w.AnalysisVersion, DefinitionHash: strings.Repeat("a", 64), DependencyHash: strings.Repeat("b", 64), ValidationHash: strings.Repeat("c", 64)})
	}
	return DashboardSnapshot{Draft: d, Dependencies: deps}
}
func TestDashboardLayoutAndStableReadingOrder(t *testing.T) {
	d := dashboardDefinition()
	d.Widgets[0], d.Widgets[1] = d.Widgets[1], d.Widgets[0]
	normalized, e := NormalizeDashboardDefinition(d)
	if e != nil {
		t.Fatal(e)
	}
	if normalized.Title != "Overview" || normalized.Widgets[0].ID != dashboardWidgetOne {
		t.Fatal(normalized)
	}
	for _, edit := range []func(*DashboardDefinition){func(d *DashboardDefinition) { d.Widgets = nil }, func(d *DashboardDefinition) { d.Widgets[0].ID = d.Widgets[1].ID }, func(d *DashboardDefinition) { d.Widgets[0].ReadingOrder = 1 }, func(d *DashboardDefinition) { d.Widgets[0].Layout.Width = 7 }, func(d *DashboardDefinition) { d.Widgets[0].Layout.X = -1 }, func(d *DashboardDefinition) { d.Widgets[0].Layout.Y = 999; d.Widgets[0].Layout.Height = 2 }, func(d *DashboardDefinition) { d.Widgets[0].AnalysisVersion = 0 }, func(d *DashboardDefinition) { d.Widgets[0].Title = " " }, func(d *DashboardDefinition) { d.Widgets[0].AnalysisID = "foreign text" }} {
		d := dashboardDefinition()
		edit(&d)
		if _, e := NormalizeDashboardDefinition(d); e == nil {
			t.Fatalf("invaliddefinition accepted: %+v", d)
		}
	}
}
func TestDashboardValidationBindsScopeDependenciesAndSemanticDiff(t *testing.T) {
	snapshot := dashboardSnapshot(t)
	first, e := BuildDashboardValidation(snapshot)
	if e != nil {
		t.Fatal(e)
	}
	if !first.Valid || first.ExecutionPerformed || len(first.Diff) != 1 || first.ValidationHash == "" {
		t.Fatal(first)
	}
	changed := snapshot
	changed.Dependencies.Analyses = append([]DashboardDependency(nil), snapshot.Dependencies.Analyses...)
	changed.Dependencies.Analyses[0].DefinitionHash = strings.Repeat("d", 64)
	other, e := BuildDashboardValidation(changed)
	if e != nil || other.ValidationHash == first.ValidationHash {
		t.Fatal("dependency change must invalidate publication", e)
	}
	bad := snapshot
	bad.Dependencies.Environment = "production"
	if _, e = BuildDashboardValidation(bad); e == nil {
		t.Fatal("crossenvironment dependencies accepted")
	}
	snapshot.Draft.PublishedVersion = 1
	snapshot.Baseline = &DashboardVersion{DashboardID: snapshot.Draft.ID, ProjectID: snapshot.Draft.ProjectID, Environment: snapshot.Draft.Environment, Version: 1, Definition: snapshot.Draft.Definition}
	snapshot.Draft.Definition.Widgets = append([]DashboardWidget(nil), snapshot.Draft.Definition.Widgets...)
	snapshot.Draft.Definition.Widgets[0].Layout.Y = 4
	snapshot.Draft.Definition.Widgets[0].AnalysisVersion = 2
	snapshot.Dependencies.Analyses[0].AnalysisVersion = 2
	update, e := BuildDashboardValidation(snapshot)
	if e != nil {
		t.Fatal(e)
	}
	categories := map[string]bool{}
	for _, d := range update.Diff {
		categories[d.Category] = true
	}
	if !categories["layout"] || !categories["dependencies"] {
		t.Fatal(update.Diff)
	}
}
func dashboardExecution() DashboardExecute {
	return DashboardExecute{TimeRange: analysis.TimeRange{From: time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC), To: time.Date(2026, 11, 2, 0, 0, 0, 0, time.UTC)}, Timezone: "UTC"}
}
func TestDashboardRuntimeFiltersPreserveFixedPredicatesAndBoundVersions(t *testing.T) {
	fixed := &analysis.Filter{Op: "eq", Field: &analysis.Field{Kind: "system", Key: "uid"}, Value: "fixed"}
	runtime := &analysis.Filter{Op: "eq", Field: &analysis.Field{Kind: "property", EventKey: "page_opened", SchemaVersion: 1, Key: "enabled"}, Value: false}
	version := SavedAnalysisVersion{Environment: "development", Definition: SavedAnalysisDefinition{Query: analysis.Template{Kind: "trend", EventVersions: []analysis.EventVersion{{EventKey: "page_opened", SchemaVersion: 1}}, Filter: fixed}}}
	request, e := BindDashboardWidget(version, runtime, dashboardExecution())
	if e != nil {
		t.Fatal(e)
	}
	if request.Filter.Op != "and" || len(request.Filter.Args) != 2 || request.Filter.Args[0].Value != "fixed" || request.Filter.Args[1].Value != false {
		t.Fatal(request.Filter)
	}
	if !reflect.DeepEqual(version.Definition.Query.Filter, fixed) || version.Definition.Query.Filter.Op != "eq" {
		t.Fatal("mutatedpublishedfilter")
	}
	runtime.Field.EventKey = "foreign_event"
	if _, e = BindDashboardWidget(version, runtime, dashboardExecution()); e == nil {
		t.Fatal("unselectedeventfilter accepted")
	}
	runtime.Field.Kind = "system"
	runtime.Field.Key = "sql"
	if _, e = BindDashboardWidget(version, runtime, dashboardExecution()); e == nil {
		t.Fatal("invalidsystemfield accepted")
	}
}
func TestDashboardExecutionTargetsAndCanonicalInput(t *testing.T) {
	in := dashboardExecution()
	in.WidgetFilters = []DashboardWidgetFilter{{WidgetID: dashboardWidgetTwo, Filter: &analysis.Filter{Op: "eq", Field: &analysis.Field{Kind: "system", Key: "uid"}, Value: "two"}}, {WidgetID: dashboardWidgetOne, Filter: &analysis.Filter{Op: "eq", Field: &analysis.Field{Kind: "system", Key: "uid"}, Value: "one"}}}
	normalized, e := NormalizeDashboardExecution(in, dashboardDefinition())
	if e != nil || normalized.WidgetFilters[0].WidgetID != dashboardWidgetOne {
		t.Fatal(normalized, e)
	}
	normalized.WidgetFilters[0].Filter.Value = "edited"
	if in.WidgetFilters[1].Filter.Value != "one" {
		t.Fatal("runtimeinputnotindependent")
	}
	for _, filters := range [][]DashboardWidgetFilter{{{WidgetID: "unknown", Filter: in.WidgetFilters[0].Filter}}, {{WidgetID: dashboardWidgetOne, Filter: nil}}, {in.WidgetFilters[0], in.WidgetFilters[0]}} {
		in.WidgetFilters = filters
		if _, e = NormalizeDashboardExecution(in, dashboardDefinition()); e == nil {
			t.Fatal("invalidwidgettargetaccepted", filters)
		}
	}
}
func TestDashboardRunStatesKeepMixedOutcomes(t *testing.T) {
	for _, tc := range []struct {
		states []string
		want   string
	}{{[]string{"queued", "queued"}, "queued"}, {[]string{"succeeded", "running"}, "running"}, {[]string{"succeeded", "succeeded"}, "succeeded"}, {[]string{"succeeded", "failed"}, "partial"}, {[]string{"succeeded", "expired"}, "partial"}, {[]string{"failed", "canceled"}, "failed"}, {[]string{"canceled", "canceled"}, "canceled"}, {[]string{"expired", "expired"}, "expired"}} {
		widgets := []DashboardWidgetRun{}
		for _, state := range tc.states {
			widgets = append(widgets, DashboardWidgetRun{Query: analysis.Job{State: state}})
		}
		if got := DashboardRunState(widgets); got != tc.want {
			t.Fatal(tc.states, got, tc.want)
		}
	}
}
func TestDashboardContractsRejectResultCopiesAndUnknownRuntimeFields(t *testing.T) {
	v := savedValidator(t)
	encoded, _ := json.Marshal(DashboardCreate{Environment: "development", Definition: dashboardDefinition()})
	value, e := contract.Decode(encoded)
	if e != nil {
		t.Fatal(e)
	}
	if e = v.Validate("DashboardCreate", value); e != nil {
		t.Fatal(e)
	}
	object := value.(map[string]any)
	object["definition"].(map[string]any)["rows"] = []any{}
	if e = v.Validate("DashboardCreate", value); e == nil {
		t.Fatal("resultcopyaccepted")
	}
	raw, _ := json.Marshal(dashboardExecution())
	value, _ = contract.Decode(raw)
	value.(map[string]any)["sql"] = "SELECT 1"
	if e = v.Validate("DashboardExecute", value); e == nil {
		t.Fatal("runtimeSQLaccepted")
	}
}

type dashboardStoreFake struct {
	DashboardStore
	calls int
}

func (f *dashboardStoreFake) EnqueueDashboardRun(context.Context, string, string, string, int64, string, DashboardExecute) (DashboardRun, error) {
	f.calls++
	return DashboardRun{}, nil
}
func TestDashboardServiceExecutionAvailabilityAndBudgets(t *testing.T) {
	f := &dashboardStoreFake{}
	s := NewDashboards(f, savedValidator(t), false)
	_, e := s.EnqueueDashboardRun(context.Background(), "actor", "project", "id", 1, "key", dashboardExecution())
	savedErrorCode(t, e, "QUERY_STORAGE_UNAVAILABLE")
	if f.calls != 0 {
		t.Fatal("disabledengineadmittedrun")
	}
	s.executionAvailable = true
	in := dashboardExecution()
	in.TimeRange.To = in.TimeRange.From.Add(32 * 24 * time.Hour)
	_, e = s.EnqueueDashboardRun(context.Background(), "actor", "project", "id", 1, "key", in)
	if e == nil || f.calls != 0 {
		t.Fatal("unboundedrunadmitted")
	}
	_, e = s.EnqueueDashboardRun(context.Background(), "actor", "project", "id", 1, "key", dashboardExecution())
	if e != nil || f.calls != 1 {
		t.Fatal(e, f.calls)
	}
}
