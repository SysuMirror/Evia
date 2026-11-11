package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"regexp"
	"strconv"
	"time"

	"github.com/howtomakeaname/Evia/backend/internal/access"
	"github.com/howtomakeaname/Evia/backend/internal/exporting"
	"github.com/howtomakeaname/Evia/backend/internal/platform/fault"
	"github.com/howtomakeaname/Evia/backend/internal/platform/ids"
	"github.com/jackc/pgx/v5"
)

var _ exporting.Store = (*Store)(nil)

var exportUUIDPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

const exportLeaseLost = "EXPORT_LEASE_LOST"

func exportInvalid(message string) error {
	return fault.New(fault.Invalid, "INVALID_EXPORT_INPUT", message)
}

// exportAuthority keeps export creation owner-only for human sessions while
// machines need the explicit data.export grant reloaded inside the transaction.
func (s *Store) exportAuthority(ctx context.Context, tx pgx.Tx, actor, project, environment string) error {
	identity, machine := access.IdentityFromContext(ctx)
	if machine {
		if identity.PrincipalID != actor {
			return machineDenied()
		}
		return s.authorizeOperation(ctx, tx, actor, project, environment, access.PermissionDataExport)
	}
	return memberOwnerTx(ctx, tx, actor, project)
}

func validateExportInput(in exporting.Input) error {
	if !exportUUIDPattern.MatchString(in.ProjectID) || !exportUUIDPattern.MatchString(in.ActorID) {
		return exportInvalid("项目或操作主体标识格式不正确")
	}
	switch in.Environment {
	case "development", "staging", "production":
	default:
		return exportInvalid("环境标识无效")
	}
	if len(in.Name) < 1 || len(in.Name) > exporting.MaxNameLength {
		return exportInvalid("导出名称长度无效")
	}
	if len(in.IdempotencyKey) < 1 || len(in.IdempotencyKey) > 128 {
		return exportInvalid("幂等键长度无效")
	}
	if len(in.RequestHash) != 64 {
		return exportInvalid("请求摘要无效")
	}
	if in.Request.Kind == "" || in.Request.Environment != in.Environment {
		return exportInvalid("导出必须包含环境一致的有界分析请求")
	}
	if in.ArtifactExpiry != nil && !in.ArtifactExpiry.After(time.Now()) {
		return exportInvalid("产物过期时间必须晚于当前时间")
	}
	return nil
}

const exportJobColumns = `id::text,project_id::text,environment,actor_id::text,name,format,request_json,state,
 idempotency_key,request_hash,error_code,attempts,lease_generation,lease_owner,lease_until,
 artifact_path,artifact_sha256,artifact_bytes,result_rows,expires_at,created_at,updated_at,finished_at`

func scanExportJob(row queryScanner) (exporting.Job, error) {
	var j exporting.Job
	var rawRequest []byte
	var artifactPath, artifactSHA *string
	var artifactBytes *int64
	var resultRows *int
	e := row.Scan(&j.JobID, &j.ProjectID, &j.Environment, &j.ActorID, &j.Name, &j.Format, &rawRequest,
		&j.State, &j.IdempotencyKey, &j.RequestHash, &j.ErrorCode, &j.Attempts, &j.LeaseGeneration,
		&j.LeaseOwner, &j.LeaseUntil, &artifactPath, &artifactSHA, &artifactBytes, &resultRows,
		&j.ExpiresAt, &j.CreatedAt, &j.UpdatedAt, &j.FinishedAt)
	if e != nil {
		return j, e
	}
	if e = json.Unmarshal(rawRequest, &j.Request); e != nil {
		return j, e
	}
	j.ArtifactPath = artifactPath
	j.ArtifactSHA256 = artifactSHA
	j.ArtifactBytes = artifactBytes
	j.ResultRows = resultRows
	return j, nil
}

