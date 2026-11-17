package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/howtomakeaname/Evia/backend/internal/analysis"
	"github.com/howtomakeaname/Evia/backend/internal/platform/canonical"
	"github.com/howtomakeaname/Evia/backend/internal/platform/fault"
	"github.com/howtomakeaname/Evia/backend/internal/platform/ids"
	"github.com/howtomakeaname/Evia/backend/internal/reporting"
	"github.com/jackc/pgx/v5"
)

func dashboardRunMissing() error {
	return fault.New(fault.NotFound, "DASHBOARD_RUN_NOT_FOUND", "看板运行不存在或不可见")
}
func dashboardRunExpired() error {
	return fault.New(fault.Conflict, "DASHBOARD_RUN_REQUEST_EXPIRED", "原看板运行已过期或已删除，请使用新的幂等键提交")
}

// The project lock serializes all admission, including each child query's shared
// budget check. Any child failure rolls back the entire fanout and retry marker.
func (s *Store) EnqueueDashboardRun(ctx context.Context, actor, project, id string, version int64, key string, in reporting.DashboardExecute) (reporting.DashboardRun, error) {
	var out reporting.DashboardRun
	tx, e := s.catalogTx(ctx, actor, project, false)
	if e != nil {
		return out, e
	}
	defer tx.Rollback(context.Background())
	if e = s.authorizeReportingResource(ctx, tx, actor, project, id, "dashboard_versions", "dashboard_id", "dashboards.run"); e != nil {
		return out, e
	}
	if e = savedKey(key); e != nil {
		return out, e
	}
	if _, e = tx.Exec(ctx, `SELECT id FROM projects WHERE id=$1 FOR UPDATE`, project); e != nil {
		return out, e
	}
	if e = assertDashboardActiveTx(ctx, tx, project, id); e != nil {
		return out, e
	}
	snapshot, e := readDashboardVersionTx(ctx, tx, project, id, version)
	if e != nil {
		return out, e
	}
	in, e = reporting.NormalizeDashboardExecution(in, snapshot.Definition)
	if e != nil {
		return out, e
	}
	requests, e := s.dashboardRequestsTx(ctx, tx, actor, project, snapshot, in, "queries.run")
	if e != nil {
		return out, e
	}
	hash, e := canonical.Hash(struct {
		DashboardID string                     `json:"dashboard_id"`
		Version     int64                      `json:"version"`
		Input       reporting.DashboardExecute `json:"input"`
	}{id, version, in})
	if e != nil {
		return out, e
	}
	operation := "run:" + id
	var prior struct {
		RunID string `json:"run_id"`
	}
	replay, e := dashboardIdempotencyReplay(ctx, tx, actor, project, snapshot.Environment, operation, key, hash, &prior)
	if e != nil {
		return out, e
	}
	if replay {
		out, e = s.readDashboardRunTx(ctx, tx, actor, project, prior.RunID, "queries.run")
		if e != nil {
			return out, e
		}
		expired := false
		for _, widget := range out.Widgets {
			if widget.Query.State == "expired" {
				expired = true
			}
		}
		if e = tx.Commit(ctx); e != nil {
			return out, e
		}
		if expired {
			return reporting.DashboardRun{}, dashboardRunExpired()
		}
		return out, nil
	}
	jobs := make([]analysis.Job, 0, len(requests))
	var expires time.Time
	for _, request := range requests {
		job, e := s.enqueueQueryTx(ctx, tx, actor, project, request)
		if e != nil {
			return out, e
		}
		jobs = append(jobs, job)
		var deadline time.Time
		if e = tx.QueryRow(ctx, `SELECT expires_at FROM analysis_query_jobs WHERE id=$1`, job.ID).Scan(&deadline); e != nil {
			return out, e
		}
		if expires.IsZero() || deadline.Before(expires) {
			expires = deadline
		}
	}
	filterData, e := json.Marshal(append([]reporting.DashboardWidgetFilter{}, in.WidgetFilters...))
	if e != nil {
		return out, e
	}
	executionData, e := canonical.JSON(in)
	if e != nil {
		return out, e
	}
	runID := ids.New()
	if _, e = tx.Exec(ctx, `INSERT INTO dashboard_runs(project_id,environment,dashboard_id,version,run_id,created_by,time_from,time_to,timezone,widget_filters,execution_input,expires_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)`, project, snapshot.Environment, id, version, runID, actor, in.TimeRange.From, in.TimeRange.To, in.Timezone, filterData, executionData, expires); e != nil {
		return out, e
	}
	for position, widget := range snapshot.Definition.Widgets {
		job := jobs[position]
		if widget.MetricRef != nil {
			if _, e = tx.Exec(ctx, `INSERT INTO dashboard_run_widgets(project_id,environment,dashboard_id,version,run_id,widget_id,metric_id,metric_version,position,query_id,original_query_id,query_created_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$10,$11)`, project, snapshot.Environment, id, version, runID, widget.ID, widget.MetricRef.MetricID, widget.MetricRef.Version, position, job.ID, job.CreatedAt); e != nil {
				return out, e
			}
			continue
		}
		if _, e = tx.Exec(ctx, `INSERT INTO dashboard_run_widgets(project_id,environment,dashboard_id,version,run_id,widget_id,analysis_id,analysis_version,position,query_id,original_query_id,query_created_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$10,$11)`, project, snapshot.Environment, id, version, runID, widget.ID, widget.AnalysisID, widget.AnalysisVersion, position, job.ID, job.CreatedAt); e != nil {
			return out, e
		}
	}
	if _, e = tx.Exec(ctx, `INSERT INTO audit_entries(project_id,actor_id,action,resource_key) VALUES($1,$2,'dashboard.run_created',$3)`, project, actor, runID); e != nil {
		return out, e
	}
	prior.RunID = runID
	if e = dashboardIdempotencyWrite(ctx, tx, actor, project, snapshot.Environment, operation, key, hash, prior); e != nil {
		return out, e
	}
	out, e = s.readDashboardRunTx(ctx, tx, actor, project, runID, "queries.run")
	if e != nil {
		return out, e
	}
	return out, tx.Commit(ctx)
}

