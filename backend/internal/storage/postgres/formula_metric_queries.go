package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"time"

	"github.com/howtomakeaname/Evia/backend/internal/access"
	"github.com/howtomakeaname/Evia/backend/internal/analysis"
	"github.com/howtomakeaname/Evia/backend/internal/platform/canonical"
	"github.com/howtomakeaname/Evia/backend/internal/platform/fault"
	"github.com/howtomakeaname/Evia/backend/internal/platform/ids"
	"github.com/howtomakeaname/Evia/backend/internal/reporting"
	"github.com/jackc/pgx/v5"
)

// EnqueueFormulaMetricVersion pins an immutable composite metric version to a
// durable trend job carrying an internal formula marker. The shared query queue
// and project lock still bound admission; leaf resolution happens in the worker.
var _ reporting.FormulaExecutionLoader = (*Store)(nil)

func (s *Store) EnqueueFormulaMetricVersion(ctx context.Context, actor, project, id string, version int64, key string, in reporting.MetricExecute) (analysis.Job, error) {
	tx, e := s.catalogTx(ctx, actor, project, false)
	if e != nil {
		return analysis.Job{}, e
	}
	defer tx.Rollback(context.Background())
	if e = s.authorizeFormulaResource(ctx, tx, actor, project, id, "formula_metric_versions", "metric_id", "metrics.run"); e != nil {
		return analysis.Job{}, e
	}
	if e = metricKey(key); e != nil {
		return analysis.Job{}, e
	}
	// Same runtime window and shape boundaries as an atomic metric run.
	if in.TimeRange.From.IsZero() || in.TimeRange.To.IsZero() || !in.TimeRange.From.Before(in.TimeRange.To) || in.TimeRange.From.Year() < 1970 || in.TimeRange.To.Year() > 2100 {
		return analysis.Job{}, analysis.Invalid("INVALID_TIME_RANGE", "时间范围必须为有效的非空半开区间", "/time_range")
	}
	if in.TimeRange.To.Sub(in.TimeRange.From) > 31*24*time.Hour {
		return analysis.Job{}, analysis.Invalid("QUERY_COST_EXCEEDED", "公式查询时间范围超过预算", "/time_range")
	}
	if _, e = time.LoadLocation(in.Timezone); e != nil || len(in.Timezone) > 64 {
		return analysis.Job{}, analysis.Invalid("INVALID_TIMEZONE", "请选择 IANA 时区", "/timezone")
	}
	if in.Interval != "" && in.Interval != "minute" && in.Interval != "hour" && in.Interval != "day" {
		return analysis.Job{}, analysis.Invalid("INVALID_INTERVAL", "请选择时间粒度", "/interval")
	}
	if len(in.GroupBy) > 3 {
		return analysis.Job{}, analysis.Invalid("QUERY_COST_EXCEEDED", "公式查询分组维度超过预算", "/group_by")
	}
	if in.Limit != 0 && (in.Limit < 1 || in.Limit > 500) {
		return analysis.Job{}, analysis.Invalid("QUERY_RESULT_LIMIT_EXCEEDED", "结果行数超过预算", "/limit")
	}
	// Match the atomic run ordering: project lock, archive guard, then the
	// immutable version read, so an archived parent cannot admit new runs.
	if _, e = tx.Exec(ctx, `SELECT id FROM projects WHERE id=$1 FOR UPDATE`, project); e != nil {
		return analysis.Job{}, e
	}
	if e = assertFormulaMetricActiveTx(ctx, tx, project, id); e != nil {
		return analysis.Job{}, e
	}
	snapshot, e := readFormulaMetricVersionTx(ctx, tx, project, id, version)
	if e != nil {
		return analysis.Job{}, e
	}
	if e = s.authorizeQuery(ctx, tx, actor, project, snapshot.Environment, "queries.run", false); e != nil {
		return analysis.Job{}, e
	}
	var queued int
	if e = tx.QueryRow(ctx, `SELECT count(*) FROM analysis_query_jobs WHERE project_id=$1 AND state='queued' AND expires_at>clock_timestamp() AND queued_deadline>clock_timestamp()`, project).Scan(&queued); e != nil {
		return analysis.Job{}, e
	}
	if queued >= 32 {
		return analysis.Job{}, fault.New(fault.Limited, "QUERY_CONCURRENCY_LIMIT", "项目查询队列已满，请稍后重试")
	}
	request := analysis.Request{
		Kind:        "trend",
		Environment: snapshot.Environment,
		TimeRange:   in.TimeRange,
		Timezone:    in.Timezone,
		Interval:    in.Interval,
		GroupBy:     in.GroupBy,
		Filter:      in.Filter,
		Limit:       in.Limit,
		Formula:     &analysis.FormulaJobRef{MetricID: id, Version: version},
	}
	hash, e := canonical.Hash(struct {
		MetricID string           `json:"metric_id"`
		Version  int64            `json:"version"`
		Request  analysis.Request `json:"request"`
	}{id, version, request})
	if e != nil {
		return analysis.Job{}, e
	}
	var previousHash string
	var queryID *string
	var active bool
	e = tx.QueryRow(ctx, `SELECT request_hash,query_id,expires_at>clock_timestamp() FROM formula_metric_query_requests WHERE actor_id=$1 AND project_id=$2 AND environment=$3 AND metric_id=$4 AND idempotency_key=$5`, actor, project, snapshot.Environment, id, key).Scan(&previousHash, &queryID, &active)
	if e != nil && !errors.Is(e, pgx.ErrNoRows) {
		return analysis.Job{}, e
	}
	if e == nil && active {
		if previousHash != hash {
			return analysis.Job{}, fault.New(fault.Conflict, "IDEMPOTENCY_CONFLICT", "幂等键已用于不同请求")
		}
		if queryID == nil {
			return analysis.Job{}, metricQueryExpired()
		}
		if e = expireQuery(ctx, tx, *queryID); e != nil {
			return analysis.Job{}, e
		}
		job, e := scanQuery(tx.QueryRow(ctx, `SELECT `+queryJobColumns+` FROM analysis_query_jobs WHERE id=$1 AND project_id=$2 AND environment=$3 FOR UPDATE`, *queryID, project, snapshot.Environment), project)
		if e != nil {
			var problem *fault.Error
			if errors.As(e, &problem) && problem.Code == "QUERY_NOT_FOUND" {
				return analysis.Job{}, metricQueryExpired()
			}
			return analysis.Job{}, e
		}
		if e = tx.Commit(ctx); e != nil {
			return analysis.Job{}, e
		}
		if job.State == "expired" {
			return analysis.Job{}, metricQueryExpired()
		}
		return job, nil
	}
	if _, e = tx.Exec(ctx, `DELETE FROM formula_metric_query_requests WHERE actor_id=$1 AND project_id=$2 AND environment=$3 AND metric_id=$4 AND idempotency_key=$5 AND expires_at<=clock_timestamp()`, actor, project, snapshot.Environment, id, key); e != nil {
		return analysis.Job{}, e
	}
	if _, e = tx.Exec(ctx, `DELETE FROM formula_metric_query_requests WHERE ctid IN (SELECT ctid FROM formula_metric_query_requests WHERE project_id=$1 AND expires_at<=clock_timestamp() ORDER BY expires_at LIMIT 1000)`, project); e != nil {
		return analysis.Job{}, e
	}
	data, e := json.Marshal(request)
	if e != nil {
		return analysis.Job{}, e
	}
	identity, _ := access.IdentityFromContext(ctx)
	var credentialID any
	if identity.CredentialID != "" {
		credentialID = identity.CredentialID
	}
	jobID := ids.New()
	job, e := scanQuery(tx.QueryRow(ctx, `INSERT INTO analysis_query_jobs(id,project_id,environment,actor_id,kind,request,credential_id,requires_raw) VALUES($1,$2,$3,$4,$5,$6,$7,$8) RETURNING `+queryJobColumns, jobID, project, snapshot.Environment, actor, "trend", data, credentialID, false), project)
	if e != nil {
		return analysis.Job{}, e
	}
	if _, e = tx.Exec(ctx, `INSERT INTO formula_metric_query_requests(actor_id,project_id,environment,metric_id,version,idempotency_key,request_hash,query_id) VALUES($1,$2,$3,$4,$5,$6,$7,$8)`, actor, project, snapshot.Environment, id, version, key, hash, job.ID); e != nil {
		return analysis.Job{}, e
	}
	if _, e = tx.Exec(ctx, `INSERT INTO audit_entries(project_id,actor_id,action,resource_key) VALUES($1,$2,'formula_metric.query_enqueued',$3)`, project, actor, job.ID); e != nil {
		return analysis.Job{}, e
	}
	return job, tx.Commit(ctx)
}

