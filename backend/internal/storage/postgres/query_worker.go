package postgres

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/howtomakeaname/Evia/backend/internal/access"
	"github.com/howtomakeaname/Evia/backend/internal/analysis"
	"github.com/howtomakeaname/Evia/backend/internal/platform/fault"
	"github.com/jackc/pgx/v5"
)

var _ analysis.JobStore = (*Store)(nil)

func leaseLost() error {
	return fault.New(fault.Conflict, "QUERY_LEASE_LOST", "查询执行租约已失效")
}
func (s *Store) ClaimQuery(ctx context.Context, worker string, lease time.Duration) (*analysis.Claim, error) {
	if len(worker) < 1 || len(worker) > 128 || lease < time.Second || lease > 2*time.Minute {
		return nil, analysis.Invalid("INVALID_QUERY_WORKER", "执行器租约配置无效")
	}
	tx, e := s.Pool.Begin(ctx)
	if e != nil {
		return nil, e
	}
	defer tx.Rollback(context.Background())
	if _, e = tx.Exec(ctx, `UPDATE analysis_query_jobs SET state='expired',result_json=NULL,finished_at=COALESCE(finished_at,clock_timestamp()),lease_owner=NULL,lease_until=NULL,error_json='{"code":"QUERY_EXPIRED","retryable":false}'::jsonb WHERE id IN (SELECT id FROM analysis_query_jobs WHERE state<>'expired' AND (expires_at<=clock_timestamp() OR (state='queued' AND queued_deadline<=clock_timestamp()) OR (state='succeeded' AND result_expires_at<=clock_timestamp())) LIMIT 1000 FOR UPDATE SKIP LOCKED)`); e != nil {
		return nil, e
	}
	if _, e = tx.Exec(ctx, `DELETE FROM analysis_query_jobs WHERE id IN (SELECT id FROM analysis_query_jobs WHERE state='expired' AND expires_at<clock_timestamp()-interval '1 hour' LIMIT 1000 FOR UPDATE SKIP LOCKED)`); e != nil {
		return nil, e
	}
	if _, e = tx.Exec(ctx, `UPDATE analysis_query_jobs SET state='failed',finished_at=clock_timestamp(),lease_owner=NULL,lease_until=NULL,error_json='{"code":"QUERY_WORKER_RETRY_EXHAUSTED","retryable":false}'::jsonb WHERE id IN (SELECT id FROM analysis_query_jobs WHERE state='running' AND lease_until<=clock_timestamp() AND attempts>=3 LIMIT 1000 FOR UPDATE SKIP LOCKED)`); e != nil {
		return nil, e
	}
	var claim analysis.Claim
	var data []byte
	var kind string
	e = tx.QueryRow(ctx, `SELECT j.id::text,j.actor_id::text,j.project_id::text,j.request,j.kind,COALESCE(j.credential_id::text,''),j.requires_raw FROM analysis_query_jobs j WHERE (j.state='queued' OR (j.state='running' AND j.lease_until<=clock_timestamp() AND j.attempts<3)) AND j.expires_at>clock_timestamp() AND (SELECT count(*) FROM analysis_query_jobs active WHERE active.project_id=j.project_id AND active.state='running' AND active.lease_until>clock_timestamp())<4 ORDER BY j.created_at,j.id LIMIT 1 FOR UPDATE OF j SKIP LOCKED`).Scan(&claim.JobID, &claim.ActorID, &claim.ProjectID, &data, &kind, &claim.CredentialID, &claim.RequiresRaw)
	if errors.Is(e, pgx.ErrNoRows) {
		return nil, tx.Commit(ctx)
	}
	if e != nil {
		return nil, e
	}
	var acquired bool
	if e = tx.QueryRow(ctx, `SELECT pg_try_advisory_xact_lock(hashtextextended($1,7514305))`, claim.ProjectID).Scan(&acquired); e != nil {
		return nil, e
	}
	if !acquired {
		return nil, nil
	}
	var active int
	if e = tx.QueryRow(ctx, `SELECT count(*) FROM analysis_query_jobs WHERE project_id=$1 AND state='running' AND lease_until>clock_timestamp()`, claim.ProjectID).Scan(&active); e != nil {
		return nil, e
	}
	if active >= 4 {
		return nil, nil
	}
	authorized, e := s.queryActorAuthorized(ctx, tx, claim.ActorID, claim.ProjectID, claim.JobID)
	if e != nil {
		return nil, e
	}
	if !authorized {
		if _, e = tx.Exec(ctx, `UPDATE analysis_query_jobs SET state='canceled',finished_at=clock_timestamp(),lease_owner=NULL,lease_until=NULL,error_json='{"code":"ACCESS_REVOKED","retryable":false}'::jsonb WHERE id=$1`, claim.JobID); e != nil {
			return nil, e
		}
		return nil, tx.Commit(ctx)
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	if e = decoder.Decode(&claim.Request); e != nil {
		return nil, e
	}
	claim.Worker = worker
	if e = tx.QueryRow(ctx, `UPDATE analysis_query_jobs SET state='running',lease_owner=$2,lease_until=clock_timestamp()+$3::double precision*interval '1 second',lease_generation=lease_generation+1,attempts=attempts+1 WHERE id=$1 RETURNING lease_generation`, claim.JobID, worker, lease.Seconds()).Scan(&claim.Generation); e != nil {
		return nil, e
	}
	if e = tx.Commit(ctx); e != nil {
		return nil, e
	}
	return &claim, nil
}

// Authorization always reloads the durable credential and scope. A worker's
// incoming context and mutable claim cannot substitute a rotated credential.
func (s *Store) queryActorAuthorized(ctx context.Context, tx pgx.Tx, actor, project, id string) (bool, error) {
	var environment, credential, kind string
	var raw bool
	e := tx.QueryRow(ctx, `SELECT environment,COALESCE(credential_id::text,''),requires_raw,kind FROM analysis_query_jobs WHERE id=$1 AND project_id=$2 AND actor_id=$3`, id, project, actor).Scan(&environment, &credential, &raw, &kind)
	if errors.Is(e, pgx.ErrNoRows) {
		return false, nil
	}
	if e != nil {
		return false, e
	}
	identity := access.Identity{}
	if credential != "" {
		identity = access.Identity{PrincipalID: actor, CredentialID: credential, ProjectID: project, Environment: environment}
	}
	ctx = access.WithIdentity(ctx, identity)
	e = s.authorizeQuery(ctx, tx, actor, project, environment, "queries.run", raw || queryDetail(kind))
	if e == nil {
		return true, nil
	}
	var problem *fault.Error
	if errors.As(e, &problem) && (problem.Kind == fault.Forbidden || problem.Kind == fault.Unauthenticated || problem.Kind == fault.NotFound) {
		return false, nil
	}
	return false, e
}
func (s *Store) QueryLeaseValid(ctx context.Context, claim analysis.Claim) (bool, error) {
	tx, e := s.Pool.Begin(ctx)
	if e != nil {
		return false, e
	}
	defer tx.Rollback(context.Background())
	var kind string
	e = tx.QueryRow(ctx, `SELECT kind FROM analysis_query_jobs WHERE id=$1 AND project_id=$2 AND actor_id=$3 AND lease_owner=$4 AND lease_generation=$5 AND state='running' AND lease_until>clock_timestamp() AND expires_at>clock_timestamp()`, claim.JobID, claim.ProjectID, claim.ActorID, claim.Worker, claim.Generation).Scan(&kind)
	if errors.Is(e, pgx.ErrNoRows) {
		return false, nil
	}
	if e != nil {
		return false, e
	}
	authorized, e := s.queryActorAuthorized(ctx, tx, claim.ActorID, claim.ProjectID, claim.JobID)
	if e != nil {
		return false, e
	}
	if !authorized {
		if _, e = tx.Exec(ctx, `UPDATE analysis_query_jobs SET state='canceled',finished_at=clock_timestamp(),lease_owner=NULL,lease_until=NULL,error_json='{"code":"ACCESS_REVOKED","retryable":false}'::jsonb WHERE id=$1 AND state='running' AND lease_generation=$2 AND lease_owner=$3`, claim.JobID, claim.Generation, claim.Worker); e != nil {
			return false, e
		}
	}
	if e = tx.Commit(ctx); e != nil {
		return false, e
	}
	return authorized, nil
}
func (s *Store) CompleteQuery(ctx context.Context, claim analysis.Claim, outcome analysis.Outcome) error {
	if (outcome.Result == nil) == (outcome.Error == nil) {
		return analysis.Invalid("INVALID_QUERY_OUTCOME", "查询执行结果无效")
	}
	var resultJSON, errorJSON []byte
	var e error
	state := "failed"
	if outcome.Result != nil {
		if len(outcome.Result.Rows) > 500 || outcome.Result.QueryID != claim.JobID {
			return analysis.Invalid("INVALID_QUERY_OUTCOME", "查询结果标识或行数无效")
		}
		resultJSON, e = json.Marshal(outcome.Result)
		if e != nil {
			return e
		}
		if len(resultJSON) > 2*1024*1024 {
			return analysis.Invalid("QUERY_RESULT_LIMIT_EXCEEDED", "查询结果超过存储字节预算")
		}
		state = "succeeded"
	} else {
		errorJSON, e = json.Marshal(outcome.Error)
		if e != nil {
			return e
		}
	}
	tx, e := s.Pool.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(context.Background())
	var kind string
	e = tx.QueryRow(ctx, `SELECT kind FROM analysis_query_jobs WHERE id=$1 AND project_id=$2 AND actor_id=$3`, claim.JobID, claim.ProjectID, claim.ActorID).Scan(&kind)
	if e != nil {
		return e
	}
	authorized, e := s.queryActorAuthorized(ctx, tx, claim.ActorID, claim.ProjectID, claim.JobID)
	if e != nil {
		return e
	}
	if !authorized {
		_, e = tx.Exec(ctx, `UPDATE analysis_query_jobs SET state='canceled',finished_at=clock_timestamp(),lease_owner=NULL,lease_until=NULL,error_json='{"code":"ACCESS_REVOKED","retryable":false}'::jsonb WHERE id=$1 AND state='running' AND lease_generation=$2 AND lease_owner=$3`, claim.JobID, claim.Generation, claim.Worker)
		if e != nil {
			return e
		}
		if e = tx.Commit(ctx); e != nil {
			return e
		}
		return leaseLost()
	}
	command, e := tx.Exec(ctx, `UPDATE analysis_query_jobs SET state=$6,result_json=$7,error_json=$8,result_expires_at=CASE WHEN $6='succeeded' THEN clock_timestamp()+interval '1 hour' ELSE NULL END,finished_at=clock_timestamp(),lease_owner=NULL,lease_until=NULL WHERE id=$1 AND project_id=$2 AND actor_id=$3 AND lease_owner=$4 AND lease_generation=$5 AND state='running' AND lease_until>clock_timestamp() AND expires_at>clock_timestamp()`, claim.JobID, claim.ProjectID, claim.ActorID, claim.Worker, claim.Generation, state, resultJSON, errorJSON)
	if e != nil {
		return e
	}
	if command.RowsAffected() != 1 {
		return leaseLost()
	}
	if _, e = tx.Exec(ctx, `INSERT INTO audit_entries(project_id,actor_id,action,resource_key) VALUES($1,$2,'analysis.query_completed',$3)`, claim.ProjectID, claim.ActorID, claim.JobID); e != nil {
		return e
	}
	return tx.Commit(ctx)
}
