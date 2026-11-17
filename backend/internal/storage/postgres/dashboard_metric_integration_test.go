//go:build integration

package postgres

import (
	"context"
	"testing"

	"github.com/howtomakeaname/Evia/backend/internal/analysis"
	"github.com/howtomakeaname/Evia/backend/internal/reporting"
)

// publishDashboardMetric creates and publishes a count metric on the already
// released lesson_opened event, giving a dashboard a metric-version source.
func publishDashboardMetric(t *testing.T, f catalogFixture) reporting.MetricDraft {
	t.Helper()
	ctx := context.Background()
	def := reporting.MetricDefinition{
		Title:         "Dashboard metric",
		EventVersions: []analysis.EventVersion{{EventKey: "lesson_opened", SchemaVersion: 1}},
		Subject:       "uid",
		Aggregation:   analysis.Metric{Op: "count"},
		Unit:          "event",
	}
	created, e := f.s.CreateMetric(ctx, f.owner, f.project, "dash-metric-create", reporting.MetricCreate{Environment: "development", Definition: def})
	if e != nil {
		t.Fatal(e)
	}
	snapshot, e := f.s.MetricValidationSnapshot(ctx, f.owner, f.project, created.ID, reporting.MetricValidate{DraftRevision: created.DraftRevision, ExpectedPublishedVersion: 0})
	if e != nil {
		t.Fatal(e)
	}
	validation, e := reporting.BuildMetricValidation(snapshot)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = f.s.PublishMetric(ctx, f.owner, f.project, created.ID, "dash-metric-publish", reporting.MetricPublish{DraftRevision: created.DraftRevision, ExpectedPublishedVersion: 0, ValidationHash: validation.ValidationHash}); e != nil {
		t.Fatal(e)
	}
	published, e := f.s.GetMetricDraft(ctx, f.owner, f.project, created.ID)
	if e != nil {
		t.Fatal(e)
	}
	return published
}

func TestDashboardMetricWidgetLifecycle(t *testing.T) {
	f, analysisVersion, execute := savedRunFixture(t)
	ctx := context.Background()
	metric := publishDashboardMetric(t, f)

	// Rebuild the dashboard with one analysis widget and one metric widget.
	def := reporting.DashboardDefinition{
		Title:       "Mixed source dashboard",
		Description: "analysis plus metric",
		Widgets: []reporting.DashboardWidget{
			{ID: "11111111-1111-4111-8111-111111111111", AnalysisID: analysisVersion.AnalysisID, AnalysisVersion: analysisVersion.Version, Title: "Analysis card", Layout: reporting.DashboardLayout{X: 0, Y: 0, Width: 6, Height: 2}},
			{ID: "22222222-2222-4222-8222-222222222222", MetricRef: &reporting.MetricReference{MetricID: metric.ID, Version: metric.PublishedVersion}, Title: "Metric card", ReadingOrder: 1, Layout: reporting.DashboardLayout{X: 6, Y: 0, Width: 6, Height: 2}},
		},
	}
	created, e := f.s.CreateDashboard(ctx, f.owner, f.project, "mixed-dashboard", reporting.DashboardCreate{Environment: "development", Definition: def})
	if e != nil {
		t.Fatal(e)
	}
	published := dashboardPublish(t, f, created)

	// The two witnesses target different immutable version tables.
	var analysisWitnesses, metricWitnesses int
	if e = f.s.Pool.QueryRow(ctx, `SELECT count(*) FILTER (WHERE analysis_id IS NOT NULL), count(*) FILTER (WHERE metric_id IS NOT NULL) FROM dashboard_dependencies WHERE project_id=$1 AND dashboard_id=$2 AND version=$3`, f.project, created.ID, published.Version).Scan(&analysisWitnesses, &metricWitnesses); e != nil {
		t.Fatal(e)
	}
	if analysisWitnesses != 1 || metricWitnesses != 1 {
		t.Fatalf("expected one analysis and one metric witness, got analysis=%d metric=%d", analysisWitnesses, metricWitnesses)
	}

	// Running the dashboard fans out one query per widget, including the metric.
	dashboardExecute := reporting.DashboardExecute{TimeRange: execute.TimeRange, Timezone: execute.Timezone}
	run, e := f.s.EnqueueDashboardRun(ctx, f.owner, f.project, created.ID, published.Version, "mixed-run", dashboardExecute)
	if e != nil {
		t.Fatal(e)
	}
	if len(run.Widgets) != 2 {
		t.Fatalf("expected two run widgets, got %d", len(run.Widgets))
	}
	var sawMetric bool
	for _, w := range run.Widgets {
		if w.MetricRef != nil && w.MetricRef.MetricID == metric.ID {
			sawMetric = true
			if w.AnalysisID != "" {
				t.Fatalf("metric run widget must not carry analysis id: %+v", w)
			}
		}
	}
	if !sawMetric {
		t.Fatalf("metric widget missing from run: %+v", run.Widgets)
	}

	// A metric reference to a nonexistent version fails validation.
	bad := def
	bad.Title = "Bad metric ref"
	bad.Widgets = []reporting.DashboardWidget{{ID: "11111111-1111-4111-8111-111111111111", MetricRef: &reporting.MetricReference{MetricID: "33333333-3333-4333-8333-333333333333", Version: 99}, Title: "Ghost", Layout: reporting.DashboardLayout{Width: 12, Height: 2}}}
	badDraft, e := f.s.CreateDashboard(ctx, f.owner, f.project, "bad-metric-dashboard", reporting.DashboardCreate{Environment: "development", Definition: bad})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = f.s.DashboardValidationSnapshot(ctx, f.owner, f.project, badDraft.ID, reporting.DashboardValidate{DraftRevision: badDraft.DraftRevision, ExpectedPublishedVersion: 0}); e == nil {
		t.Fatal("expected nonexistent metric version rejection")
	}

	// A widget carrying both sources is rejected at normalization.
	ambiguous := def
	ambiguous.Title = "Ambiguous"
	ambiguous.Widgets = []reporting.DashboardWidget{{
		ID:              "11111111-1111-4111-8111-111111111111",
		AnalysisID:      analysisVersion.AnalysisID,
		AnalysisVersion: analysisVersion.Version,
		MetricRef:       &reporting.MetricReference{MetricID: metric.ID, Version: metric.PublishedVersion},
		Title:           "Both",
		Layout:          reporting.DashboardLayout{Width: 12, Height: 2},
	}}
	if _, e = f.s.CreateDashboard(ctx, f.owner, f.project, "ambiguous-dashboard", reporting.DashboardCreate{Environment: "development", Definition: ambiguous}); e == nil {
		t.Fatal("expected ambiguous widget source rejection")
	}
}
