package postgres

import (
	"context"
	"errors"
	"regexp"
	"strconv"
	"time"

	"github.com/howtomakeaname/Evia/backend/internal/backfilling"
	"github.com/howtomakeaname/Evia/backend/internal/platform/fault"
	"github.com/howtomakeaname/Evia/backend/internal/platform/ids"
	"github.com/jackc/pgx/v5"
)

var _ backfilling.Store = (*Store)(nil)

var backfillUUIDPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

const backfillJobColumns = `id::text,project_id::text,environment,manifest_id::text,name,state,
 idempotency_key,request_hash,error_code,rows_expected,rows_inserted,attempts,lease_generation,
 lease_owner,lease_until,created_at,updated_at,finished_at`

func backfillInvalid(message string) error {
	return fault.New(fault.Invalid, "INVALID_BACKFILL_INPUT", message)
}

func scanBackfillJob(row queryScanner) (backfilling.Job, error) {
	var j backfilling.Job
	var errorCode, leaseOwner *string
	e := row.Scan(&j.JobID, &j.ProjectID, &j.Environment, &j.ManifestID, &j.Name, &j.State,
		&j.IdempotencyKey, &j.RequestHash, &errorCode, &j.RowsExpected, &j.RowsInserted, &j.Attempts,
		&j.LeaseGeneration, &leaseOwner, &j.LeaseUntil, &j.CreatedAt, &j.UpdatedAt, &j.FinishedAt)
	if e != nil {
		return j, e
	}
	j.ErrorCode = errorCode
	j.LeaseOwner = leaseOwner
	return j, nil
}

// validateBackfillInput repeats the domain guard and resolves the manifest so
// rows_expected is pinned at creation and the scope cannot drift at execution.
func validateBackfillInput(in backfilling.Input) error {
	if !backfillUUIDPattern.MatchString(in.ProjectID) || !backfillUUIDPattern.MatchString(in.ActorID) {
		return backfillInvalid("项目或操作主体标识格式不正确")
	}
	if !backfillUUIDPattern.MatchString(in.ManifestID) {
		return backfillInvalid("归档清单标识格式不正确")
	}
	switch in.Environment {
	case "development", "staging", "production":
	default:
		return backfillInvalid("环境标识无效")
	}
	if len(in.Name) < 1 || len(in.Name) > backfilling.MaxNameLength {
		return backfillInvalid("回补名称长度无效")
	}
	if len(in.IdempotencyKey) < 1 || len(in.IdempotencyKey) > 128 {
		return backfillInvalid("幂等键长度无效")
	}
	if len(in.RequestHash) != 64 {
		return backfillInvalid("请求摘要无效")
	}
	return nil
}

func (s *Store) CreateBackfillJob(ctx context.Context, in backfilling.Input) (backfilling.Job, bool, error) {
	if e := validateBackfillInput(in); e != nil {
		return backfilling.Job{}, false, e
	}
	tx, e := s.Pool.Begin(ctx)
	if e != nil {
		return backfilling.Job{}, false, e
	}
	defer tx.Rollback(context.Background())
	if e = memberOwnerTx(ctx, tx, in.ActorID, in.ProjectID); e != nil {
		return backfilling.Job{}, false, e
	}
	// Replay the accepted snapshot on the same actor + idempotency key.
	job, e := scanBackfillJob(tx.QueryRow(ctx, `SELECT `+backfillJobColumns+` FROM backfill_jobs
 WHERE project_id=$1 AND actor_id=$2 AND idempotency_key=$3`,
		in.ProjectID, in.ActorID, in.IdempotencyKey))
	if e == nil {
		return job, false, tx.Commit(ctx)
	}
	if !errors.Is(e, pgx.ErrNoRows) {
		return backfilling.Job{}, false, e
	}
	// The manifest must be materialized and belong to the same project/environment.
	var rowsExpected int64
	e = tx.QueryRow(ctx, `SELECT row_count FROM archive_manifests
 WHERE id=$1 AND project_id=$2 AND environment=$3 AND status='materialized'`,
		in.ManifestID, in.ProjectID, in.Environment).Scan(&rowsExpected)
	if errors.Is(e, pgx.ErrNoRows) {
		return backfilling.Job{}, false, backfillInvalid("归档清单不存在、未物化或作用域不一致")
	}
	if e != nil {
		return backfilling.Job{}, false, e
	}
	jobID := ids.New()
	row := tx.QueryRow(ctx, `INSERT INTO backfill_jobs(
 id,project_id,environment,actor_id,manifest_id,name,state,idempotency_key,request_hash,rows_expected,attempts,lease_generation)
 VALUES($1,$2,$3,$4,$5,$6,'queued',$7,$8,$9,0,0) RETURNING `+backfillJobColumns,
		jobID, in.ProjectID, in.Environment, in.ActorID, in.ManifestID, in.Name,
		in.IdempotencyKey, in.RequestHash, rowsExpected)
	job, e = scanBackfillJob(row)
	if e != nil {
		return backfilling.Job{}, false, e
	}
	return job, true, tx.Commit(ctx)
}

