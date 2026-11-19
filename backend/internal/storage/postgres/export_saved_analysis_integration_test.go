//go:build integration

package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/howtomakeaname/Evia/backend/internal/analysis"
	"github.com/howtomakeaname/Evia/backend/internal/exporting"
	"github.com/howtomakeaname/Evia/backend/internal/reporting"
	"github.com/jackc/pgx/v5/pgconn"
)

// publishSavedAnalysisForExport creates, validates and publishes one saved
// analysis version using the supplied definition and returns its identity.
func publishSavedAnalysisForExport(t *testing.T, f catalogFixture, def reporting.SavedAnalysisDefinition, idem string) (string, int64) {
	t.Helper()
	ctx := context.Background()
	created, e := f.s.CreateSavedAnalysis(ctx, f.owner, f.project, idem, reporting.SavedAnalysisCreate{
		Environment: "development",
		Definition:  def,
	})
	if e != nil {
		t.Fatal(e)
	}
	snapshot, e := f.s.SavedAnalysisValidationSnapshot(ctx, f.owner, f.project, created.ID, reporting.SavedAnalysisValidate{DraftRevision: created.DraftRevision})
	if e != nil {
		t.Fatal(e)
	}
	validation, e := reporting.BuildSavedAnalysisValidation(snapshot)
	if e != nil {
		t.Fatal(e)
	}
	if !validation.Valid {
		t.Fatalf("fixture validation must pass: %+v", validation)
	}
	published, e := f.s.PublishSavedAnalysis(ctx, f.owner, f.project, created.ID, "publish-"+idem, savedPublish(created, validation))
	if e != nil {
		t.Fatal(e)
	}
	return created.ID, published.Version
}

// tamperPublishedHash rewrites an immutable version hash on a dedicated
// connection. Published version tables are trigger-protected, so the drift
// simulation temporarily switches to replica role (allowed for the local
// superuser test role) and restores the default before releasing the conn.
func tamperPublishedHash(t *testing.T, f catalogFixture, table string, args ...any) {
	t.Helper()
	ctx := context.Background()
	conn, e := f.s.Pool.Acquire(ctx)
	if e != nil {
		t.Fatal(e)
	}
	defer conn.Release()
	if _, e = conn.Exec(ctx, `SET session_replication_role = replica`); e != nil {
		t.Fatalf("test role cannot bypass immutable triggers: %v", e)
	}
	if _, e = conn.Exec(ctx, "UPDATE "+table+" SET definition_hash=$1 WHERE project_id=$2 AND metric_id=$3 AND version=$4", args...); e != nil {
		t.Fatal(e)
	}
	if _, e = conn.Exec(ctx, `SET session_replication_role = origin`); e != nil {
		t.Fatal(e)
	}
}

func savedExportInput(f catalogFixture, key string, r analysis.Request) exporting.SavedAnalysisExportInput {
	return exporting.SavedAnalysisExportInput{
		ProjectID:      f.project,
		ActorID:        f.owner,
		Name:           "saved export " + key,
		Format:         exporting.FormatJSON,
		TimeRange:      r.TimeRange,
		Timezone:       r.Timezone,
		IdempotencyKey: key,
		RequestHash:    strings.Repeat("a", 64),
	}
}

