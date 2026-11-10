package postgres

import (
	"context"
	"errors"

	"github.com/howtomakeaname/Evia/backend/internal/analysis"
	"github.com/howtomakeaname/Evia/backend/internal/platform/canonical"
	"github.com/howtomakeaname/Evia/backend/internal/platform/fault"
	"github.com/howtomakeaname/Evia/backend/internal/reporting"
	"github.com/jackc/pgx/v5"
)

func savedQueryExpired() error {
	return fault.New(fault.Conflict, "QUERY_REQUEST_EXPIRED", "原查询已过期或已删除，请使用新的幂等键提交")
}

// verifySavedMetricPinTx re-checks an immutable metric reference immediately
// before running a published saved analysis. It fails closed if the pin is
// missing, the metric version disappeared, its environment differs or its
// digest no longer matches, so republishing a metric can never silently change
// an already published analysis. Inline analyses have no reference and pass.
func (s *Store) verifySavedMetricPinTx(ctx context.Context, tx pgx.Tx, actor string, snapshot reporting.SavedAnalysisVersion) error {
	ref := snapshot.Definition.MetricRef
	if ref == nil {
		if snapshot.Dependencies.Metric != nil {
			return analysis.Invalid("INVALID_ANALYSIS_DEPENDENCIES", "内联分析不能携带指标依赖")
		}
		return nil
	}
	pin := snapshot.Dependencies.Metric
	if pin == nil || pin.MetricID != ref.MetricID || pin.Version != ref.Version {
		return analysis.Invalid("INVALID_ANALYSIS_DEPENDENCIES", "指标固定依赖与引用版本不匹配", "/metric_ref")
	}
	metric, e := readMetricVersionTx(ctx, tx, snapshot.ProjectID, pin.MetricID, pin.Version)
	if e != nil {
		return e
	}
	if metric.Environment != snapshot.Environment {
		return analysis.Invalid("INVALID_METRIC_REFERENCE", "指标引用必须属于同一项目与环境", "/metric_ref")
	}
	if metric.DefinitionHash != pin.DefinitionHash {
		return fault.New(fault.Conflict, "METRIC_PIN_CONFLICT", "指标固定版本内容已变化，无法运行旧分析")
	}
	return s.authorizeOperation(ctx, tx, actor, snapshot.ProjectID, metric.Environment, "metrics.read")
}

// EnqueueSavedAnalysisVersion binds runtime context to an exact immutable
// snapshot. Current authorization, replay, admission, audit and provenance all
// share one transaction; a retry never creates another job or copies results.
func (s *Store) EnqueueSavedAnalysisVersion(ctx context.Context, actor, project, id string, version int64, key string, in reporting.SavedAnalysisExecute) (analysis.Job, error) {
	tx, e := s.catalogTx(ctx, actor, project, false)
	if e != nil {
		return analysis.Job{}, e
	}
	defer tx.Rollback(context.Background())
	if e = s.authorizeReportingResource(ctx, tx, actor, project, id, "saved_analysis_versions", "analysis_id", "analyses.run"); e != nil {
		return analysis.Job{}, e
	}
	if e = savedKey(key); e != nil {
		return analysis.Job{}, e
	}
	if _, e = tx.Exec(ctx, `SELECT id FROM projects WHERE id=$1 FOR UPDATE`, project); e != nil {
		return analysis.Job{}, e
	}
	snapshot, e := readSavedAnalysisVersionTx(ctx, tx, project, id, version)
	if e != nil {
		return analysis.Job{}, e
	}
	if e = s.verifySavedMetricPinTx(ctx, tx, actor, snapshot); e != nil {
		return analysis.Job{}, e
	}
	request, e := snapshot.Definition.Query.Bind(snapshot.Environment, in.TimeRange, in.Timezone)
	if e != nil {
		return analysis.Job{}, e
	}
	if e = s.authorizeQuery(ctx, tx, actor, project, request.Environment, "queries.run", analysis.RequiresRawAccess(request)); e != nil {
		return analysis.Job{}, e
	}
	// Equal instants in different RFC3339 offsets bind the same request.
	request.TimeRange.From = request.TimeRange.From.UTC()
	request.TimeRange.To = request.TimeRange.To.UTC()
	hash, e := canonical.Hash(struct {
		AnalysisID string           `json:"analysis_id"`
		Version    int64            `json:"version"`
		Request    analysis.Request `json:"request"`
	}{id, version, request})
	if e != nil {
		return analysis.Job{}, e
	}
	var previousHash string
	var queryID *string
	var active bool
	e = tx.QueryRow(ctx, `SELECT request_hash,query_id,expires_at>clock_timestamp() FROM saved_analysis_query_requests WHERE actor_id=$1 AND project_id=$2 AND environment=$3 AND analysis_id=$4 AND idempotency_key=$5`, actor, project, snapshot.Environment, id, key).Scan(&previousHash, &queryID, &active)
	if e != nil && !errors.Is(e, pgx.ErrNoRows) {
		return analysis.Job{}, e
	}
	if e == nil && active {
		if previousHash != hash {
			return analysis.Job{}, fault.New(fault.Conflict, "IDEMPOTENCY_CONFLICT", "幂等键已用于不同请求")
		}
		if queryID == nil {
			return analysis.Job{}, savedQueryExpired()
		}
		if e = expireQuery(ctx, tx, *queryID); e != nil {
			return analysis.Job{}, e
		}
		job, e := scanQuery(tx.QueryRow(ctx, `SELECT `+queryJobColumns+` FROM analysis_query_jobs WHERE id=$1 AND project_id=$2 AND environment=$3 FOR UPDATE`, *queryID, project, snapshot.Environment), project)
		if e != nil {
			var problem *fault.Error
			if errors.As(e, &problem) && problem.Code == "QUERY_NOT_FOUND" {
				return analysis.Job{}, savedQueryExpired()
			}
			return analysis.Job{}, e
		}
		if e = tx.Commit(ctx); e != nil {
			return analysis.Job{}, e
		}
		if job.State == "expired" {
			return analysis.Job{}, savedQueryExpired()
		}
		return job, nil
	}
	// Bound maintenance to this serialized project and 1000 indexed rows. Remove
	// this request's expired marker explicitly even if a large backlog precedes it.
	if _, e = tx.Exec(ctx, `DELETE FROM saved_analysis_query_requests WHERE actor_id=$1 AND project_id=$2 AND environment=$3 AND analysis_id=$4 AND idempotency_key=$5 AND expires_at<=clock_timestamp()`, actor, project, snapshot.Environment, id, key); e != nil {
		return analysis.Job{}, e
	}
	if _, e = tx.Exec(ctx, `DELETE FROM saved_analysis_query_requests WHERE ctid IN (SELECT ctid FROM saved_analysis_query_requests WHERE project_id=$1 AND expires_at<=clock_timestamp() ORDER BY expires_at LIMIT 1000)`, project); e != nil {
		return analysis.Job{}, e
	}
	job, e := s.enqueueQueryTx(ctx, tx, actor, project, request)
	if e != nil {
		return analysis.Job{}, e
	}
	if _, e = tx.Exec(ctx, `INSERT INTO saved_analysis_query_requests(actor_id,project_id,environment,analysis_id,version,idempotency_key,request_hash,query_id) VALUES($1,$2,$3,$4,$5,$6,$7,$8)`, actor, project, snapshot.Environment, id, version, key, hash, job.ID); e != nil {
		return analysis.Job{}, e
	}
	if e = tx.Commit(ctx); e != nil {
		return analysis.Job{}, e
	}
	return job, nil
}
