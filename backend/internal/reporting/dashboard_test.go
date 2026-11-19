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
	version := SavedAnalysisVersion{Environment: "development", Definition: SavedAnalysisDefinition{Query: analysis.Template{Kind: analysis.QueryKindTrend, EventVersions: []analysis.EventVersion{{EventKey: "page_opened", SchemaVersion: 1}}, Filter: fixed}}}
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
func parameterizedDashboardDefinition() DashboardDefinition {
	d := dashboardDefinition()
	d.Parameters = []ParameterDeclaration{
		{ParameterID: "section", Label: "板块", Type: ParameterString, Operator: "eq"},
		{ParameterID: "limit", Label: "下限", Type: ParameterNumber, Operator: "gte"},
	}
	d.Widgets[0].ParameterBindings = []ParameterBinding{{
		ParameterID: "section",
		Field:       analysis.Field{Kind: "property", Key: "section_id", EventKey: "page_opened", SchemaVersion: 1},
	}}
	return d
}

func TestDashboardDefinitionParameterNormalization(t *testing.T) {
	if _, e := NormalizeDashboardDefinition(parameterizedDashboardDefinition()); e != nil {
		t.Fatalf("valid parameterized definition rejected: %v", e)
	}
	propertyField := analysis.Field{Kind: "property", Key: "section_id", EventKey: "page_opened", SchemaVersion: 1}
	for _, edit := range []func(*DashboardDefinition){
		// binding references an undeclared parameter
		func(d *DashboardDefinition) { d.Parameters = nil },
		// duplicate parameter identifier
		func(d *DashboardDefinition) { d.Parameters = append(d.Parameters, d.Parameters[0]) },
		// operator/type closed set mismatch
		func(d *DashboardDefinition) { d.Parameters[0].Operator = "regexp" },
		// same parameter bound twice on one widget
		func(d *DashboardDefinition) {
			d.Widgets[0].ParameterBindings = []ParameterBinding{
				{ParameterID: "section", Field: propertyField},
				{ParameterID: "section", Field: analysis.Field{Kind: "property", Key: "other_id", EventKey: "page_opened", SchemaVersion: 1}},
			}
		},
		// same field bound by two parameters
		func(d *DashboardDefinition) {
			d.Widgets[0].ParameterBindings = []ParameterBinding{
				{ParameterID: "section", Field: propertyField},
				{ParameterID: "limit", Field: propertyField},
			}
		},
		// property field missing event pin
		func(d *DashboardDefinition) {
			d.Widgets[0].ParameterBindings = []ParameterBinding{{ParameterID: "section", Field: analysis.Field{Kind: "property", Key: "section_id"}}}
		},
		// unsupported field kind
		func(d *DashboardDefinition) {
			d.Widgets[0].ParameterBindings = []ParameterBinding{{ParameterID: "section", Field: analysis.Field{Kind: "json", Key: "section_id"}}}
		},
	} {
		d := parameterizedDashboardDefinition()
		edit(&d)
		if _, e := NormalizeDashboardDefinition(d); e == nil {
			t.Fatalf("invalid parameter shape accepted: %+v", d)
		}
	}
	tooMany := parameterizedDashboardDefinition()
	for i := 0; i < MaxParameterDeclarations; i++ {
		tooMany.Parameters = append(tooMany.Parameters, ParameterDeclaration{
			ParameterID: "p" + strings.Repeat("x", i+1), Label: "L", Type: ParameterString, Operator: "eq",
		})
	}
	if _, e := NormalizeDashboardDefinition(tooMany); e == nil {
		t.Fatal("parameter count over cap must be rejected")
	}
}

func TestDashboardExecutionParameterValuesBoundary(t *testing.T) {
	def, e := NormalizeDashboardDefinition(parameterizedDashboardDefinition())
	if e != nil {
		t.Fatal(e)
	}
	in := dashboardExecution()
	in.ParameterValues = ParameterValues{"section": "forum", "limit": json.Number("10")}
	if _, e = NormalizeDashboardExecution(in, def); e != nil {
		t.Fatalf("valid runtime values rejected: %v", e)
	}
	for _, values := range []ParameterValues{
		{"ghost": "x"},
		{"section": float64(7)},
		{"limit": json.Number("NaN")},
	} {
		bad := dashboardExecution()
		bad.ParameterValues = values
		if _, e = NormalizeDashboardExecution(bad, def); e == nil {
			t.Fatalf("runtime values %+v must be rejected", values)
		}
	}
}