type dashboardRunReference struct {
	widget     reporting.DashboardWidgetRun
	queryID    *string
	originalID string
	created    time.Time
}

// Query jobs own results and expiry. References preserve widget identity after
// normal query cleanup, without retaining or copying any result data.
func (s *Store) readDashboardRunTx(ctx context.Context, tx pgx.Tx, actor, project, id, permission string) (reporting.DashboardRun, error) {
	var out reporting.DashboardRun
	var executionData []byte
	e := tx.QueryRow(ctx, `SELECT run_id::text,dashboard_id::text,project_id::text,environment,version,created_by::text,execution_input,created_at,expires_at FROM dashboard_runs WHERE project_id=$1 AND run_id=$2`, project, id).Scan(&out.ID, &out.DashboardID, &out.ProjectID, &out.Environment, &out.Version, &out.CreatedBy, &executionData, &out.CreatedAt, &out.ExpiresAt)
	if errors.Is(e, pgx.ErrNoRows) {
		return out, dashboardRunMissing()
	}
	if e != nil {
		return out, e
	}
	var execution reporting.DashboardExecute
	if e = decodeSaved(executionData, &execution); e != nil {
		return out, e
	}
	out.TimeRange, out.Timezone, out.WidgetFilters = execution.TimeRange, execution.Timezone, execution.WidgetFilters
	snapshot, e := readDashboardVersionTx(ctx, tx, project, out.DashboardID, out.Version)
	if e != nil {
		return out, e
	}
	if _, e = s.dashboardRequestsTx(ctx, tx, actor, project, snapshot, execution, permission); e != nil {
		return out, e
	}

	rows, e := tx.Query(ctx, `SELECT widget_id::text,COALESCE(analysis_id::text,''),analysis_version,COALESCE(metric_id::text,''),metric_version,query_id::text,original_query_id::text,query_created_at FROM dashboard_run_widgets WHERE project_id=$1 AND run_id=$2 ORDER BY position`, project, id)
	if e != nil {
		return out, e
	}
	refs := []dashboardRunReference{}
	for rows.Next() {
		var ref dashboardRunReference
		var analysisID string
		var analysisVersion *int64
		var metricID string
		var metricVersion *int64
		if e = rows.Scan(&ref.widget.WidgetID, &analysisID, &analysisVersion, &metricID, &metricVersion, &ref.queryID, &ref.originalID, &ref.created); e != nil {
			rows.Close()
			return out, e
		}
		if analysisVersion != nil {
			ref.widget.AnalysisID = analysisID
			ref.widget.AnalysisVersion = *analysisVersion
		}
		if metricID != "" && metricVersion != nil {
			ref.widget.MetricRef = &reporting.MetricReference{MetricID: metricID, Version: *metricVersion}
		}
		refs = append(refs, ref)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return out, e
	}
	out.Widgets = make([]reporting.DashboardWidgetRun, 0, len(refs))
	for _, ref := range refs {
		ref.widget.Query = analysis.Job{ID: ref.originalID, State: "expired", CreatedAt: ref.created, Error: &analysis.JobError{Code: "QUERY_EXPIRED", Retryable: false}}
		if ref.queryID != nil {
			var raw bool
			var kind string
			e = tx.QueryRow(ctx, `SELECT requires_raw,kind FROM analysis_query_jobs WHERE id=$1 AND project_id=$2 AND environment=$3`, *ref.queryID, project, out.Environment).Scan(&raw, &kind)
			if e != nil && !errors.Is(e, pgx.ErrNoRows) {
				return out, e
			}
			if e == nil {
				if e = s.authorizeQuery(ctx, tx, actor, project, out.Environment, permission, raw || queryDetail(kind)); e != nil {
					return out, e
				}
			}
			if e = expireQuery(ctx, tx, *ref.queryID); e != nil {
				return out, e
			}
			job, e := scanQuery(tx.QueryRow(ctx, `SELECT `+queryJobColumns+` FROM analysis_query_jobs WHERE project_id=$1 AND environment=$2 AND id=$3 FOR UPDATE`, project, out.Environment, *ref.queryID), project)
			if e != nil {
				var problem *fault.Error
				if !errors.As(e, &problem) || problem.Code != "QUERY_NOT_FOUND" {
					return out, e
				}
			} else {
				ref.widget.Query = job
				var deadline time.Time
				if e = tx.QueryRow(ctx, `SELECT LEAST(expires_at,result_expires_at) FROM analysis_query_jobs WHERE id=$1`, *ref.queryID).Scan(&deadline); e != nil {
					return out, e
				}
				if deadline.Before(out.ExpiresAt) {
					out.ExpiresAt = deadline
				}
			}
		}
		out.Widgets = append(out.Widgets, ref.widget)
	}
	out.State = reporting.DashboardRunState(out.Widgets)
	if out.State == "queued" || out.State == "running" {
		delay := 1000
		out.PollAfterMS = &delay
	}
	return out, nil
}

