//go:build integration

package postgres

import (
	"context"
	"testing"
	"time"

	"github.com/howtomakeaname/Evia/backend/internal/analysis"
	"github.com/howtomakeaname/Evia/backend/internal/reporting"
)

func TestDashboardComparisonFanoutAndShiftedWindow(t *testing.T) {
	f, d, in := dashboardFixture(t, 1)
	dashboardPublish(t, f, d)
	ctx := context.Background()

	spec := reporting.ComparisonSpec{Unit: reporting.ComparisonWeek, Amount: 1}
	execute := in
	execute.Comparison = &spec
	run, e := f.s.EnqueueDashboardRun(ctx, f.owner, f.project, d.ID, 1, "comparison-run", execute)
	if e != nil {
		t.Fatal(e)
	}
	if len(run.Widgets) != 1 || run.Widgets[0].Comparison == nil {
		t.Fatalf("expected one widget with a comparison job: %+v", run.Widgets)
	}
	widget := run.Widgets[0]
	if widget.Comparison.ID == widget.Query.ID {
		t.Fatal("comparison must use a distinct child query")
	}
	if run.Comparison == nil || run.Comparison.Unit != spec.Unit || run.Comparison.Amount != 1 {
		t.Fatalf("run must echo the comparison spec: %+v", run.Comparison)
	}

	// The relational witness keeps one row per period sharing the position.
	var currentRows, comparisonRows int
	if e = f.s.Pool.QueryRow(ctx, `SELECT count(*) FILTER (WHERE period='current'), count(*) FILTER (WHERE period='comparison') FROM dashboard_run_widgets WHERE run_id=$1`, run.ID).Scan(&currentRows, &comparisonRows); e != nil {
		t.Fatal(e)
	}
	if currentRows != 1 || comparisonRows != 1 {
		t.Fatalf("expected one row per period, got current=%d comparison=%d", currentRows, comparisonRows)
	}

	// Each child query carries its own window; the comparison request uses the
	// DST-safe shifted range rather than the requested one.
	wantShifted, e := reporting.ShiftTimeRange(in.TimeRange, in.Timezone, spec)
	if e != nil {
		t.Fatal(e)
	}
	var currentFrom, comparisonFrom string
	if e = f.s.Pool.QueryRow(ctx, `
	  SELECT cur.from_text, cmp.from_text FROM
	    (SELECT request->'time_range'->>'from' AS from_text FROM analysis_query_jobs WHERE id=$1) cur,
	    (SELECT request->'time_range'->>'from' AS from_text FROM analysis_query_jobs WHERE id=$2) cmp`,
		widget.Query.ID, widget.Comparison.ID).Scan(&currentFrom, &comparisonFrom); e != nil {
		t.Fatal(e)
	}
	if comparisonFrom == currentFrom {
		t.Fatalf("comparison window must differ from current window: %s", comparisonFrom)
	}
	var shiftedStored string
	if e = f.s.Pool.QueryRow(ctx, `SELECT request->'time_range'->>'from' FROM analysis_query_jobs WHERE id=$1`, widget.Comparison.ID).Scan(&shiftedStored); e != nil {
		t.Fatal(e)
	}
	shiftedInstant, parseErr := time.Parse(time.RFC3339Nano, shiftedStored)
	if parseErr != nil {
		t.Fatal(parseErr)
	}
	if !shiftedInstant.Equal(wantShifted.From) {
		t.Fatalf("comparison request window mismatch: got %s want %s", shiftedInstant, wantShifted.From)
	}
	var currentStored string
	if e = f.s.Pool.QueryRow(ctx, `SELECT request->'time_range'->>'from' FROM analysis_query_jobs WHERE id=$1`, widget.Query.ID).Scan(&currentStored); e != nil {
		t.Fatal(e)
	}
	currentInstant, parseErr := time.Parse(time.RFC3339Nano, currentStored)
	if parseErr != nil {
		t.Fatal(parseErr)
	}
	if !currentInstant.Equal(in.TimeRange.From) {
		t.Fatalf("current request window must stay unchanged: got %s want %s", currentInstant, in.TimeRange.From)
	}

	// Same-key replay returns the identical run including the comparison job.
	replay, e := f.s.EnqueueDashboardRun(ctx, f.owner, f.project, d.ID, 1, "comparison-run", execute)
	if e != nil || replay.ID != run.ID || replay.Widgets[0].Comparison == nil {
		t.Fatalf("idempotent replay must preserve the comparison run: %+v %v", replay, e)
	}

	// A different comparison spec is a different request and needs a new key.
	changed := execute
	changed.Comparison = &reporting.ComparisonSpec{Unit: reporting.ComparisonMonth, Amount: 1}
	second, e := f.s.EnqueueDashboardRun(ctx, f.owner, f.project, d.ID, 1, "comparison-run", changed)
	faultCode(t, e, "IDEMPOTENCY_CONFLICT")
	second, e = f.s.EnqueueDashboardRun(ctx, f.owner, f.project, d.ID, 1, "comparison-run-2", changed)
	if e != nil || second.ID == run.ID {
		t.Fatalf("changed comparison should create a distinct run: %+v %v", second, e)
	}

	// Cancelling the run cancels both child jobs.
	if _, e = f.s.CancelDashboardRun(ctx, f.owner, f.project, run.ID); e != nil {
		t.Fatal(e)
	}
	cancelled, e := f.s.GetDashboardRun(ctx, f.owner, f.project, run.ID)
	if e != nil {
		t.Fatal(e)
	}
	for _, w := range cancelled.Widgets {
		if w.Query.State != analysis.JobStateCanceled || w.Comparison == nil || w.Comparison.State != analysis.JobStateCanceled {
			t.Fatalf("both current and comparison jobs must be canceled: %+v", w)
		}
	}
}