func (s *Store) CreateExportJob(ctx context.Context, in exporting.Input) (exporting.Job, bool, error) {
	if e := validateExportInput(in); e != nil {
		return exporting.Job{}, false, e
	}
	rawRequest, e := json.Marshal(in.Request)
	if e != nil {
		return exporting.Job{}, false, e
	}
	tx, e := s.Pool.Begin(ctx)
	if e != nil {
		return exporting.Job{}, false, e
	}
	defer tx.Rollback(context.Background())
	if e = s.exportAuthority(ctx, tx, in.ActorID, in.ProjectID, in.Environment); e != nil {
		return exporting.Job{}, false, e
	}
	existing, e := scanExportJob(tx.QueryRow(ctx, `SELECT `+exportJobColumns+` FROM export_jobs
 WHERE project_id=$1 AND actor_id=$2 AND idempotency_key=$3`, in.ProjectID, in.ActorID, in.IdempotencyKey))
	if e == nil {
		if existing.RequestHash != in.RequestHash {
			return exporting.Job{}, false, fault.New(fault.Conflict, "IDEMPOTENCY_CONFLICT", "幂等键已用于其他请求")
		}
		return existing, false, tx.Commit(ctx)
	}
	if !errors.Is(e, pgx.ErrNoRows) {
		return exporting.Job{}, false, e
	}
	jobID := ids.New()
	var expiry any
	if in.ArtifactExpiry != nil {
		expiry = in.ArtifactExpiry.UTC()
	}
	row := tx.QueryRow(ctx, `INSERT INTO export_jobs(
 id,project_id,environment,actor_id,name,request_json,idempotency_key,request_hash,expires_at)
 VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9) RETURNING `+exportJobColumns,
		jobID, in.ProjectID, in.Environment, in.ActorID, in.Name, rawRequest,
		in.IdempotencyKey, in.RequestHash, expiry)
	job, e := scanExportJob(row)
	if e != nil {
		return exporting.Job{}, false, e
	}
	if _, e = tx.Exec(ctx, `INSERT INTO audit_entries(project_id,actor_id,action,resource_key)
 VALUES($1,$2,'data.export_requested',$3)`, in.ProjectID, in.ActorID, jobID); e != nil {
		return exporting.Job{}, false, e
	}
	return job, true, tx.Commit(ctx)
}

func (s *Store) GetExportJob(ctx context.Context, actor, project, jobID string) (exporting.Job, error) {
	if !exportUUIDPattern.MatchString(jobID) {
		return exporting.Job{}, fault.New(fault.Invalid, "INVALID_EXPORT_JOB", "导出作业标识格式不正确")
	}
	tx, e := s.Pool.Begin(ctx)
	if e != nil {
		return exporting.Job{}, e
	}
	defer tx.Rollback(context.Background())
	j, e := scanExportJob(tx.QueryRow(ctx, `SELECT `+exportJobColumns+` FROM export_jobs WHERE id=$1 AND project_id=$2`, jobID, project))
	if errors.Is(e, pgx.ErrNoRows) {
		return exporting.Job{}, fault.New(fault.NotFound, "EXPORT_JOB_NOT_FOUND", "导出作业不存在或不可见")
	}
	if e != nil {
		return exporting.Job{}, e
	}
	if e = s.exportAuthority(ctx, tx, actor, project, j.Environment); e != nil {
		return exporting.Job{}, e
	}
	return j, tx.Commit(ctx)
}

func (s *Store) GetSystemExportJob(ctx context.Context, project, jobID string) (exporting.Job, error) {
	if !exportUUIDPattern.MatchString(jobID) {
		return exporting.Job{}, fault.New(fault.Invalid, "INVALID_EXPORT_JOB", "导出作业标识格式不正确")
	}
	j, e := scanExportJob(s.Pool.QueryRow(ctx, `SELECT `+exportJobColumns+` FROM export_jobs WHERE id=$1 AND project_id=$2`, jobID, project))
	if errors.Is(e, pgx.ErrNoRows) {
		return exporting.Job{}, fault.New(fault.NotFound, "EXPORT_JOB_NOT_FOUND", "导出作业不存在")
	}
	return j, e
}

