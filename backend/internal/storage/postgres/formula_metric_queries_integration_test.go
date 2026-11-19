//go:build integration

package postgres

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/howtomakeaname/Evia/backend/internal/analysis"
	"github.com/howtomakeaname/Evia/backend/internal/reporting"
)

func formulaPublished(t *testing.T, f formulaFixture) reporting.FormulaMetricVersion {
	t.Helper()
	ctx := context.Background()
	draft, e := f.s.CreateFormulaMetric(ctx, f.owner, f.project, "formula-run-create", reporting.FormulaMetricCreate{Environment: "development", Definition: formulaMetricDef(f.atomicA, f.atomicB)})
	if e != nil {
		t.Fatal(e)
	}
	validation := formulaValidate(t, f, draft)
	version, e := f.s.PublishFormulaMetric(ctx, f.owner, f.project, draft.ID, "formula-run-publish", reporting.FormulaMetricPublish{DraftRevision: draft.DraftRevision, ExpectedPublishedVersion: 0, ValidationHash: validation.ValidationHash})
	if e != nil {
		t.Fatal(e)
	}
	return version
}

func formulaExecuteInput() reporting.MetricExecute {
	from := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	return reporting.MetricExecute{TimeRange: analysis.TimeRange{From: from, To: from.Add(24 * time.Hour)}, Timezone: "UTC"}
}

func TestFormulaMetricEnqueueIdempotencyAndMarker(t *testing.T) {
	f := formulaDB(t)
	ctx := context.Background()
	version := formulaPublished(t, f)
	in := formulaExecuteInput()
	job, e := f.s.EnqueueFormulaMetricVersion(ctx, f.owner, f.project, version.MetricID, version.Version, "formula-run-key", in)
	if e != nil {
		t.Fatal(e)
	}
	if job.State != analysis.JobStateQueued {
		t.Fatalf("job state %s", job.State)
	}
	var kind string
	var requestJSON []byte
	if e = f.s.Pool.QueryRow(ctx, `SELECT kind,request FROM analysis_query_jobs WHERE id=$1`, job.ID).Scan(&kind, &requestJSON); e != nil {
		t.Fatal(e)
	}
	if kind != "trend" {
		t.Fatalf("formula job must use trend kind, got %s", kind)
	}
	var stored analysis.Request
	if e = json.Unmarshal(requestJSON, &stored); e != nil {
		t.Fatal(e)
	}
	if stored.Formula == nil || stored.Formula.MetricID != version.MetricID || stored.Formula.Version != version.Version {
		t.Fatalf("missing formula marker: %+v", stored.Formula)
	}
	// Same key and payload replay the original job.
	replay, e := f.s.EnqueueFormulaMetricVersion(ctx, f.owner, f.project, version.MetricID, version.Version, "formula-run-key", in)
	if e != nil || replay.ID != job.ID {
		t.Fatalf("replay id=%s err=%v", replay.ID, e)
	}
	// A different payload under the same key is a conflict.
	different := in
	different.Timezone = "Asia/Shanghai"
	if _, e = f.s.EnqueueFormulaMetricVersion(ctx, f.owner, f.project, version.MetricID, version.Version, "formula-run-key", different); e == nil {
		t.Fatal("expected idempotency conflict")
	}
}

func TestFormulaMetricLoadExecutionBuildsLeafRequests(t *testing.T) {
	f := formulaDB(t)
	ctx := context.Background()
	version := formulaPublished(t, f)
	in := formulaExecuteInput()
	scope := analysis.Scope{ProjectID: f.project, Environment: "development"}
	plan, e := f.s.LoadFormulaExecution(ctx, scope, reporting.MetricReference{MetricID: version.MetricID, Version: version.Version}, in)
	if e != nil {
		t.Fatal(e)
	}
	if len(plan.AtomicRequests) != 2 {
		t.Fatalf("expected two leaf requests, got %d", len(plan.AtomicRequests))
	}
	for ref, request := range plan.AtomicRequests {
		if request.Formula != nil {
			t.Fatal("leaf request must not carry a formula marker")
		}
		if request.Kind != analysis.QueryKindTrend || request.Environment != "development" || len(request.EventVersions) != 1 {
			t.Fatalf("leaf %+v request malformed: %+v", ref, request)
		}
		if !request.TimeRange.From.Equal(in.TimeRange.From) || request.Timezone != "UTC" {
			t.Fatalf("runtime parameters not propagated to leaf: %+v", request)
		}
	}
	rootRef := reporting.MetricReference{MetricID: version.MetricID, Version: version.Version}
	if len(plan.Composite) != 1 || len(reporting.FormulaReferences(plan.Composite[rootRef].Expression)) != 2 {
		t.Fatalf("plan must contain the root composite definition: %+v", plan.Composite)
	}
}

func TestFormulaMetricEnqueueRejectsUnknownVersionAndWindow(t *testing.T) {
	f := formulaDB(t)
	ctx := context.Background()
	version := formulaPublished(t, f)
	if _, e := f.s.EnqueueFormulaMetricVersion(ctx, f.owner, f.project, version.MetricID, 99, "bad-version", formulaExecuteInput()); e == nil {
		t.Fatal("expected unknown formula version rejection")
	}
	badWindow := formulaExecuteInput()
	badWindow.TimeRange.To = badWindow.TimeRange.From.Add(32 * 24 * time.Hour)
	if _, e := f.s.EnqueueFormulaMetricVersion(ctx, f.owner, f.project, version.MetricID, version.Version, "bad-window", badWindow); e == nil {
		t.Fatal("expected window budget rejection")
	}
	badInterval := formulaExecuteInput()
	badInterval.Interval = "week"
	if _, e := f.s.EnqueueFormulaMetricVersion(ctx, f.owner, f.project, version.MetricID, version.Version, "bad-interval", badInterval); e == nil {
		t.Fatal("expected interval rejection")
	}
}

func TestFormulaMetricLoadExecutionRejectsScopeMismatch(t *testing.T) {
	f := formulaDB(t)
	version := formulaPublished(t, f)
	scope := analysis.Scope{ProjectID: f.project, Environment: "production"}
	if _, e := f.s.LoadFormulaExecution(context.Background(), scope, reporting.MetricReference{MetricID: version.MetricID, Version: version.Version}, formulaExecuteInput()); e == nil {
		t.Fatal("expected cross environment rejection")
	}
}