func TestCloneDashboardDefinitionFreshIdentityAndRetainedPins(t *testing.T) {
	source, e := NormalizeDashboardDefinition(parameterizedDashboardDefinition())
	if e != nil {
		t.Fatal(e)
	}
	newIDs := []string{"aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"}
	clone, e := CloneDashboardDefinition(source, "Copied dashboard", newIDs)
	if e != nil {
		t.Fatal(e)
	}
	if clone.Title != "Copied dashboard" {
		t.Fatal("copy title not applied")
	}
	if len(clone.Widgets) != 2 {
		t.Fatalf("expected two widgets, got %d", len(clone.Widgets))
	}
	for i, widget := range clone.Widgets {
		if widget.ID != newIDs[i] {
			t.Fatalf("widget %d must receive the fresh ID %s, got %s", i, newIDs[i], widget.ID)
		}
		if source.Widgets[i].ID == widget.ID {
			t.Fatal("copy must not retain source widget IDs")
		}
		if widget.AnalysisID != source.Widgets[i].AnalysisID || widget.AnalysisVersion != source.Widgets[i].AnalysisVersion {
			t.Fatal("pinned immutable references must be retained")
		}
	}
	if len(clone.Parameters) != len(source.Parameters) {
		t.Fatal("parameter declarations must be retained")
	}
	if len(clone.Widgets[0].ParameterBindings) != 1 {
		t.Fatal("parameter bindings must be retained")
	}
	// Mutating the clone must not bleed into the source definition.
	clone.Widgets[0].ParameterBindings[0].ParameterID = "changed"
	if source.Widgets[0].ParameterBindings[0].ParameterID != "section" {
		t.Fatal("clone shares binding storage with the source")
	}
	if _, e = CloneDashboardDefinition(source, "Broken", []string{"only-one"}); e == nil {
		t.Fatal("widget ID count mismatch must be rejected")
	}
}

func rebindSourceDefinition() DashboardDefinition {
	return DashboardDefinition{
		Title: "Mixed",
		Widgets: []DashboardWidget{
			{
				ID: dashboardWidgetOne, AnalysisID: dashboardAnalysisID, AnalysisVersion: 1,
				Title: "Analysis card", Layout: DashboardLayout{Width: 6, Height: 3},
			},
			{
				ID: dashboardWidgetTwo, MetricRef: &MetricReference{MetricID: "55555555-5555-4555-8555-555555555555", Version: 2},
				Title: "Metric card", ReadingOrder: 1, Layout: DashboardLayout{X: 6, Width: 6, Height: 3},
			},
			{
				ID: "66666666-6666-4666-8666-666666666666", TextCard: &TextCard{Content: "# Note"},
				Title: "Text card", ReadingOrder: 2, Layout: DashboardLayout{Y: 3, Width: 12, Height: 3},
			},
		},
	}
}

func TestRebindDashboardDefinitionSwapsExecutableSourcesAndKeepsTextCard(t *testing.T) {
	source, e := NormalizeDashboardDefinition(rebindSourceDefinition())
	if e != nil {
		t.Fatal(e)
	}
	newIDs := []string{
		"aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa",
		"bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb",
		"cccccccc-cccc-4ccc-8ccc-cccccccccccc",
	}
	targetAnalysis := "77777777-7777-4777-8777-777777777777"
	targetMetric := "88888888-8888-4888-8888-888888888888"
	rebound, e := RebindDashboardDefinition(source, "Rebound", newIDs, []DashboardRebinding{
		{WidgetID: dashboardWidgetOne, AnalysisID: targetAnalysis, AnalysisVersion: 4},
		{WidgetID: dashboardWidgetTwo, MetricID: targetMetric, MetricVersion: 5},
	})
	if e != nil {
		t.Fatalf("valid rebinding rejected: %v", e)
	}
	if rebound.Title != "Rebound" || len(rebound.Widgets) != 3 {
		t.Fatalf("rebound shape mismatch: %+v", rebound)
	}
	for i, widget := range rebound.Widgets {
		if widget.ID != newIDs[i] {
			t.Fatalf("widget %d must receive a fresh ID", i)
		}
	}
	analysisCard := rebound.Widgets[0]
	if analysisCard.AnalysisID != targetAnalysis || analysisCard.AnalysisVersion != 4 || analysisCard.MetricRef != nil {
		t.Fatalf("analysis widget not rebound: %+v", analysisCard)
	}
	metricCard := rebound.Widgets[1]
	if metricCard.MetricRef == nil || metricCard.MetricRef.MetricID != targetMetric || metricCard.MetricRef.Version != 5 || metricCard.AnalysisID != "" {
		t.Fatalf("metric widget not rebound: %+v", metricCard)
	}
	textCard := rebound.Widgets[2]
	if textCard.TextCard == nil || textCard.TextCard.Content != "# Note" {
		t.Fatalf("text card must carry over untouched: %+v", textCard)
	}
}