func (s *Store) ListExportJobs(ctx context.Context, actor, project string, filter exporting.ListFilter) (exporting.Page, error) {
	if filter.Limit < 1 || filter.Limit > 100 {
		return exporting.Page{}, fault.New(fault.Invalid, "INVALID_LIMIT", "分页大小超出范围")
	}
	if filter.After != "" && !exportUUIDPattern.MatchString(filter.After) {
		return exporting.Page{}, fault.New(fault.Invalid, "INVALID_CURSOR", "分页游标已失效")
	}
	tx, e := s.Pool.Begin(ctx)
	if e != nil {
		return exporting.Page{}, e
	}
	defer tx.Rollback(context.Background())
	identity, machine := access.IdentityFromContext(ctx)
	if machine {
		if identity.PrincipalID != actor {
			return exporting.Page{}, machineDenied()
		}
	} else if e = memberOwnerTx(ctx, tx, actor, project); e != nil {
		return exporting.Page{}, e
	}
	query := `SELECT ` + exportJobColumns + ` FROM export_jobs WHERE project_id=$1`
	args := []any{project}
	if filter.After != "" {
		query += ` AND (created_at,id)>(SELECT created_at,id FROM export_jobs WHERE project_id=$1 AND id=$2)`
		args = append(args, filter.After)
	}
	query += ` ORDER BY created_at,id LIMIT $` + strconv.Itoa(len(args)+1)
	args = append(args, filter.Limit+1)
	rows, e := tx.Query(ctx, query, args...)
	if e != nil {
		return exporting.Page{}, e
	}
	defer rows.Close()
	out := exporting.Page{Items: []exporting.Job{}}
	for rows.Next() {
		job, e := scanExportJob(rows)
		if e != nil {
			return exporting.Page{}, e
		}
		if machine && identity.Environment != job.Environment {
			continue
		}
		out.Items = append(out.Items, job)
	}
	if e = rows.Err(); e != nil {
		return exporting.Page{}, e
	}
	if len(out.Items) > filter.Limit {
		out.After = out.Items[filter.Limit-1].JobID
		out.Items = out.Items[:filter.Limit]
	}
	return out, tx.Commit(ctx)
}

func (s *Store) CancelExportJob(ctx context.Context, actor, project, jobID string) (exporting.Job, error) {
	if !exportUUIDPattern.MatchString(jobID) {
		return exporting.Job{}, fault.New(fault.Invalid, "INVALID_EXPORT_JOB", "导出作业标识格式不正确")
	}
	tx, e := s.Pool.Begin(ctx)
	if e != nil {
		return exporting.Job{}, e
	}
	defer tx.Rollback(context.Background())
	j, e := scanExportJob(tx.QueryRow(ctx, `SELECT `+exportJobColumns+` FROM export_jobs WHERE id=$1 AND project_id=$2 FOR UPDATE`, jobID, project))
	if errors.Is(e, pgx.ErrNoRows) {
		return exporting.Job{}, fault.New(fault.NotFound, "EXPORT_JOB_NOT_FOUND", "导出作业不存在或不可见")
	}
	if e != nil {
		return exporting.Job{}, e
	}
	if e = s.exportAuthority(ctx, tx, actor, project, j.Environment); e != nil {
		return exporting.Job{}, e
	}
	if j.State != exporting.StateQueued {
		return exporting.Job{}, fault.New(fault.Conflict, "EXPORT_NOT_CANCELLABLE", "只有排队中的导出作业可以取消")
	}
	j, e = scanExportJob(tx.QueryRow(ctx, `UPDATE export_jobs SET state='cancelled',finished_at=clock_timestamp(),updated_at=clock_timestamp()
 WHERE id=$1 RETURNING `+exportJobColumns, jobID))
	if e != nil {
		return exporting.Job{}, e
	}
	if _, e = tx.Exec(ctx, `INSERT INTO audit_entries(project_id,actor_id,action,resource_key)
 VALUES($1,$2,'data.export_cancelled',$3)`, project, actor, jobID); e != nil {
		return exporting.Job{}, e
	}
	return j, tx.Commit(ctx)
}