func (s *Store) GetBackfillJob(ctx context.Context, actor, project, jobID string) (backfilling.Job, error) {
	if !backfillUUIDPattern.MatchString(jobID) {
		return backfilling.Job{}, backfillInvalid("回补作业标识格式不正确")
	}
	tx, e := s.Pool.Begin(ctx)
	if e != nil {
		return backfilling.Job{}, e
	}
	defer tx.Rollback(context.Background())
	if e = memberOwnerTx(ctx, tx, actor, project); e != nil {
		return backfilling.Job{}, e
	}
	job, e := scanBackfillJob(tx.QueryRow(ctx,
		`SELECT `+backfillJobColumns+` FROM backfill_jobs WHERE id=$1 AND project_id=$2`, jobID, project))
	if errors.Is(e, pgx.ErrNoRows) {
		return backfilling.Job{}, fault.New(fault.NotFound, "BACKFILL_JOB_NOT_FOUND", "回补作业不存在或不可见")
	}
	if e != nil {
		return backfilling.Job{}, e
	}
	return job, tx.Commit(ctx)
}

func (s *Store) ListBackfillJobs(ctx context.Context, actor, project string, filter backfilling.ListFilter) (backfilling.Page, error) {
	if filter.Limit < 1 || filter.Limit > 100 {
		return backfilling.Page{}, backfillInvalid("分页大小超出范围")
	}
	if filter.After != "" && !backfillUUIDPattern.MatchString(filter.After) {
		return backfilling.Page{}, backfillInvalid("分页游标已失效")
	}
	tx, e := s.Pool.Begin(ctx)
	if e != nil {
		return backfilling.Page{}, e
	}
	defer tx.Rollback(context.Background())
	if e = memberOwnerTx(ctx, tx, actor, project); e != nil {
		return backfilling.Page{}, e
	}
	query := `SELECT ` + backfillJobColumns + ` FROM backfill_jobs WHERE project_id=$1`
	args := []any{project}
	if filter.After != "" {
		query += ` AND (created_at,id)>(SELECT created_at,id FROM backfill_jobs WHERE project_id=$1 AND id=$` +
			strconv.Itoa(len(args)+1) + `)`
		args = append(args, filter.After)
	}
	query += ` ORDER BY created_at,id LIMIT $` + strconv.Itoa(len(args)+1)
	args = append(args, filter.Limit+1)
	rows, e := tx.Query(ctx, query, args...)
	if e != nil {
		return backfilling.Page{}, e
	}
	defer rows.Close()
	out := backfilling.Page{Items: []backfilling.Job{}}
	for rows.Next() {
		job, scanErr := scanBackfillJob(rows)
		if scanErr != nil {
			return backfilling.Page{}, scanErr
		}
		out.Items = append(out.Items, job)
	}
	if e = rows.Err(); e != nil {
		return backfilling.Page{}, e
	}
	if len(out.Items) > filter.Limit {
		out.After = out.Items[filter.Limit-1].JobID
		out.Items = out.Items[:filter.Limit]
	}
	return out, tx.Commit(ctx)
}

func (s *Store) CancelBackfillJob(ctx context.Context, actor, project, jobID string) (backfilling.Job, error) {
	if !backfillUUIDPattern.MatchString(jobID) {
		return backfilling.Job{}, backfillInvalid("回补作业标识格式不正确")
	}
	tx, e := s.Pool.Begin(ctx)
	if e != nil {
		return backfilling.Job{}, e
	}
	defer tx.Rollback(context.Background())
	if e = memberOwnerTx(ctx, tx, actor, project); e != nil {
		return backfilling.Job{}, e
	}
	job, e := scanBackfillJob(tx.QueryRow(ctx,
		`SELECT `+backfillJobColumns+` FROM backfill_jobs WHERE id=$1 AND project_id=$2 FOR UPDATE`, jobID, project))
	if errors.Is(e, pgx.ErrNoRows) {
		return backfilling.Job{}, fault.New(fault.NotFound, "BACKFILL_JOB_NOT_FOUND", "回补作业不存在或不可见")
	}
	if e != nil {
		return backfilling.Job{}, e
	}
	if job.State != backfilling.StateQueued {
		return backfilling.Job{}, fault.New(fault.Conflict, "BACKFILL_NOT_CANCELLABLE", "只有排队中的回补作业可以取消")
	}
	job, e = scanBackfillJob(tx.QueryRow(ctx,
		`UPDATE backfill_jobs SET state='cancelled',updated_at=clock_timestamp(),finished_at=clock_timestamp()
 WHERE id=$1 RETURNING `+backfillJobColumns, jobID))
	if e != nil {
		return backfilling.Job{}, e
	}
	return job, tx.Commit(ctx)
}