func TestSavedAnalysisExportBindsSnapshotAndReplays(t *testing.T) {
	f, draft := savedFixture(t)
	ctx := context.Background()
	validation := savedValidate(t, f, draft)
	published, e := f.s.PublishSavedAnalysis(ctx, f.owner, f.project, draft.ID, "export-publish-inline", savedPublish(draft, validation))
	if e != nil {
		t.Fatal(e)
	}
	r := queryRequest(t, "trend")
	in := savedExportInput(f, "export-inline-1", r)
	in.Format = exporting.FormatCSV

	job, created, e := f.s.CreateSavedAnalysisExportJob(ctx, f.owner, f.project, draft.ID, published.Version, in)
	if e != nil || !created {
		t.Fatalf("create saved export: %+v err=%v", job, e)
	}
	if job.State != exporting.StateQueued || job.Format != exporting.FormatCSV {
		t.Fatalf("unexpected job snapshot: %+v", job)
	}
	if job.Source == nil || job.Source.AnalysisID != draft.ID || job.Source.Version != published.Version {
		t.Fatalf("source witness missing: %+v", job.Source)
	}
	if job.Request.Environment != "development" || job.Request.Kind != analysis.QueryKindTrend {
		t.Fatalf("bound request mismatch: %+v", job.Request)
	}
	if !job.Request.TimeRange.From.Equal(r.TimeRange.From.UTC()) || !job.Request.TimeRange.To.Equal(r.TimeRange.To.UTC()) {
		t.Fatalf("bound time range mismatch: %+v", job.Request.TimeRange)
	}
	if job.Request.Formula != nil {
		t.Fatal("inline export must not carry a formula marker")
	}

	// Same actor/project/key replays the accepted snapshot without a new row.
	replay, replayed, e := f.s.CreateSavedAnalysisExportJob(ctx, f.owner, f.project, draft.ID, published.Version, in)
	if e != nil || replayed || replay.JobID != job.JobID || replay.Source == nil {
		t.Fatalf("idempotent replay changed: %+v err=%v replayed=%v", replay, e, replayed)
	}
	diverged := in
	diverged.RequestHash = strings.Repeat("b", 64)
	if _, _, e = f.s.CreateSavedAnalysisExportJob(ctx, f.owner, f.project, draft.ID, published.Version, diverged); e == nil || faultPublicCode(e) != "IDEMPOTENCY_CONFLICT" {
		t.Fatalf("divergent key must conflict: %v", e)
	}

	// The witness survives a fresh read and is claimable by the export worker.
	stored, e := f.s.GetExportJob(ctx, f.owner, f.project, job.JobID)
	if e != nil || stored.Source == nil || stored.Source.AnalysisID != draft.ID {
		t.Fatalf("stored witness lost: %+v err=%v", stored, e)
	}
	claim, e := f.s.ClaimExportJob(ctx, "export-worker-saved", time.Minute)
	if e != nil || claim == nil || claim.JobID != job.JobID || claim.Source == nil || claim.Request.Formula != nil {
		t.Fatalf("claim must retain witness and bound request: %+v err=%v", claim, e)
	}

	// Migration 041 soundness CHECK: a source id without its version is rejected.
	if _, e = f.s.Pool.Exec(ctx, `UPDATE export_jobs SET source_analysis_version=NULL WHERE id=$1`, job.JobID); e == nil {
		t.Fatal("soundness CHECK must reject a half-populated source witness")
	} else {
		var pgErr *pgconn.PgError
		if !errors.As(e, &pgErr) || pgErr.Code != "23514" {
			t.Fatalf("expected check_violation 23514, got %v", e)
		}
	}
}

func TestSavedAnalysisExportAuthorityArchiveAndVersionGuards(t *testing.T) {
	f, draft := savedFixture(t)
	ctx := context.Background()
	validation := savedValidate(t, f, draft)
	published, e := f.s.PublishSavedAnalysis(ctx, f.owner, f.project, draft.ID, "export-publish-guards", savedPublish(draft, validation))
	if e != nil {
		t.Fatal(e)
	}
	r := queryRequest(t, "trend")

	blocked := savedExportInput(f, "export-viewer-denied", r)
	blocked.ActorID = f.viewer
	if _, _, e = f.s.CreateSavedAnalysisExportJob(ctx, f.viewer, f.project, draft.ID, published.Version, blocked); e == nil {
		t.Fatal("viewer export creation must be rejected")
	}
	blocked.ActorID = f.outsider
	blocked.IdempotencyKey = "export-outsider-denied"
	if _, _, e = f.s.CreateSavedAnalysisExportJob(ctx, f.outsider, f.project, draft.ID, published.Version, blocked); e == nil {
		t.Fatal("outsider export creation must be rejected")
	}

	// An unknown immutable version cannot be bound.
	missing := savedExportInput(f, "export-missing-version", r)
	if _, _, e = f.s.CreateSavedAnalysisExportJob(ctx, f.owner, f.project, draft.ID, published.Version+99, missing); e == nil {
		t.Fatal("missing saved analysis version must be rejected")
	}

	// Archived analyses fail closed for new exports even though versions stay readable.
	if _, e = f.s.ArchiveSavedAnalysis(ctx, f.owner, f.project, draft.ID); e != nil {
		t.Fatal(e)
	}
	archivedInput := savedExportInput(f, "export-archived", r)
	if _, _, e = f.s.CreateSavedAnalysisExportJob(ctx, f.owner, f.project, draft.ID, published.Version, archivedInput); e == nil || faultPublicCode(e) != "SAVED_ANALYSIS_ARCHIVED" {
		t.Fatalf("archived analysis export must fail closed: %v", e)
	}
}