func TestRebindDashboardDefinitionRejectsCoverageAndKindFailures(t *testing.T) {
	source, e := NormalizeDashboardDefinition(rebindSourceDefinition())
	if e != nil {
		t.Fatal(e)
	}
	newIDs := []string{
		"aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa",
		"bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb",
		"cccccccc-cccc-4ccc-8ccc-cccccccccccc",
	}
	targetAnalysis := "77777777-7777-4777-8777-777777777777"
	targetMetric := "88888888-8888-4888-8888-888888888888"
	base := []DashboardRebinding{
		{WidgetID: dashboardWidgetOne, AnalysisID: targetAnalysis, AnalysisVersion: 4},
		{WidgetID: dashboardWidgetTwo, MetricID: targetMetric, MetricVersion: 5},
	}
	for name, mutate := range map[string]func([]DashboardRebinding) []DashboardRebinding{
		"missing executable coverage": func(r []DashboardRebinding) []DashboardRebinding {
			return r[:1]
		},
		"unknown widget entry": func(r []DashboardRebinding) []DashboardRebinding {
			return append(r, DashboardRebinding{
				WidgetID: "99999999-9999-4999-8999-999999999999", AnalysisID: targetAnalysis, AnalysisVersion: 1,
			})
		},
		"text card entry": func(r []DashboardRebinding) []DashboardRebinding {
			return append(r, DashboardRebinding{
				WidgetID: "66666666-6666-4666-8666-666666666666", AnalysisID: targetAnalysis, AnalysisVersion: 1,
			})
		},
		"duplicate widget entry": func(r []DashboardRebinding) []DashboardRebinding {
			return append(r, r[0])
		},
		"kind mismatch on metric widget": func(r []DashboardRebinding) []DashboardRebinding {
			withKind := append([]DashboardRebinding(nil), r...)
			withKind[1] = DashboardRebinding{WidgetID: dashboardWidgetTwo, AnalysisID: targetAnalysis, AnalysisVersion: 4}
			return withKind
		},
		"both source pairs": func(r []DashboardRebinding) []DashboardRebinding {
			withBoth := append([]DashboardRebinding(nil), r...)
			withBoth[0] = DashboardRebinding{
				WidgetID: dashboardWidgetOne, AnalysisID: targetAnalysis, AnalysisVersion: 4,
				MetricID: targetMetric, MetricVersion: 5,
			}
			return withBoth
		},
		"neither source pair": func(r []DashboardRebinding) []DashboardRebinding {
			empty := append([]DashboardRebinding(nil), r...)
			empty[0] = DashboardRebinding{WidgetID: dashboardWidgetOne}
			return empty
		},
	} {
		t.Run(name, func(t *testing.T) {
			if _, e := RebindDashboardDefinition(source, "Rebound", newIDs, mutate(append([]DashboardRebinding(nil), base...))); e == nil {
				t.Fatalf("%s must be rejected", name)
			}
		})
	}
}

func TestDashboardRunStatesKeepMixedOutcomes(t *testing.T) {
	for _, tc := range []struct {
		states []string
		want   string
	}{
		{[]string{analysis.JobStateQueued, analysis.JobStateQueued}, DashboardRunQueued},
		{[]string{analysis.JobStateSucceeded, analysis.JobStateRunning}, DashboardRunRunning},
		{[]string{analysis.JobStateSucceeded, analysis.JobStateSucceeded}, DashboardRunSucceeded},
		{[]string{analysis.JobStateSucceeded, analysis.JobStateFailed}, DashboardRunPartial},
		{[]string{analysis.JobStateSucceeded, analysis.JobStateExpired}, DashboardRunPartial},
		{[]string{analysis.JobStateFailed, analysis.JobStateCanceled}, DashboardRunFailed},
		{[]string{analysis.JobStateCanceled, analysis.JobStateCanceled}, DashboardRunCanceled},
		{[]string{analysis.JobStateExpired, analysis.JobStateExpired}, DashboardRunExpired},
	} {
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