func (s *Store) ClaimExportJob(ctx context.Context, worker string, lease time.Duration) (*exporting.Claim, error) {
	if len(worker) < 1 || len(worker) > 128 || lease < time.Second || lease > 10*time.Minute {
		return nil, exportInvalid("执行器租约配置无效")
	}
	tx, e := s.Pool.Begin(ctx)
	if e != nil {
		return nil, e
	}
	defer tx.Rollback(context.Background())
	// Expired running jobs exhaust attempts into failed or re-enter the queue.
	if _, e = tx.Exec(ctx, `UPDATE export_jobs SET state='failed',error_code='EXPORT_ATTEMPTS_EXHAUSTED',
 finished_at=clock_timestamp(),lease_owner=NULL,lease_until=NULL,updated_at=clock_timestamp()
 WHERE id IN (SELECT id FROM export_jobs WHERE state='running'
 AND lease_until<=clock_timestamp() AND attempts>=$1 LIMIT 100 FOR UPDATE SKIP LOCKED)`, exporting.MaxAttempts); e != nil {
		return nil, e
	}
	if _, e = tx.Exec(ctx, `UPDATE export_jobs SET state='queued',lease_owner=NULL,lease_until=NULL,updated_at=clock_timestamp()
 WHERE id IN (SELECT id FROM export_jobs WHERE state='running'
 AND lease_until<=clock_timestamp() AND attempts<$1 LIMIT 100 FOR UPDATE SKIP LOCKED)`, exporting.MaxAttempts); e != nil {
		return nil, e
	}
	job, e := scanExportJob(tx.QueryRow(ctx, `UPDATE export_jobs SET state='running',lease_owner=$1,
 lease_until=clock_timestamp()+$2::double precision*interval '1 second',
 lease_generation=lease_generation+1,attempts=attempts+1,updated_at=clock_timestamp()
 WHERE id=(SELECT id FROM export_jobs WHERE state='queued' ORDER BY created_at,id LIMIT 1 FOR UPDATE SKIP LOCKED)
 RETURNING `+exportJobColumns, worker, lease.Seconds()))
	if errors.Is(e, pgx.ErrNoRows) {
		return nil, tx.Commit(ctx)
	}
	if e != nil {
		return nil, e
	}
	if e = tx.Commit(ctx); e != nil {
		return nil, e
	}
	return &exporting.Claim{Job: job, Worker: worker, Generation: job.LeaseGeneration}, nil
}

func (s *Store) exportLeaseGuard(ctx context.Context, claim exporting.Claim, next string, extra string, args ...any) error {
	tx, e := s.Pool.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(context.Background())
	command, e := tx.Exec(ctx, `UPDATE export_jobs SET state=$3,updated_at=clock_timestamp() `+extra+`
 WHERE id=$1 AND lease_owner=$2 AND lease_generation=$4 AND lease_until>clock_timestamp() AND state='running'`,
		append([]any{claim.JobID, claim.Worker, next, claim.Generation}, args...)...)
	if e != nil {
		return e
	}
	if command.RowsAffected() != 1 {
		return fault.New(fault.Conflict, exportLeaseLost, "导出作业租约已失效")
	}
	return tx.Commit(ctx)
}

func (s *Store) CompleteExportJob(ctx context.Context, claim exporting.Claim, artifact exporting.Artifact, expiresAt *time.Time) error {
	if artifact.SHA256 == "" || artifact.Path == "" {
		return exportInvalid("导出产物信息不完整")
	}
	var expiry any
	if expiresAt != nil {
		expiry = expiresAt.UTC()
	}
	return s.exportLeaseGuard(ctx, claim, exporting.StateCompleted,
		`,artifact_path=$5,artifact_sha256=$6,artifact_bytes=$7,result_rows=$8,expires_at=COALESCE($9,expires_at),finished_at=clock_timestamp()`,
		artifact.Path, artifact.SHA256, artifact.Bytes, artifact.Rows, expiry)
}