func TestSavedAnalysisExportAtomicPinDriftFailsClosed(t *testing.T) {
	f, metric := metricBackedFixture(t)
	ctx := context.Background()
	analysisID, version := publishSavedAnalysisForExport(t, f, metricBackedDefinition(metric.MetricID), "metric-export-analysis")
	r := queryRequest(t, "trend")

	job, created, e := f.s.CreateSavedAnalysisExportJob(ctx, f.owner, f.project, analysisID, version, savedExportInput(f, "export-metric-1", r))
	if e != nil || !created || job.Source == nil || job.Request.Formula != nil {
		t.Fatalf("atomic metric export mismatch: %+v err=%v", job, e)
	}

	// Simulate republished content drift on the immutable pin: export creation
	// must close with METRIC_PIN_CONFLICT rather than rebinding silently.
	tamperPublishedHash(t, f, "metric_versions",
		strings.Repeat("b", 64), f.project, metric.MetricID, metric.Version)
	drift := savedExportInput(f, "export-metric-drift", r)
	if _, _, e = f.s.CreateSavedAnalysisExportJob(ctx, f.owner, f.project, analysisID, version, drift); e == nil || faultPublicCode(e) != "METRIC_PIN_CONFLICT" {
		t.Fatalf("pin drift must fail closed: %v", e)
	}
}

func TestSavedAnalysisExportCompositeMarkerBudgetsAndDrift(t *testing.T) {
	f, formula := publishCompositeFormula(t)
	ctx := context.Background()
	analysisID, version := publishSavedAnalysisForExport(t, f.catalogFixture, compositeBackedDefinition(formula.MetricID), "composite-export-analysis")
	r := queryRequest(t, "trend")

	job, created, e := f.s.CreateSavedAnalysisExportJob(ctx, f.owner, f.project, analysisID, version, savedExportInput(f.catalogFixture, "export-composite-1", r))
	if e != nil || !created {
		t.Fatalf("composite export create: %+v err=%v", job, e)
	}
	if job.Source == nil || job.Source.AnalysisID != analysisID || job.Source.Version != version {
		t.Fatalf("composite export source witness missing: %+v", job.Source)
	}
	if job.Request.Formula == nil || job.Request.Formula.MetricID != formula.MetricID || job.Request.Formula.Version != formula.Version {
		t.Fatalf("composite export must persist the formula marker: %+v", job.Request)
	}
	var raw []byte
	if e = f.s.Pool.QueryRow(ctx, `SELECT request_json FROM export_jobs WHERE id=$1`, job.JobID).Scan(&raw); e != nil {
		t.Fatal(e)
	}
	var marker struct {
		Formula *struct {
			MetricID string `json:"metric_id"`
			Version  int64  `json:"version"`
		} `json:"formula_ref"`
	}
	if e = json.Unmarshal(raw, &marker); e != nil {
		t.Fatal(e)
	}
	if marker.Formula == nil || marker.Formula.MetricID != formula.MetricID {
		t.Fatalf("persisted request_json lost formula_ref: %s", string(raw))
	}
	// The claimed job handed to the export worker keeps the marker so the
	// CompositeFormulaEngine wrapper can expand the DAG at execution time.
	claim, e := f.s.ClaimExportJob(ctx, "export-worker-composite", time.Minute)
	if e != nil || claim == nil || claim.JobID != job.JobID || claim.Request.Formula == nil {
		t.Fatalf("composite claim lost marker: %+v err=%v", claim, e)
	}

	// Composite exports share the run path's 31-day budget and IANA timezone rule.
	overRange := savedExportInput(f.catalogFixture, "export-composite-long", r)
	overRange.TimeRange = analysis.TimeRange{From: time.Now().Add(-32 * 24 * time.Hour), To: time.Now()}
	if _, _, e = f.s.CreateSavedAnalysisExportJob(ctx, f.owner, f.project, analysisID, version, overRange); e == nil || faultPublicCode(e) != "QUERY_COST_EXCEEDED" {
		t.Fatalf("over-budget range must be rejected: %v", e)
	}
	badZone := savedExportInput(f.catalogFixture, "export-composite-zone", r)
	badZone.Timezone = "Not/A/Zone"
	if _, _, e = f.s.CreateSavedAnalysisExportJob(ctx, f.owner, f.project, analysisID, version, badZone); e == nil || faultPublicCode(e) != "INVALID_TIMEZONE" {
		t.Fatalf("bad timezone must be rejected: %v", e)
	}

	// Composite pin drift closes through the formula_metric_versions witness.
	tamperPublishedHash(t, f.catalogFixture, "formula_metric_versions",
		strings.Repeat("c", 64), f.project, formula.MetricID, formula.Version)
	drift := savedExportInput(f.catalogFixture, "export-composite-drift", r)
	if _, _, e = f.s.CreateSavedAnalysisExportJob(ctx, f.owner, f.project, analysisID, version, drift); e == nil || faultPublicCode(e) != "METRIC_PIN_CONFLICT" {
		t.Fatalf("composite pin drift must fail closed: %v", e)
	}
}
