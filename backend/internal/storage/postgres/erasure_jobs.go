package postgres

import (
	"context"
	"errors"
	"regexp"
	"strconv"
	"time"

	"github.com/howtomakeaname/Evia/backend/internal/access"
	"github.com/howtomakeaname/Evia/backend/internal/erasure"
	"github.com/howtomakeaname/Evia/backend/internal/platform/env"
	"github.com/howtomakeaname/Evia/backend/internal/platform/fault"
	"github.com/howtomakeaname/Evia/backend/internal/platform/ids"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

var _ erasure.Store = (*Store)(nil)

var erasureUUIDPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

const erasureLeaseLost = "ERASURE_LEASE_LOST"

func erasureInvalid(message string) error {
	return fault.New(fault.Invalid, "INVALID_ERASURE_INPUT", message)
}

// erasureAuthority keeps destruction owner-only for human sessions while
// machines need the explicit data.erasure grant reloaded inside the transaction.
func (s *Store) erasureAuthority(ctx context.Context, tx pgx.Tx, actor, project, environment string) error {
	identity, machine := access.IdentityFromContext(ctx)
	if machine {
		if identity.PrincipalID != actor {
			return machineDenied()
		}
		return s.authorizeOperation(ctx, tx, actor, project, environment, access.PermissionDataErasure)
	}
	return memberOwnerTx(ctx, tx, actor, project)
}

// memberOwnerTx mirrors the governance owner rule without exposing membership
// rows to non-owners: non-members get the same invisible-project response.
func memberOwnerTx(ctx context.Context, tx pgx.Tx, actor, project string) error {
	var role string
	e := tx.QueryRow(ctx, `SELECT m.role FROM project_members m JOIN accounts a ON a.id=m.user_id
 WHERE m.project_id=$1 AND m.user_id=$2 AND NOT a.disabled`, project, actor).Scan(&role)
	if errors.Is(e, pgx.ErrNoRows) {
		return projectInvisible()
	}
	if e != nil {
		return e
	}
	if role != "owner" {
		return fault.New(fault.Forbidden, "ERASURE_FORBIDDEN", "只有项目所有者可以发起数据删除")
	}
	return setAuditCredential(ctx, tx, "")
}

func validateErasureInput(in erasure.Input) error {
	if !erasureUUIDPattern.MatchString(in.ProjectID) || !erasureUUIDPattern.MatchString(in.ActorID) {
		return erasureInvalid("项目或操作主体标识格式不正确")
	}
	switch in.Environment {
	case env.Development, env.Staging, env.Production:
	default:
		return erasureInvalid("环境标识无效")
	}
	if in.SubjectKind != erasure.SubjectUID && in.SubjectKind != erasure.SubjectAnonymousID {
		return erasureInvalid("删除对象类型无效")
	}
	if len(in.SubjectValue) < 1 || len(in.SubjectValue) > 128 {
		return erasureInvalid("删除对象标识长度无效")
	}
	if in.SubjectKind == erasure.SubjectAnonymousID && !erasureUUIDPattern.MatchString(in.SubjectValue) {
		return erasureInvalid("匿名标识必须是 UUID")
	}
	if len(in.Reason) < 1 || len(in.Reason) > 1024 || len(in.IdempotencyKey) < 1 || len(in.IdempotencyKey) > 128 {
		return erasureInvalid("删除原因或幂等键长度无效")
	}
	if len(in.RequestHash) != 64 {
		return erasureInvalid("请求摘要无效")
	}
	if in.BackupExpiryAt != nil && !in.BackupExpiryAt.After(time.Now()) {
		return erasureInvalid("备份过期时间必须晚于当前时间")
	}
	return nil
}

func scanErasureJob(row queryScanner) (erasure.Job, error) {
	var j erasure.Job
	e := row.Scan(&j.JobID, &j.ProjectID, &j.Environment, &j.ActorID, &j.SubjectKind, &j.SubjectValue,
		&j.Reason, &j.State, &j.IdempotencyKey, &j.RequestHash, &j.BackupExpiryAt, &j.ErrorCode,
		&j.Attempts, &j.LeaseGeneration, &j.LeaseOwner, &j.LeaseUntil, &j.CreatedAt, &j.UpdatedAt, &j.FinishedAt)
	return j, e
}

const erasureJobColumns = `id::text,project_id::text,environment,actor_id::text,subject_kind,subject_value,reason,state,
 idempotency_key,request_hash,backup_expiry_at,error_code,attempts,lease_generation,lease_owner,lease_until,
 created_at,updated_at,finished_at`

func isErasureActiveConflict(e error) bool {
	var pgErr *pgconn.PgError
	return errors.As(e, &pgErr) && pgErr.Code == "23505" && pgErr.ConstraintName == "erasure_jobs_active_target"
}

func (s *Store) CreateJob(ctx context.Context, in erasure.Input) (erasure.Job, bool, error) {
	if e := validateErasureInput(in); e != nil {
		return erasure.Job{}, false, e
	}
	tx, e := s.Pool.Begin(ctx)
	if e != nil {
		return erasure.Job{}, false, e
	}
	defer tx.Rollback(context.Background())
	if e = s.erasureAuthority(ctx, tx, in.ActorID, in.ProjectID, in.Environment); e != nil {
		return erasure.Job{}, false, e
	}
	// Idempotent replay returns the originally accepted snapshot when the
	// request hash matches; a reused key with a different body conflicts.
	var existing erasure.Job
	e = tx.QueryRow(ctx, `SELECT `+erasureJobColumns+` FROM erasure_jobs
 WHERE project_id=$1 AND actor_id=$2 AND idempotency_key=$3`, in.ProjectID, in.ActorID, in.IdempotencyKey).
		Scan(scanArgs(&existing)...)
	if e == nil {
		if existing.RequestHash != in.RequestHash {
			return erasure.Job{}, false, fault.New(fault.Conflict, "IDEMPOTENCY_CONFLICT", "幂等键已用于其他请求")
		}
		return existing, false, tx.Commit(ctx)
	}
	if !errors.Is(e, pgx.ErrNoRows) {
		return erasure.Job{}, false, e
	}
	jobID := ids.New()
	var backup any
	if in.BackupExpiryAt != nil {
		backup = in.BackupExpiryAt.UTC()
	}
	row := tx.QueryRow(ctx, `INSERT INTO erasure_jobs(
 id,project_id,environment,actor_id,subject_kind,subject_value,reason,idempotency_key,request_hash,backup_expiry_at)
 VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10) RETURNING `+erasureJobColumns,
		jobID, in.ProjectID, in.Environment, in.ActorID, in.SubjectKind, in.SubjectValue,
		in.Reason, in.IdempotencyKey, in.RequestHash, backup)
	job, e := scanErasureJob(row)
	if e != nil {
		if isErasureActiveConflict(e) {
			return erasure.Job{}, false, fault.New(fault.Conflict, "ERASURE_JOB_ACTIVE", "该身份已有进行中的删除作业")
		}
		return erasure.Job{}, false, e
	}
	if _, e = tx.Exec(ctx, `INSERT INTO audit_entries(project_id,actor_id,action,resource_key)
 VALUES($1,$2,'data.erasure_requested',$3)`, in.ProjectID, in.ActorID, jobID); e != nil {
		return erasure.Job{}, false, e
	}
	return job, true, tx.Commit(ctx)
}

// scanArgs adapts the fixed column list to the pgx Scan signature.
func scanArgs(j *erasure.Job) []any {
	return []any{&j.JobID, &j.ProjectID, &j.Environment, &j.ActorID, &j.SubjectKind, &j.SubjectValue,
		&j.Reason, &j.State, &j.IdempotencyKey, &j.RequestHash, &j.BackupExpiryAt, &j.ErrorCode,
		&j.Attempts, &j.LeaseGeneration, &j.LeaseOwner, &j.LeaseUntil, &j.CreatedAt, &j.UpdatedAt, &j.FinishedAt}
}

func (s *Store) GetJob(ctx context.Context, actor, project, jobID string) (erasure.Job, error) {
	if !erasureUUIDPattern.MatchString(jobID) {
		return erasure.Job{}, fault.New(fault.Invalid, "INVALID_ERASURE_JOB", "删除作业标识格式不正确")
	}
	tx, e := s.Pool.Begin(ctx)
	if e != nil {
		return erasure.Job{}, e
	}
	defer tx.Rollback(context.Background())
	var j erasure.Job
	e = tx.QueryRow(ctx, `SELECT `+erasureJobColumns+` FROM erasure_jobs WHERE id=$1 AND project_id=$2`, jobID, project).
		Scan(scanArgs(&j)...)
	if errors.Is(e, pgx.ErrNoRows) {
		return erasure.Job{}, fault.New(fault.NotFound, "ERASURE_JOB_NOT_FOUND", "删除作业不存在或不可见")
	}
	if e != nil {
		return erasure.Job{}, e
	}
	if e = s.erasureAuthority(ctx, tx, actor, project, j.Environment); e != nil {
		return erasure.Job{}, e
	}
	return j, tx.Commit(ctx)
}

func (s *Store) ListJobs(ctx context.Context, actor, project string, filter erasure.ListFilter) (erasure.Page, error) {
	if filter.Limit < 1 || filter.Limit > 100 {
		return erasure.Page{}, fault.New(fault.Invalid, "INVALID_LIMIT", "分页大小超出范围")
	}
	if filter.After != "" && !erasureUUIDPattern.MatchString(filter.After) {
		return erasure.Page{}, fault.New(fault.Invalid, "INVALID_CURSOR", "分页游标已失效")
	}
	tx, e := s.Pool.Begin(ctx)
	if e != nil {
		return erasure.Page{}, e
	}
	defer tx.Rollback(context.Background())
	// Authority only needs project membership/environment scope; the first job's
	// environment is reloaded below for the machine credential check.
	identity, machine := access.IdentityFromContext(ctx)
	if machine {
		if identity.PrincipalID != actor {
			return erasure.Page{}, machineDenied()
		}
	} else if e = memberOwnerTx(ctx, tx, actor, project); e != nil {
		return erasure.Page{}, e
	}
	// Keyset pagination on (created_at,id); the cursor row is read in the same
	// transaction so page boundaries stay stable.
	query := `SELECT ` + erasureJobColumns + ` FROM erasure_jobs WHERE project_id=$1`
	args := []any{project}
	if filter.After != "" {
		query += ` AND (created_at,id)>(SELECT created_at,id FROM erasure_jobs WHERE project_id=$1 AND id=$2)`
		args = append(args, filter.After)
	}
	query += ` ORDER BY created_at,id LIMIT $` + strconv.Itoa(len(args)+1)
	args = append(args, filter.Limit+1)
	rows, e := tx.Query(ctx, query, args...)
	if e != nil {
		return erasure.Page{}, e
	}
	defer rows.Close()
	out := erasure.Page{Items: []erasure.Job{}}
	for rows.Next() {
		job, e := scanErasureJob(rows)
		if e != nil {
			return erasure.Page{}, e
		}
		if machine && identity.Environment != job.Environment {
			continue
		}
		out.Items = append(out.Items, job)
	}
	if e = rows.Err(); e != nil {
		return erasure.Page{}, e
	}
	if len(out.Items) > filter.Limit {
		out.After = out.Items[filter.Limit-1].JobID
		out.Items = out.Items[:filter.Limit]
	}
	return out, tx.Commit(ctx)
}

func (s *Store) ClaimJob(ctx context.Context, worker string, lease time.Duration) (*erasure.Claim, error) {
	if len(worker) < 1 || len(worker) > 128 || lease < time.Second || lease > 10*time.Minute {
		return nil, erasureInvalid("执行器租约配置无效")
	}
	tx, e := s.Pool.Begin(ctx)
	if e != nil {
		return nil, e
	}
	defer tx.Rollback(context.Background())
	// Expired leases re-enter the queue; exhausted attempts park as failed so
	// an operator must investigate rather than loop forever.
	if _, e = tx.Exec(ctx, `UPDATE erasure_jobs SET state='failed',error_code='ERASURE_ATTEMPTS_EXHAUSTED',
 finished_at=clock_timestamp(),lease_owner=NULL,lease_until=NULL,updated_at=clock_timestamp()
 WHERE id IN (SELECT id FROM erasure_jobs WHERE state IN ('hiding','purging','rebuilding')
 AND lease_until<=clock_timestamp() AND attempts>=$1::integer LIMIT 100 FOR UPDATE SKIP LOCKED)`, erasure.MaxAttempts); e != nil {
		return nil, e
	}
	if _, e = tx.Exec(ctx, `UPDATE erasure_jobs SET state='queued',lease_owner=NULL,lease_until=NULL,updated_at=clock_timestamp()
 WHERE id IN (SELECT id FROM erasure_jobs WHERE state IN ('hiding','purging','rebuilding')
 AND lease_until<=clock_timestamp() AND attempts<$1::integer LIMIT 100 FOR UPDATE SKIP LOCKED)`, erasure.MaxAttempts); e != nil {
		return nil, e
	}
	var job erasure.Job
	e = tx.QueryRow(ctx, `UPDATE erasure_jobs SET state='hiding',lease_owner=$1,
 lease_until=clock_timestamp()+$2::double precision*interval '1 second',
 lease_generation=lease_generation+1,attempts=attempts+1,updated_at=clock_timestamp()
 WHERE id=(SELECT id FROM erasure_jobs WHERE state='queued' ORDER BY created_at,id LIMIT 1 FOR UPDATE SKIP LOCKED)
 RETURNING `+erasureJobColumns, worker, lease.Seconds()).Scan(scanArgs(&job)...)
	if errors.Is(e, pgx.ErrNoRows) {
		return nil, tx.Commit(ctx)
	}
	if e != nil {
		return nil, e
	}
	if e = tx.Commit(ctx); e != nil {
		return nil, e
	}
	return &erasure.Claim{Job: job, Worker: worker, Generation: job.LeaseGeneration}, nil
}

// leaseGuard applies a state change only while the worker still owns a live
// lease and the job is in the expected state.
func (s *Store) leaseGuard(ctx context.Context, claim erasure.Claim, expected, next string, extra string, args ...any) error {
	tx, e := s.Pool.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(context.Background())
	command, e := tx.Exec(ctx, `UPDATE erasure_jobs SET state=$3,updated_at=clock_timestamp() `+extra+`
 WHERE id=$1 AND lease_owner=$2 AND lease_generation=$4 AND lease_until>clock_timestamp() AND state=$5`,
		append([]any{claim.JobID, claim.Worker, next, claim.Generation, expected}, args...)...)
	if e != nil {
		return e
	}
	if command.RowsAffected() != 1 {
		return fault.New(fault.Conflict, erasureLeaseLost, "删除作业租约已失效")
	}
	return tx.Commit(ctx)
}

func (s *Store) BeginHiding(ctx context.Context, claim erasure.Claim) error {
	if claim.State != erasure.StateHiding {
		return erasureInvalid("作业状态不允许写入删除屏障")
	}
	tx, e := s.Pool.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(context.Background())
	// The barrier must exist before the storage delete is issued. ON CONFLICT
	// re-points an existing tombstone (retry after failure) at the new job.
	var target struct{ project, environment, kind, value string }
	e = tx.QueryRow(ctx, `SELECT project_id::text,environment,subject_kind,subject_value FROM erasure_jobs
 WHERE id=$1 AND lease_owner=$2 AND lease_generation=$3 AND lease_until>clock_timestamp() AND state='hiding'
 FOR UPDATE`, claim.JobID, claim.Worker, claim.Generation).Scan(&target.project, &target.environment, &target.kind, &target.value)
	if errors.Is(e, pgx.ErrNoRows) {
		return fault.New(fault.Conflict, erasureLeaseLost, "删除作业租约已失效")
	}
	if e != nil {
		return e
	}
	if _, e = tx.Exec(ctx, `INSERT INTO erasure_tombstones(project_id,environment,subject_kind,subject_value,job_id)
 VALUES($1,$2,$3,$4,$5)
 ON CONFLICT(project_id,environment,subject_kind,subject_value)
 DO UPDATE SET job_id=EXCLUDED.job_id,created_at=clock_timestamp()`,
		target.project, target.environment, target.kind, target.value, claim.JobID); e != nil {
		return e
	}
	return tx.Commit(ctx)
}

func (s *Store) Advance(ctx context.Context, claim erasure.Claim, state string) error {
	transitions := map[string]string{erasure.StatePurging: erasure.StateHiding, erasure.StateRebuilding: erasure.StatePurging}
	expected, ok := transitions[state]
	if !ok {
		return erasureInvalid("删除作业状态流转无效")
	}
	if claim.State != expected {
		return erasureInvalid("删除作业当前状态不允许该流转")
	}
	return s.leaseGuard(ctx, claim, expected, state, "")
}

func (s *Store) Complete(ctx context.Context, claim erasure.Claim) error {
	tx, e := s.Pool.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(context.Background())
	command, e := tx.Exec(ctx, `UPDATE erasure_jobs SET state='completed',finished_at=clock_timestamp(),
 updated_at=clock_timestamp(),lease_owner=NULL,lease_until=NULL
 WHERE id=$1 AND lease_owner=$2 AND lease_generation=$3 AND lease_until>clock_timestamp() AND state='rebuilding'`,
		claim.JobID, claim.Worker, claim.Generation)
	if e != nil {
		return e
	}
	if command.RowsAffected() != 1 {
		return fault.New(fault.Conflict, erasureLeaseLost, "删除作业租约已失效")
	}
	if _, e = tx.Exec(ctx, `INSERT INTO audit_entries(project_id,actor_id,action,resource_key)
 SELECT project_id,actor_id,'data.erasure_completed',id FROM erasure_jobs WHERE id=$1`, claim.JobID); e != nil {
		return e
	}
	return tx.Commit(ctx)
}

func (s *Store) Fail(ctx context.Context, claim erasure.Claim, code string) error {
	if len(code) < 1 || len(code) > 64 {
		return erasureInvalid("失败代码无效")
	}
	tx, e := s.Pool.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(context.Background())
	command, e := tx.Exec(ctx, `UPDATE erasure_jobs SET state='failed',error_code=$4,finished_at=clock_timestamp(),
 updated_at=clock_timestamp(),lease_owner=NULL,lease_until=NULL
 WHERE id=$1 AND lease_owner=$2 AND lease_generation=$3 AND state IN ('hiding','purging','rebuilding')`,
		claim.JobID, claim.Worker, claim.Generation, code)
	if e != nil {
		return e
	}
	if command.RowsAffected() != 1 {
		return fault.New(fault.Conflict, erasureLeaseLost, "删除作业租约已失效")
	}
	return tx.Commit(ctx)
}
