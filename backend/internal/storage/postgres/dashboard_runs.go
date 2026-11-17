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
	var comparisonRange *analysis.TimeRange
	if in.Comparison != nil {
		shifted, e := reporting.ShiftTimeRange(in.TimeRange, in.Timezone, *in.Comparison)
		if e != nil {
			return out, e
		}
		comparisonRange = &shifted
	}
	requests, e := s.dashboardRequestsTx(ctx, tx, actor, project, snapshot, in, comparisonRange, "queries.run")
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
			if widget.Query.State == "expired" ||
				(widget.Comparison != nil && widget.Comparison.State == "expired") {
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
	type widgetJobPair struct {
		current    analysis.Job
		comparison *analysis.Job
	}
	jobs := make([]widgetJobPair, 0, len(requests))
	var expires time.Time
	enqueueOne := func(request analysis.Request) (analysis.Job, error) {
		job, e := s.enqueueQueryTx(ctx, tx, actor, project, request)
		if e != nil {
			return analysis.Job{}, e
		}
		var deadline time.Time
		if e = tx.QueryRow(ctx, `SELECT expires_at FROM analysis_query_jobs WHERE id=$1`, job.ID).Scan(&deadline); e != nil {
			return analysis.Job{}, e
		}
		if expires.IsZero() || deadline.Before(expires) {
			expires = deadline
		}
		return job, nil
	}
	for _, request := range requests {
		job, e := enqueueOne(request.Current)
		if e != nil {
			return out, e
		}
		pair := widgetJobPair{current: job}
		if request.Comparison != nil {
			comparisonJob, e := enqueueOne(*request.Comparison)
			if e != nil {
				return out, e
			}
			pair.comparison = &comparisonJob
		}
		jobs = append(jobs, pair)
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
	insertRunWidget := func(widget reporting.DashboardWidget, position int, period string, job analysis.Job) error {
		if widget.MetricRef != nil {
			_, e = tx.Exec(ctx, `INSERT INTO dashboard_run_widgets(project_id,environment,dashboard_id,version,run_id,widget_id,metric_id,metric_version,position,period,query_id,original_query_id,query_created_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$11,$12)`, project, snapshot.Environment, id, version, runID, widget.ID, widget.MetricRef.MetricID, widget.MetricRef.Version, position, period, job.ID, job.CreatedAt)
			return e
		}
		_, e = tx.Exec(ctx, `INSERT INTO dashboard_run_widgets(project_id,environment,dashboard_id,version,run_id,widget_id,analysis_id,analysis_version,position,period,query_id,original_query_id,query_created_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$11,$12)`, project, snapshot.Environment, id, version, runID, widget.ID, widget.AnalysisID, widget.AnalysisVersion, position, period, job.ID, job.CreatedAt)
		return e
	}
	for position, widget := range snapshot.Definition.Widgets {
		pair := jobs[position]
		if e = insertRunWidget(widget, position, "current", pair.current); e != nil {
			return out, e
		}
		if pair.comparison != nil {
			if e = insertRunWidget(widget, position, "comparison", *pair.comparison); e != nil {
				return out, e
			}
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
	out.TimeRange, out.Timezone, out.WidgetFilters, out.Comparison = execution.TimeRange, execution.Timezone, execution.WidgetFilters, execution.Comparison
	snapshot, e := readDashboardVersionTx(ctx, tx, project, out.DashboardID, out.Version)
	if e != nil {
		return out, e
	}
	var comparisonRange *analysis.TimeRange
	if execution.Comparison != nil {
		shifted, e := reporting.ShiftTimeRange(execution.TimeRange, execution.Timezone, *execution.Comparison)
		if e != nil {
			return out, e
		}
		comparisonRange = &shifted
	}
	if _, e = s.dashboardRequestsTx(ctx, tx, actor, project, snapshot, execution, comparisonRange, permission); e != nil {
		return out, e
	}

	rows, e := tx.Query(ctx, `SELECT widget_id::text,COALESCE(analysis_id::text,''),analysis_version,COALESCE(metric_id::text,''),metric_version,period,query_id::text,original_query_id::text,query_created_at FROM dashboard_run_widgets WHERE project_id=$1 AND run_id=$2 ORDER BY position,period DESC`, project, id)
	if e != nil {
		return out, e
	}
	type runWidgetRow struct {
		widgetID        string
		period          string
		queryID         *string
		originalID      string
		created         time.Time
		analysisID      string
		analysisVersion *int64
		metricID        string
		metricVersion   *int64
		job             analysis.Job
	}
	ordered := []runWidgetRow{}
	for rows.Next() {
		var row runWidgetRow
		if e = rows.Scan(&row.widgetID, &row.analysisID, &row.analysisVersion, &row.metricID, &row.metricVersion, &row.period, &row.queryID, &row.originalID, &row.created); e != nil {
			rows.Close()
			return out, e
		}
		ordered = append(ordered, row)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return out, e
	}
	// First pass: resolve every child job (authorization, expiry, FOR UPDATE).
	for i := range ordered {
		row := &ordered[i]
		row.job = analysis.Job{ID: row.originalID, State: "expired", CreatedAt: row.created, Error: &analysis.JobError{Code: "QUERY_EXPIRED", Retryable: false}}
		if row.queryID == nil {
			continue
		}
		var raw bool
		var kind string
		e = tx.QueryRow(ctx, `SELECT requires_raw,kind FROM analysis_query_jobs WHERE id=$1 AND project_id=$2 AND environment=$3`, *row.queryID, project, out.Environment).Scan(&raw, &kind)
		if e != nil && !errors.Is(e, pgx.ErrNoRows) {
			return out, e
		}
		if e == nil {
			if e = s.authorizeQuery(ctx, tx, actor, project, out.Environment, permission, raw || queryDetail(kind)); e != nil {
				return out, e
			}
		}
		if e = expireQuery(ctx, tx, *row.queryID); e != nil {
			return out, e
		}
		resolved, e := scanQuery(tx.QueryRow(ctx, `SELECT `+queryJobColumns+` FROM analysis_query_jobs WHERE project_id=$1 AND environment=$2 AND id=$3 FOR UPDATE`, project, out.Environment, *row.queryID), project)
		if e != nil {
			var problem *fault.Error
			if !errors.As(e, &problem) || problem.Code != "QUERY_NOT_FOUND" {
				return out, e
			}
		} else {
			row.job = resolved
			var deadline time.Time
			if e = tx.QueryRow(ctx, `SELECT LEAST(expires_at,result_expires_at) FROM analysis_query_jobs WHERE id=$1`, *row.queryID).Scan(&deadline); e != nil {
				return out, e
			}
			if deadline.Before(out.ExpiresAt) {
				out.ExpiresAt = deadline
			}
		}
	}
	// Second pass: current rows establish widget order and identity.
	out.Widgets = make([]reporting.DashboardWidgetRun, 0, len(snapshot.Definition.Widgets))
	for _, row := range ordered {
		if row.period != "current" {
			continue
		}
		widget := reporting.DashboardWidgetRun{WidgetID: row.widgetID, Query: row.job}
		if row.analysisVersion != nil {
			widget.AnalysisID = row.analysisID
			widget.AnalysisVersion = *row.analysisVersion
		}
		if row.metricID != "" && row.metricVersion != nil {
			widget.MetricRef = &reporting.MetricReference{MetricID: row.metricID, Version: *row.metricVersion}
		}
		out.Widgets = append(out.Widgets, widget)
	}
	// Third pass: attach comparison jobs by widget ID regardless of row order.
	for _, row := range ordered {
		if row.period != "comparison" {
			continue
		}
		for i := range out.Widgets {
			if out.Widgets[i].WidgetID == row.widgetID {
				comparisonJob := row.job
				out.Widgets[i].Comparison = &comparisonJob
				break
			}
		}
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
	cancelJob := func(jobID string) error {
		command, e := tx.Exec(ctx, `UPDATE analysis_query_jobs SET state='canceled',finished_at=clock_timestamp(),lease_owner=NULL,lease_until=NULL,error_json='{"code":"QUERY_CANCELED","retryable":false}'::jsonb WHERE project_id=$1 AND id=$2 AND state IN ('queued','running')`, project, jobID)
		if e != nil {
			return e
		}
		if command.RowsAffected() > 0 {
			changed = true
			if _, e = tx.Exec(ctx, `INSERT INTO audit_entries(project_id,actor_id,action,resource_key) VALUES($1,$2,'analysis.query_canceled',$3)`, project, actor, jobID); e != nil {
				return e
			}
		}
		return nil
	}
	for _, widget := range out.Widgets {
		if e = cancelJob(widget.Query.ID); e != nil {
			return out, e
		}
		if widget.Comparison != nil {
			if e = cancelJob(widget.Comparison.ID); e != nil {
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

// dashboardWidgetRequests pairs the current-window query with the optional
// shifted-window comparison query for one widget.
type dashboardWidgetRequests struct {
	Current    analysis.Request
	Comparison *analysis.Request
}

// Rebind immutable widget snapshots on reads too, so expired/deleted child jobs
// cannot bypass raw access checks for identity-bearing execution filters. A
// non-nil comparisonRange produces a second request per widget over the same
// filters and granularity but the DST-shifted window.
func (s *Store) dashboardRequestsTx(ctx context.Context, tx pgx.Tx, actor, project string, snapshot reporting.DashboardVersion, in reporting.DashboardExecute, comparisonRange *analysis.TimeRange, permission string) ([]dashboardWidgetRequests, error) {
	filters := map[string]*analysis.Filter{}
	for _, filter := range in.WidgetFilters {
		filters[filter.WidgetID] = filter.Filter
	}
	requests := make([]dashboardWidgetRequests, 0, len(snapshot.Definition.Widgets))
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
		pair := dashboardWidgetRequests{Current: request}
		if comparisonRange != nil {
			comparison, e := s.dashboardComparisonRequestTx(ctx, tx, actor, project, snapshot, widget, filters[widget.ID], in, *comparisonRange, permission)
			if e != nil {
				return nil, e
			}
			pair.Comparison = &comparison
		}
		requests = append(requests, pair)
	}
	return requests, nil
}

// dashboardComparisonRequestTx rebuilds one widget request against the shifted
// window, reusing the pinned source and every fixed/runtime predicate.
func (s *Store) dashboardComparisonRequestTx(ctx context.Context, tx pgx.Tx, actor, project string, snapshot reporting.DashboardVersion, widget reporting.DashboardWidget, filter *analysis.Filter, in reporting.DashboardExecute, shifted analysis.TimeRange, permission string) (analysis.Request, error) {
	var request analysis.Request
	if widget.MetricRef != nil {
		mv, e := readMetricVersionTx(ctx, tx, project, widget.MetricRef.MetricID, widget.MetricRef.Version)
		if e != nil {
			return analysis.Request{}, e
		}
		execute := reporting.MetricExecute{TimeRange: shifted, Timezone: in.Timezone, Filter: filter}
		template, e := reporting.MetricTemplate(mv.Definition, execute)
		if e != nil {
			return analysis.Request{}, e
		}
		request, e = template.Bind(snapshot.Environment, shifted, in.Timezone)
		if e != nil {
			return analysis.Request{}, e
		}
	} else {
		v, e := readSavedAnalysisVersionTx(ctx, tx, project, widget.AnalysisID, widget.AnalysisVersion)
		if e != nil {
			return analysis.Request{}, e
		}
		comparisonIn := in
		comparisonIn.TimeRange = shifted
		request, e = reporting.BindDashboardWidget(v, filter, comparisonIn)
		if e != nil {
			return analysis.Request{}, e
		}
	}
	if e := s.authorizeQuery(ctx, tx, actor, project, request.Environment, permission, analysis.RequiresRawAccess(request)); e != nil {
		return analysis.Request{}, e
	}
	return request, nil
}