func (s *Store) GetDashboardRun(ctx context.Context, actor, project, id string) (reporting.DashboardRun, error) {
	tx, e := s.catalogTx(ctx, actor, project, false)
	if e != nil {
		return reporting.DashboardRun{}, e
	}
	defer tx.Rollback(context.Background())
	if e = s.authorizeReportingResource(ctx, tx, actor, project, id, "dashboard_runs", "run_id", "dashboards.read"); e != nil {
		return reporting.DashboardRun{}, e
	}
	out, e := s.readDashboardRunTx(ctx, tx, actor, project, id, "queries.read")
	if e != nil {
		return out, e
	}
	return out, tx.Commit(ctx)
}
func (s *Store) CancelDashboardRun(ctx context.Context, actor, project, id string) (reporting.DashboardRun, error) {
	var out reporting.DashboardRun
	tx, e := s.catalogTx(ctx, actor, project, false)
	if e != nil {
		return out, e
	}
	defer tx.Rollback(context.Background())
	if e = s.authorizeReportingResource(ctx, tx, actor, project, id, "dashboard_runs", "run_id", "dashboards.run"); e != nil {
		return out, e
	}
	out, e = s.readDashboardRunTx(ctx, tx, actor, project, id, "queries.run")
	if e != nil {
		return out, e
	}
	var permitted bool
	if e = tx.QueryRow(ctx, `SELECT (r.created_by=$3 OR EXISTS(SELECT 1 FROM project_members m WHERE m.project_id=r.project_id AND m.user_id=$3 AND m.role IN ('owner','editor'))) FROM dashboard_runs r WHERE r.project_id=$1 AND r.run_id=$2`, project, id, actor).Scan(&permitted); e != nil {
		return out, e
	}
	if !permitted {
		return out, fault.New(fault.Forbidden, "DASHBOARD_RUN_CANCEL_FORBIDDEN", "只能取消自己的看板运行")
	}
	changed := false
	for _, widget := range out.Widgets {
		command, e := tx.Exec(ctx, `UPDATE analysis_query_jobs SET state='canceled',finished_at=clock_timestamp(),lease_owner=NULL,lease_until=NULL,error_json='{"code":"QUERY_CANCELED","retryable":false}'::jsonb WHERE project_id=$1 AND id=$2 AND state IN ('queued','running')`, project, widget.Query.ID)
		if e != nil {
			return out, e
		}
		if command.RowsAffected() > 0 {
			changed = true
			if _, e = tx.Exec(ctx, `INSERT INTO audit_entries(project_id,actor_id,action,resource_key) VALUES($1,$2,'analysis.query_canceled',$3)`, project, actor, widget.Query.ID); e != nil {
				return out, e
			}
		}
	}
	if changed {
		if _, e = tx.Exec(ctx, `INSERT INTO audit_entries(project_id,actor_id,action,resource_key) VALUES($1,$2,'dashboard.run_canceled',$3)`, project, actor, id); e != nil {
			return out, e
		}
	}
	out, e = s.readDashboardRunTx(ctx, tx, actor, project, id, "queries.run")
	if e != nil {
		return out, e
	}
	return out, tx.Commit(ctx)
}

