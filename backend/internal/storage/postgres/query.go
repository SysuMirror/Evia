package postgres

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/howtomakeaname/Evia/backend/internal/analysis"
	"github.com/howtomakeaname/Evia/backend/internal/platform/fault"
	"github.com/howtomakeaname/Evia/backend/internal/platform/ids"
	"github.com/jackc/pgx/v5"
)

var _ analysis.Store = (*Store)(nil)

func queryMissing() error {
	return fault.New(fault.NotFound, "QUERY_NOT_FOUND", "查询不存在或不可见")
}
func queryDetail(kind string) bool { return kind == "events" || kind == "journey" }

const queryJobColumns = `id::text,state,created_at,finished_at,error_json`

type queryScanner interface{ Scan(...any) error }

func scanQuery(row queryScanner, project string) (analysis.Job, error) {
	var job analysis.Job
	var errorJSON []byte
	e := row.Scan(&job.ID, &job.State, &job.CreatedAt, &job.FinishedAt, &errorJSON)
	if errors.Is(e, pgx.ErrNoRows) {
		return job, queryMissing()
	}
	if e != nil {
		return job, e
	}
	if len(errorJSON) > 0 {
		job.Error = &analysis.JobError{}
		if e = json.Unmarshal(errorJSON, job.Error); e != nil {
			return job, e
		}
	}
	if job.State == "queued" || job.State == "running" {
		delay := 1000
		job.PollAfterMS = &delay
	}
	if job.State == "succeeded" {
		job.ResultURL = "/api/v1/projects/" + project + "/queries/" + job.ID + "/result"
	}
	return job, nil
}
func (s *Store) EnqueueQuery(ctx context.Context, actor, project string, request analysis.Request) (analysis.Job, error) {
	tx, e := s.catalogTx(ctx, actor, project, queryDetail(request.Kind))
	if e != nil {
		return analysis.Job{}, e
	}
	defer tx.Rollback(context.Background())
	job, e := enqueueQueryTx(ctx, tx, actor, project, request)
	if e != nil {
		return analysis.Job{}, e
	}
	if e = tx.Commit(ctx); e != nil {
		return analysis.Job{}, e
	}
	return job, nil
}