// ClaimBackfillJob mirrors the export queue: SKIP LOCKED lease, stale-job
// requeue, and attempts exhausted parks the job as failed.
func (s *Store) ClaimBackfillJob(ctx context.Context, worker string, lease time.Duration) (*backfilling.Claim, error) {
	if len(worker) < 1 || len(worker) > 128 || lease < time.Second || lease > 10*time.Minute {
		return nil, backfillInvalid("执行器租约配置无效")
	}
	tx, e := s.Pool.Begin(ctx)
	if e != nil {
		return nil, e
	}
	defer tx.Rollback(context.Background())
	if _, e = tx.Exec(ctx, `UPDATE backfill_jobs SET state='queued',lease_owner=NULL,lease_until=NULL,
 lease_generation=lease_generation+1,attempts=attempts+1,updated_at=clock_timestamp()
 WHERE state='running' AND lease_until<=clock_timestamp() AND attempts<$1`, backfilling.MaxAttempts); e != nil {
		return nil, e
	}
	if _, e = tx.Exec(ctx, `UPDATE backfill_jobs SET state='failed',error_code='BACKFILL_ATTEMPTS_EXHAUSTED',
 lease_owner=NULL,lease_until=NULL,finished_at=clock_timestamp(),updated_at=clock_timestamp()
 WHERE id IN (SELECT id FROM backfill_jobs WHERE state='running' AND lease_until<=clock_timestamp()
 AND attempts>=$1 LIMIT 100 FOR UPDATE SKIP LOCKED)`, backfilling.MaxAttempts); e != nil {
		return nil, e
	}
	job, e := scanBackfillJob(tx.QueryRow(ctx, `UPDATE backfill_jobs SET state='running',lease_owner=$1,lease_until=clock_timestamp()+$2::double precision*interval '1 second',
 lease_generation=lease_generation+1,attempts=attempts+1,updated_at=clock_timestamp()
 WHERE id=(SELECT id FROM backfill_jobs WHERE state='queued' ORDER BY created_at,id
 FOR UPDATE SKIP LOCKED LIMIT 1)
 RETURNING `+backfillJobColumns, worker, lease.Seconds()))
	if errors.Is(e, pgx.ErrNoRows) {
		return nil, tx.Commit(ctx)
	}
	if e != nil {
		return nil, e
	}
	if e = tx.Commit(ctx); e != nil {
		return nil, e
	}
	return &backfilling.Claim{Job: job, Worker: worker, Generation: job.LeaseGeneration}, nil
}

func (s *Store) CompleteBackfillJob(ctx context.Context, claim backfilling.Claim, rowsInserted int64) error {
	command, e := s.Pool.Exec(ctx, `UPDATE backfill_jobs SET state='completed',rows_inserted=$2,
 finished_at=clock_timestamp(),updated_at=clock_timestamp()
 WHERE id=$1 AND lease_owner=$3 AND lease_generation=$4 AND state='running'`,
		claim.JobID, rowsInserted, claim.Worker, claim.Generation)
	if e != nil {
		return e
	}
	if command.RowsAffected() != 1 {
		return fault.New(fault.Conflict, backfilling.LeaseLostCode, "回补作业租约已失效")
	}
	return nil
}

func (s *Store) FailBackfillJob(ctx context.Context, claim backfilling.Claim, code string) error {
	if len(code) < 1 || len(code) > 128 {
		return backfillInvalid("失败码无效")
	}
	command, e := s.Pool.Exec(ctx, `UPDATE backfill_jobs SET state='failed',error_code=$5,
 finished_at=clock_timestamp(),updated_at=clock_timestamp()
 WHERE id=$1 AND lease_owner=$2 AND lease_generation=$3 AND state='running'`,
		claim.JobID, claim.Worker, claim.Generation, code)
	if e != nil {
		return e
	}
	if command.RowsAffected() != 1 {
		return fault.New(fault.Conflict, backfilling.LeaseLostCode, "回补作业租约已失效")
	}
	return nil
}
