package postgres

import (
	"context"
	"encoding/json"
	"time"

	"github.com/howtomakeaname/Evia/backend/internal/analysis"
	"github.com/howtomakeaname/Evia/backend/internal/exporting"
	"github.com/howtomakeaname/Evia/backend/internal/reporting"
)

// bindSavedAnalysisExportRequest mirrors EnqueueSavedAnalysisVersion: a
// composite metric pin produces the internal formula_ref marker request (with
// the same 31-day budget and IANA timezone checks as the run path), while
// inline and atomic-metric snapshots bind their stored query template.
func bindSavedAnalysisExportRequest(snapshot reporting.SavedAnalysisVersion, in exporting.SavedAnalysisExportInput) (analysis.Request, error) {
	if ref := snapshot.Definition.MetricRef; ref != nil && ref.IsComposite() {
		if in.TimeRange.To.Sub(in.TimeRange.From) > 31*24*time.Hour {
			return analysis.Request{}, analysis.Invalid("QUERY_COST_EXCEEDED", "查询时间范围超过预算", "/time_range")
		}
		if _, e := time.LoadLocation(in.Timezone); e != nil || len(in.Timezone) > 64 {
			return analysis.Request{}, analysis.Invalid("INVALID_TIMEZONE", "请选择 IANA 时区", "/timezone")
		}
		return analysis.Request{
			Kind:        "trend",
			Environment: snapshot.Environment,
			TimeRange:   in.TimeRange,
			Timezone:    in.Timezone,
			Formula:     &analysis.FormulaJobRef{MetricID: ref.MetricID, Version: ref.Version},
		}, nil
	}
	return snapshot.Definition.Query.Bind(snapshot.Environment, in.TimeRange, in.Timezone)
}

// CreateSavedAnalysisExportJob materializes an immutable saved analysis
// version as an export job. The whole resolution runs in one serialized
// transaction: export authority, the archived fail-closed guard, the metric
// pin recheck (METRIC_PIN_CONFLICT closes on drift) and the shared export job
// idempotency/insert path, so a retry never duplicates a job or rebinds a
// different snapshot.
func (s *Store) CreateSavedAnalysisExportJob(ctx context.Context, actor, project, analysisID string, version int64, in exporting.SavedAnalysisExportInput) (exporting.Job, bool, error) {
	if e := exporting.ValidateSavedAnalysisExportInput(in); e != nil {
		return exporting.Job{}, false, e
	}
	if !exportUUIDPattern.MatchString(analysisID) || version < 1 {
		return exporting.Job{}, false, exportInvalid("保存分析标识或版本无效")
	}
	if e := savedKey(in.IdempotencyKey); e != nil {
		return exporting.Job{}, false, e
	}
	tx, e := s.catalogTx(ctx, actor, project, true)
	if e != nil {
		return exporting.Job{}, false, e
	}
	defer tx.Rollback(context.Background())
	if _, e = tx.Exec(ctx, `SELECT id FROM projects WHERE id=$1 FOR UPDATE`, project); e != nil {
		return exporting.Job{}, false, e
	}
	snapshot, e := readSavedAnalysisVersionTx(ctx, tx, project, analysisID, version)
	if e != nil {
		return exporting.Job{}, false, e
	}
	if e = s.exportAuthority(ctx, tx, actor, project, snapshot.Environment); e != nil {
		return exporting.Job{}, false, e
	}
	if e = assertSavedAnalysisActiveTx(ctx, tx, project, analysisID); e != nil {
		return exporting.Job{}, false, e
	}
	if e = s.verifySavedMetricPinTx(ctx, tx, actor, snapshot); e != nil {
		return exporting.Job{}, false, e
	}
	request, e := bindSavedAnalysisExportRequest(snapshot, in)
	if e != nil {
		return exporting.Job{}, false, e
	}
	// Equal instants in different RFC3339 offsets bind the same request.
	request.TimeRange.From = request.TimeRange.From.UTC()
	request.TimeRange.To = request.TimeRange.To.UTC()
	normalized, e := validateExportInput(exporting.Input{
		ProjectID:      project,
		Environment:    snapshot.Environment,
		ActorID:        actor,
		Name:           in.Name,
		Format:         in.Format,
		Request:        request,
		IdempotencyKey: in.IdempotencyKey,
		RequestHash:    in.RequestHash,
		ArtifactExpiry: in.ArtifactExpiry,
		Source:         &exporting.SavedAnalysisSource{AnalysisID: analysisID, Version: version},
	})
	if e != nil {
		return exporting.Job{}, false, e
	}
	rawRequest, e := json.Marshal(normalized.Request)
	if e != nil {
		return exporting.Job{}, false, e
	}
	return s.createExportJobTx(ctx, tx, normalized, rawRequest)
}