// enqueueQueryTx admits direct and saved queries under the same project lock,
// queue budget, field resolver and audit boundary. The caller owns authorization
// and commits the complete transaction, including any execution provenance.
func enqueueQueryTx(ctx context.Context, tx pgx.Tx, actor, project string, request analysis.Request) (analysis.Job, error) {
	if _, e := tx.Exec(ctx, `SELECT id FROM projects WHERE id=$1 FOR UPDATE`, project); e != nil {
		return analysis.Job{}, e
	}
	var queued int
	if e := tx.QueryRow(ctx, `SELECT count(*) FROM analysis_query_jobs WHERE project_id=$1 AND state='queued' AND expires_at>clock_timestamp() AND queued_deadline>clock_timestamp()`, project).Scan(&queued); e != nil {
		return analysis.Job{}, e
	}
	if queued >= 32 {
		return analysis.Job{}, fault.New(fault.Limited, "QUERY_CONCURRENCY_LIMIT", "项目查询队列已满，请稍后重试")
	}
	if e := resolveQueryFields(ctx, tx, project, &request); e != nil {
		return analysis.Job{}, e
	}
	data, e := json.Marshal(request)
	if e != nil {
		return analysis.Job{}, e
	}
	id := ids.New()
	job, e := scanQuery(tx.QueryRow(ctx, `INSERT INTO analysis_query_jobs(id,project_id,environment,actor_id,kind,request) VALUES($1,$2,$3,$4,$5,$6) RETURNING `+queryJobColumns, id, project, request.Environment, actor, request.Kind, data), project)
	if e != nil {
		return analysis.Job{}, e
	}
	if _, e = tx.Exec(ctx, `INSERT INTO audit_entries(project_id,actor_id,action,resource_key) VALUES($1,$2,'analysis.query_created',$3)`, project, actor, id); e != nil {
		return analysis.Job{}, e
	}
	return job, nil
}
func resolveQueryFields(ctx context.Context, tx pgx.Tx, project string, request *analysis.Request) error {
	cache := map[string]map[string]any{}
	load := func(event string, version int64) (map[string]any, error) {
		key := fmt.Sprintf("%s:%d", event, version)
		if v, ok := cache[key]; ok {
			return v, nil
		}
		var data []byte
		e := tx.QueryRow(ctx, `SELECT v.definition->'schema' FROM catalog_versions v WHERE v.project_id=$1 AND v.event_key=$2 AND v.schema_version=$3 AND EXISTS(SELECT 1 FROM catalog_release_events re WHERE re.project_id=v.project_id AND re.environment=$4 AND re.event_key=v.event_key AND re.schema_version=v.schema_version)`, project, event, version, request.Environment).Scan(&data)
		if errors.Is(e, pgx.ErrNoRows) {
			return nil, analysis.Invalid("QUERY_EVENT_VERSION_NOT_PUBLISHED", "事件版本不存在或尚未发布")
		}
		if e != nil {
			return nil, e
		}
		var schema map[string]any
		if e = json.Unmarshal(data, &schema); e != nil {
			return nil, e
		}
		cache[key] = schema
		return schema, nil
	}
	for _, version := range request.EventVersions {
		if _, e := load(version.EventKey, version.SchemaVersion); e != nil {
			return e
		}
	}
	if request.Funnel != nil {
		for _, step := range request.Funnel.Steps {
			if _, e := load(step.EventKey, step.SchemaVersion); e != nil {
				return e
			}
		}
	}
	if e := analysis.WalkFields(request, func(field *analysis.Field) error {
		if field.Kind == "system" {
			return nil
		}
		schema, e := load(field.EventKey, field.SchemaVersion)
		if e != nil {
			return e
		}
		properties, _ := schema["properties"].(map[string]any)
		definition, ok := properties[field.Key].(map[string]any)
		if !ok {
			return analysis.Invalid("QUERY_PROPERTY_NOT_DEFINED", "属性不属于指定事件版本")
		}
		kind, _ := definition["type"].(string)
		if kind == "array" {
			items, _ := definition["items"].(map[string]any)
			item, _ := items["type"].(string)
			kind = "array:" + item
		}
		field.ResolvedType = kind
		return nil
	}); e != nil {
		return e
	}
	return analysis.CheckResolved(*request)
}
func expireQuery(ctx context.Context, tx pgx.Tx, id string) error {
	_, e := tx.Exec(ctx, `UPDATE analysis_query_jobs SET state='expired',result_json=NULL,finished_at=COALESCE(finished_at,clock_timestamp()),lease_owner=NULL,lease_until=NULL,error_json='{"code":"QUERY_EXPIRED","retryable":false}'::jsonb WHERE id=$1 AND state<>'expired' AND (expires_at<=clock_timestamp() OR (state='queued' AND queued_deadline<=clock_timestamp()) OR (state='succeeded' AND result_expires_at<=clock_timestamp()))`, id)
	return e
}
func (s *Store) queryReadTx(ctx context.Context, actor, project, id string) (pgx.Tx, string, error) {
	tx, e := s.catalogTx(ctx, actor, project, false)
	if e != nil {
		return nil, "", e
	}
	var kind string
	e = tx.QueryRow(ctx, `SELECT kind FROM analysis_query_jobs WHERE id=$1 AND project_id=$2`, id, project).Scan(&kind)
	if errors.Is(e, pgx.ErrNoRows) {
		e = queryMissing()
	}
	if e == nil && queryDetail(kind) {
		var role string
		e = tx.QueryRow(ctx, `SELECT role FROM project_members WHERE project_id=$1 AND user_id=$2`, project, actor).Scan(&role)
		if e == nil && role != "owner" && role != "editor" {
			e = fault.New(fault.Forbidden, "QUERY_DETAIL_FORBIDDEN", "当前角色不能查看用户事件明细")
		}
	}
	if e == nil {
		e = expireQuery(ctx, tx, id)
	}
	if e != nil {
		tx.Rollback(context.Background())
		return nil, "", e
	}
	return tx, kind, nil
}
func (s *Store) GetQuery(ctx context.Context, actor, project, id string) (analysis.Job, error) {
	tx, _, e := s.queryReadTx(ctx, actor, project, id)
	if e != nil {
		return analysis.Job{}, e
	}
	defer tx.Rollback(context.Background())
	job, e := scanQuery(tx.QueryRow(ctx, `SELECT `+queryJobColumns+` FROM analysis_query_jobs WHERE id=$1 AND project_id=$2`, id, project), project)
	if e != nil {
		return job, e
	}
	return job, tx.Commit(ctx)
}
func (s *Store) CancelQuery(ctx context.Context, actor, project, id string) (analysis.Job, error) {
	tx, _, e := s.queryReadTx(ctx, actor, project, id)
	if e != nil {
		return analysis.Job{}, e
	}
	defer tx.Rollback(context.Background())
	var permitted bool
	if e = tx.QueryRow(ctx, `SELECT (j.actor_id=$3 OR m.role IN ('owner','editor')) FROM analysis_query_jobs j JOIN project_members m ON m.project_id=j.project_id AND m.user_id=$3 WHERE j.id=$1 AND j.project_id=$2 FOR UPDATE OF j`, id, project, actor).Scan(&permitted); e != nil {
		return analysis.Job{}, e
	}
	if !permitted {
		return analysis.Job{}, fault.New(fault.Forbidden, "QUERY_CANCEL_FORBIDDEN", "只能取消自己的查询")
	}
	command, e := tx.Exec(ctx, `UPDATE analysis_query_jobs SET state='canceled',finished_at=clock_timestamp(),lease_owner=NULL,lease_until=NULL,error_json='{"code":"QUERY_CANCELED","retryable":false}'::jsonb WHERE id=$1 AND state IN ('queued','running')`, id)
	if e != nil {
		return analysis.Job{}, e
	}
	if command.RowsAffected() > 0 {
		if _, e = tx.Exec(ctx, `INSERT INTO audit_entries(project_id,actor_id,action,resource_key) VALUES($1,$2,'analysis.query_canceled',$3)`, project, actor, id); e != nil {
			return analysis.Job{}, e
		}
	}
	job, e := scanQuery(tx.QueryRow(ctx, `SELECT `+queryJobColumns+` FROM analysis_query_jobs WHERE id=$1`, id), project)
	if e != nil {
		return job, e
	}
	return job, tx.Commit(ctx)
}
func (s *Store) QueryResult(ctx context.Context, actor, project, id string) (analysis.Result, error) {
	tx, _, e := s.queryReadTx(ctx, actor, project, id)
	if e != nil {
		return analysis.Result{}, e
	}
	defer tx.Rollback(context.Background())
	var state string
	var data []byte
	if e = tx.QueryRow(ctx, `SELECT state,result_json FROM analysis_query_jobs WHERE id=$1 AND project_id=$2`, id, project).Scan(&state, &data); e != nil {
		return analysis.Result{}, e
	}
	if state != "succeeded" {
		if e = tx.Commit(ctx); e != nil {
			return analysis.Result{}, e
		}
		code := "QUERY_RESULT_NOT_READY"
		if state == "expired" {
			code = "QUERY_EXPIRED"
		}
		if state == "failed" || state == "canceled" {
			code = "QUERY_RESULT_UNAVAILABLE"
		}
		return analysis.Result{}, fault.New(fault.Conflict, code, "查询尚无可读取的结果")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	var result analysis.Result
	if e = decoder.Decode(&result); e != nil {
		return result, e
	}
	if _, e = tx.Exec(ctx, `INSERT INTO audit_entries(project_id,actor_id,action,resource_key) VALUES($1,$2,'analysis.query_result_read',$3)`, project, actor, id); e != nil {
		return result, e
	}
	return result, tx.Commit(ctx)
}