// LoadFormulaExecution resolves a formula job into a fully expanded execution
// plan: every nested composite definition and one executable trend request per
// distinct atomic leaf. It runs in the worker's context and never resolves an
// implicit latest version; the publish-time DAG bounds keep recursion finite.
func (s *Store) LoadFormulaExecution(ctx context.Context, scope analysis.Scope, ref reporting.MetricReference, execute reporting.MetricExecute) (reporting.FormulaExecutionPlan, error) {
	tx, e := s.Pool.Begin(ctx)
	if e != nil {
		return reporting.FormulaExecutionPlan{}, e
	}
	defer tx.Rollback(context.Background())
	root, e := readFormulaMetricVersionTx(ctx, tx, scope.ProjectID, ref.MetricID, ref.Version)
	if e != nil {
		return reporting.FormulaExecutionPlan{}, e
	}
	if root.Environment != scope.Environment {
		return reporting.FormulaExecutionPlan{}, analysis.Invalid("FORMULA_SCOPE_MISMATCH", "复合指标与执行环境不一致", "/formula")
	}
	plan := reporting.FormulaExecutionPlan{
		Root:           ref,
		Composite:      map[reporting.MetricReference]reporting.FormulaDefinition{},
		AtomicRequests: map[reporting.MetricReference]analysis.Request{},
	}
	visited := map[string]bool{}
	var expand func(node reporting.MetricReference) error
	expand = func(node reporting.MetricReference) error {
		key := node.MetricID + "#" + strconv.FormatInt(node.Version, 10)
		if visited[key] {
			return nil
		}
		visited[key] = true
		var composite bool
		if e = tx.QueryRow(ctx, `SELECT true FROM formula_metric_versions WHERE project_id=$1 AND metric_id=$2 AND version=$3`, scope.ProjectID, node.MetricID, node.Version).Scan(&composite); e != nil && !errors.Is(e, pgx.ErrNoRows) {
			return e
		}
		if e == nil {
			version, readErr := readFormulaMetricVersionTx(ctx, tx, scope.ProjectID, node.MetricID, node.Version)
			if readErr != nil {
				return readErr
			}
			if version.Environment != scope.Environment {
				return analysis.Invalid("FORMULA_SCOPE_MISMATCH", "复合指标与执行环境不一致", "/formula")
			}
			normalized, e := reporting.NormalizeFormulaMetricDefinition(version.Definition)
			if e != nil {
				return e
			}
			plan.Composite[node] = normalized.Formula
			for _, child := range reporting.FormulaReferences(normalized.Formula.Expression) {
				if e = expand(child); e != nil {
					return e
				}
			}
			return nil
		}
		atomic, e := readMetricVersionTx(ctx, tx, scope.ProjectID, node.MetricID, node.Version)
		if e != nil {
			return e
		}
		if atomic.Environment != scope.Environment {
			return analysis.Invalid("INVALID_METRIC_REFERENCE", "指标引用必须属于同一项目与环境", "/formula")
		}
		template, e := reporting.MetricTemplate(atomic.Definition, execute)
		if e != nil {
			return e
		}
		leafRequest, e := template.Bind(scope.Environment, execute.TimeRange, execute.Timezone)
		if e != nil {
			return e
		}
		plan.AtomicRequests[node] = leafRequest
		return nil
	}
	rootDefinition, e := reporting.NormalizeFormulaMetricDefinition(root.Definition)
	if e != nil {
		return reporting.FormulaExecutionPlan{}, e
	}
	plan.Composite[ref] = rootDefinition.Formula
	visited[ref.MetricID+"#"+strconv.FormatInt(ref.Version, 10)] = true
	for _, child := range reporting.FormulaReferences(rootDefinition.Formula.Expression) {
		if e = expand(child); e != nil {
			return reporting.FormulaExecutionPlan{}, e
		}
	}
	return plan, tx.Commit(ctx)
}