func TestDashboardComparisonRejectsInvalidSpec(t *testing.T) {
	f, d, in := dashboardFixture(t, 1)
	dashboardPublish(t, f, d)
	ctx := context.Background()
	for _, spec := range []reporting.ComparisonSpec{
		{Unit: "fortnight", Amount: 1},
		{Unit: reporting.ComparisonDay, Amount: 0},
		{Unit: reporting.ComparisonYear, Amount: 13},
	} {
		execute := in
		execute.Comparison = &spec
		if _, e := f.s.EnqueueDashboardRun(ctx, f.owner, f.project, d.ID, 1, "bad-comparison-"+spec.Unit, execute); e == nil {
			t.Fatalf("spec %+v must be rejected", spec)
		}
	}
}

func TestDashboardWithoutComparisonKeepsSinglePeriod(t *testing.T) {
	f, d, in := dashboardFixture(t, 2)
	dashboardPublish(t, f, d)
	ctx := context.Background()
	run, e := f.s.EnqueueDashboardRun(ctx, f.owner, f.project, d.ID, 1, "no-comparison", in)
	if e != nil {
		t.Fatal(e)
	}
	if run.Comparison != nil {
		t.Fatal("run without comparison spec must not echo one")
	}
	for _, w := range run.Widgets {
		if w.Comparison != nil {
			t.Fatalf("widget must not carry a comparison job: %+v", w)
		}
	}
	var rows int
	if e = f.s.Pool.QueryRow(ctx, `SELECT count(*) FROM dashboard_run_widgets WHERE run_id=$1 AND period <> 'current'`, run.ID).Scan(&rows); e != nil {
		t.Fatal(e)
	}
	if rows != 0 {
		t.Fatalf("legacy path must only write current-period rows, got %d", rows)
	}
}

func TestDashboardMetricWidgetComparisonFanout(t *testing.T) {
	f, analysisVersion, execute := savedRunFixture(t)
	ctx := context.Background()
	metric := publishDashboardMetric(t, f)
	def := reporting.DashboardDefinition{
		Title:       "Metric comparison",
		Description: "",
		Widgets: []reporting.DashboardWidget{
			{ID: "11111111-1111-4111-8111-111111111111", AnalysisID: analysisVersion.AnalysisID, AnalysisVersion: analysisVersion.Version, Title: "Analysis", Layout: reporting.DashboardLayout{Width: 6, Height: 2}},
			{ID: "22222222-2222-4222-8222-222222222222", MetricRef: &reporting.MetricReference{MetricID: metric.ID, Version: metric.PublishedVersion}, Title: "Metric", ReadingOrder: 1, Layout: reporting.DashboardLayout{X: 6, Y: 0, Width: 6, Height: 2}},
		},
	}
	created, e := f.s.CreateDashboard(ctx, f.owner, f.project, "metric-comparison-dashboard", reporting.DashboardCreate{Environment: "development", Definition: def})
	if e != nil {
		t.Fatal(e)
	}
	published := dashboardPublish(t, f, created)
	dashboardExecute := reporting.DashboardExecute{
		TimeRange:  execute.TimeRange,
		Timezone:   execute.Timezone,
		Comparison: &reporting.ComparisonSpec{Unit: reporting.ComparisonDay, Amount: 7},
	}
	run, e := f.s.EnqueueDashboardRun(ctx, f.owner, f.project, created.ID, published.Version, "metric-comparison-run", dashboardExecute)
	if e != nil {
		t.Fatal(e)
	}
	if len(run.Widgets) != 2 {
		t.Fatalf("expected two widgets, got %d", len(run.Widgets))
	}
	for _, w := range run.Widgets {
		if w.Comparison == nil || w.Comparison.ID == w.Query.ID {
			t.Fatalf("every widget needs a distinct comparison job: %+v", w)
		}
	}
	// Two widgets x two periods means four child query jobs for this run.
	var jobs int
	if e = f.s.Pool.QueryRow(ctx, `SELECT count(*) FROM dashboard_run_widgets WHERE run_id=$1`, run.ID).Scan(&jobs); e != nil {
		t.Fatal(e)
	}
	if jobs != 4 {
		t.Fatalf("expected four run widget rows, got %d", jobs)
	}
}