// Rebind immutable widget snapshots on reads too, so expired/deleted child jobs
// cannot bypass raw access checks for identity-bearing execution filters.
func (s *Store) dashboardRequestsTx(ctx context.Context, tx pgx.Tx, actor, project string, snapshot reporting.DashboardVersion, in reporting.DashboardExecute, permission string) ([]analysis.Request, error) {
	filters := map[string]*analysis.Filter{}
	for _, filter := range in.WidgetFilters {
		filters[filter.WidgetID] = filter.Filter
	}
	requests := make([]analysis.Request, 0, len(snapshot.Definition.Widgets))
	for _, widget := range snapshot.Definition.Widgets {
		var request analysis.Request
		if widget.MetricRef != nil {
			mv, e := readMetricVersionTx(ctx, tx, project, widget.MetricRef.MetricID, widget.MetricRef.Version)
			if e != nil {
				return nil, e
			}
			if mv.Environment != snapshot.Environment {
				return nil, analysis.Invalid("DASHBOARD_METRIC_ENVIRONMENT_MISMATCH", "看板组件指标版本不属于当前环境")
			}
			execute := reporting.MetricExecute{
				TimeRange: in.TimeRange,
				Timezone:  in.Timezone,
				Filter:    filters[widget.ID],
			}
			template, e := reporting.MetricTemplate(mv.Definition, execute)
			if e != nil {
				return nil, e
			}
			request, e = template.Bind(snapshot.Environment, in.TimeRange, in.Timezone)
			if e != nil {
				return nil, e
			}
		} else {
			v, e := readSavedAnalysisVersionTx(ctx, tx, project, widget.AnalysisID, widget.AnalysisVersion)
			if e != nil {
				return nil, e
			}
			if v.Environment != snapshot.Environment {
				return nil, analysis.Invalid("DASHBOARD_ANALYSIS_ENVIRONMENT_MISMATCH", "看板组件分析版本不属于当前环境")
			}
			request, e = reporting.BindDashboardWidget(v, filters[widget.ID], in)
			if e != nil {
				return nil, e
			}
		}
		if e := s.authorizeQuery(ctx, tx, actor, project, request.Environment, permission, analysis.RequiresRawAccess(request)); e != nil {
			return nil, e
		}
		requests = append(requests, request)
	}
	return requests, nil
}
