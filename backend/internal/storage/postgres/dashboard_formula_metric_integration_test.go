//go:build integration

package postgres

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/howtomakeaname/Evia/backend/internal/reporting"
)

func TestDashboardCompositeFormulaWidgetLifecycle(t *testing.T) {
	f, formula := publishCompositeFormula(t)
	ctx := context.Background()

	def := reporting.DashboardDefinition{
		Title:       "Composite formula dashboard",
		Description: "one composite metric widget",
		Widgets: []reporting.DashboardWidget{
			{
				ID:           "11111111-1111-4111-8111-111111111111",
				MetricRef:    &reporting.MetricReference{MetricID: formula.MetricID, Version: formula.Version, Kind: reporting.MetricRefKindComposite},
				Title:        "Formula card",
				ReadingOrder: 0,
				Layout:       reporting.DashboardLayout{X: 0, Y: 0, Width: 12, Height: 2},
			},
		},
	}
	created, e := f.s.CreateDashboard(ctx, f.owner, f.project, "composite-dashboard", reporting.DashboardCreate{Environment: "development", Definition: def})
	if e != nil {
		t.Fatal(e)
	}
	published := dashboardPublish(t, f.catalogFixture, created)

	// The witness must land in the formula target columns, never the atomic ones.
	var formulaWitnesses, atomicWitnesses, analysisWitnesses int
	if e = f.s.Pool.QueryRow(ctx, `SELECT count(*) FILTER (WHERE formula_metric_id IS NOT NULL), count(*) FILTER (WHERE metric_id IS NOT NULL), count(*) FILTER (WHERE analysis_id IS NOT NULL) FROM dashboard_dependencies WHERE project_id=$1 AND dashboard_id=$2 AND version=$3`, f.project, created.ID, published.Version).Scan(&formulaWitnesses, &atomicWitnesses, &analysisWitnesses); e != nil {
		t.Fatal(e)
	}
	if formulaWitnesses != 1 || atomicWitnesses != 0 || analysisWitnesses != 0 {
		t.Fatalf("expected one formula witness only, got formula=%d atomic=%d analysis=%d", formulaWitnesses, atomicWitnesses, analysisWitnesses)
	}
	var pinnedID, pinnedVersion any
	if e = f.s.Pool.QueryRow(ctx, `SELECT formula_metric_id::text, formula_metric_version FROM dashboard_dependencies WHERE project_id=$1 AND dashboard_id=$2 AND version=$3`, f.project, created.ID, published.Version).Scan(&pinnedID, &pinnedVersion); e != nil {
		t.Fatal(e)
	}
	if pinnedID != formula.MetricID {
		t.Fatalf("formula witness pins wrong id: %v", pinnedID)
	}

	// Running fans out a formula_ref marker job and the readback keeps kind=composite.
	r := queryRequest(t, "trend")
	run, e := f.s.EnqueueDashboardRun(ctx, f.owner, f.project, created.ID, published.Version, "composite-run", reporting.DashboardExecute{TimeRange: r.TimeRange, Timezone: r.Timezone})
	if e != nil {
		t.Fatal(e)
	}
	if len(run.Widgets) != 1 {
		t.Fatalf("expected one run widget, got %d", len(run.Widgets))
	}
	widget := run.Widgets[0]
	if widget.MetricRef == nil || !widget.MetricRef.IsComposite() || widget.MetricRef.MetricID != formula.MetricID {
		t.Fatalf("run widget must echo the composite metric ref: %+v", widget.MetricRef)
	}
	var stored []byte
	if e = f.s.Pool.QueryRow(ctx, `SELECT request FROM analysis_query_jobs WHERE id=$1`, widget.Query.ID).Scan(&stored); e != nil {
		t.Fatal(e)
	}
	var marker struct {
		Formula *struct {
			MetricID string `json:"metric_id"`
			Version  int64  `json:"version"`
		} `json:"formula_ref"`
	}
	if e = json.Unmarshal(stored, &marker); e != nil {
		t.Fatal(e)
	}
	if marker.Formula == nil || marker.Formula.MetricID != formula.MetricID || marker.Formula.Version != formula.Version {
		t.Fatalf("dashboard job must carry the formula_ref marker: %s", string(stored))
	}
	var runFormulaWitnesses int
	if e = f.s.Pool.QueryRow(ctx, `SELECT count(*) FROM dashboard_run_widgets WHERE run_id=$1 AND formula_metric_id IS NOT NULL`, run.ID).Scan(&runFormulaWitnesses); e != nil {
		t.Fatal(e)
	}
	if runFormulaWitnesses != 1 {
		t.Fatalf("run widget witness must use the formula target, got %d", runFormulaWitnesses)
	}

	// The run-widget soundness CHECK must reject an all-NULL identity row.
	// Its dependency foreign keys use MATCH SIMPLE and skip NULLs, so this CHECK
	// is the only database boundary blocking a run widget without an immutable
	// source; the run FK/position constraints are satisfied by the real run.
	if _, e = f.s.Pool.Exec(ctx, `INSERT INTO dashboard_run_widgets(project_id,environment,dashboard_id,version,run_id,widget_id,position,period,original_query_id,query_created_at)
	 VALUES($1,$2,$3,$4,$5,$6,1,'current',$7,clock_timestamp())`,
		f.project, published.Environment, created.ID, published.Version, run.ID, "22222222-2222-4222-8222-222222222222", widget.Query.ID); e == nil {
		t.Fatal("expected run widget soundness CHECK to reject all-NULL identity row")
	}

	// A composite ref to a nonexistent formula version fails closed.
	bad := def
	bad.Title = "Bad formula ref"
	bad.Widgets[0].MetricRef = &reporting.MetricReference{MetricID: "33333333-3333-4333-8333-333333333333", Version: 99, Kind: reporting.MetricRefKindComposite}
	badDraft, e := f.s.CreateDashboard(ctx, f.owner, f.project, "bad-formula-dashboard", reporting.DashboardCreate{Environment: "development", Definition: bad})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = f.s.DashboardValidationSnapshot(ctx, f.owner, f.project, badDraft.ID, reporting.DashboardValidate{DraftRevision: badDraft.DraftRevision, ExpectedPublishedVersion: 0}); e == nil {
		t.Fatal("expected nonexistent formula version rejection")
	}
}

func repeatA64() string {
	out := make([]byte, 64)
	for i := range out {
		out[i] = 'a'
	}
	return string(out)
}