func (s *Store) FailExportJob(ctx context.Context, claim exporting.Claim, code string) error {
	if len(code) < 1 || len(code) > 128 {
		return exportInvalid("失败码无效")
	}
	return s.exportLeaseGuard(ctx, claim, exporting.StateFailed,
		`,error_code=$5,finished_at=clock_timestamp()`, code)
}

func (s *Store) CreateExportGrant(ctx context.Context, actor, project, jobID, tokenHash string, expiresAt time.Time) (exporting.Grant, error) {
	if !exportUUIDPattern.MatchString(jobID) || len(tokenHash) != 64 {
		return exporting.Grant{}, exportInvalid("下载授权参数无效")
	}
	tx, e := s.Pool.Begin(ctx)
	if e != nil {
		return exporting.Grant{}, e
	}
	defer tx.Rollback(context.Background())
	var state string
	e = tx.QueryRow(ctx, `SELECT state FROM export_jobs WHERE id=$1 AND project_id=$2 FOR UPDATE`, jobID, project).Scan(&state)
	if errors.Is(e, pgx.ErrNoRows) {
		return exporting.Grant{}, fault.New(fault.NotFound, "EXPORT_JOB_NOT_FOUND", "导出作业不存在或不可见")
	}
	if e != nil {
		return exporting.Grant{}, e
	}
	if e = s.exportAuthority(ctx, tx, actor, project, ""); e != nil {
		return exporting.Grant{}, e
	}
	if state != exporting.StateCompleted {
		return exporting.Grant{}, fault.New(fault.Invalid, "EXPORT_NOT_READY", "只有已完成的导出可以创建下载授权")
	}
	var grant exporting.Grant
	e = tx.QueryRow(ctx, `INSERT INTO export_download_grants(token_hash,job_id,project_id,actor_id,expires_at)
 VALUES($1,$2,$3,$4,$5) RETURNING token_hash,job_id::text,project_id::text,actor_id::text,expires_at,redeemed_at,created_at`,
		tokenHash, jobID, project, actor, expiresAt.UTC()).
		Scan(&grant.TokenHash, &grant.JobID, &grant.ProjectID, &grant.ActorID, &grant.ExpiresAt, &grant.RedeemedAt, &grant.CreatedAt)
	if e != nil {
		return exporting.Grant{}, e
	}
	if _, e = tx.Exec(ctx, `INSERT INTO audit_entries(project_id,actor_id,action,resource_key)
 VALUES($1,$2,'data.export_grant_created',$3)`, project, actor, jobID); e != nil {
		return exporting.Grant{}, e
	}
	return grant, tx.Commit(ctx)
}

func (s *Store) RedeemExportGrant(ctx context.Context, tokenHash string) (*exporting.Grant, error) {
	if len(tokenHash) != 64 {
		return nil, nil
	}
	tx, e := s.Pool.Begin(ctx)
	if e != nil {
		return nil, e
	}
	defer tx.Rollback(context.Background())
	var grant exporting.Grant
	e = tx.QueryRow(ctx, `UPDATE export_download_grants SET redeemed_at=clock_timestamp()
 WHERE token_hash=$1 AND redeemed_at IS NULL AND expires_at>clock_timestamp()
 RETURNING token_hash,job_id::text,project_id::text,actor_id::text,expires_at,redeemed_at,created_at`,
		tokenHash).
		Scan(&grant.TokenHash, &grant.JobID, &grant.ProjectID, &grant.ActorID, &grant.ExpiresAt, &grant.RedeemedAt, &grant.CreatedAt)
	if errors.Is(e, pgx.ErrNoRows) {
		return nil, tx.Commit(ctx)
	}
	if e != nil {
		return nil, e
	}
	if _, e = tx.Exec(ctx, `INSERT INTO audit_entries(project_id,actor_id,action,resource_key)
 VALUES($1,$2,'data.export_downloaded',$3)`, grant.ProjectID, grant.ActorID, grant.JobID); e != nil {
		return nil, e
	}
	if e = tx.Commit(ctx); e != nil {
		return nil, e
	}
	return &grant, nil
}
