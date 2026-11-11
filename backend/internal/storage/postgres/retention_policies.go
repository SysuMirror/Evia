package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/howtomakeaname/Evia/backend/internal/access"
	"github.com/howtomakeaname/Evia/backend/internal/platform/fault"
	"github.com/howtomakeaname/Evia/backend/internal/retention"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

var _ retention.Store = (*Store)(nil)

// retentionOwnerTx mirrors the governance/erasure owner rule with
// retention-specific wording. Machines are rejected; non-members see the same
// invisible-project response; only owners may read or write lifecycle policy.
func retentionOwnerTx(ctx context.Context, tx pgx.Tx, actor, project string) error {
	if _, machine := access.IdentityFromContext(ctx); machine {
		return machineDenied()
	}
	var role string
	e := tx.QueryRow(ctx, `SELECT m.role FROM project_members m JOIN accounts a ON a.id=m.user_id
 WHERE m.project_id=$1 AND m.user_id=$2 AND NOT a.disabled FOR SHARE OF m,a`, project, actor).Scan(&role)
	if errors.Is(e, pgx.ErrNoRows) {
		return projectInvisible()
	}
	if e != nil {
		return e
	}
	if role != "owner" {
		return fault.New(fault.Forbidden, "RETENTION_FORBIDDEN", "只有项目所有者可以管理数据保留策略")
	}
	return setAuditCredential(ctx, tx, "")
}

func retentionRevisionConflict() error {
	return fault.New(fault.Conflict, "RETENTION_REVISION_CONFLICT", "保留策略修订已变化或已被创建，请重新加载")
}

func scanPolicy(row queryScanner) (retention.Policy, error) {
	var p retention.Policy
	var createdAt, updatedAt time.Time
	if e := row.Scan(&p.ProjectID, &p.Environment, &p.RetentionDays, &p.Revision, &createdAt, &updatedAt); e != nil {
		return retention.Policy{}, e
	}
	p.CreatedAt = createdAt.UTC().Format(time.RFC3339Nano)
	p.UpdatedAt = updatedAt.UTC().Format(time.RFC3339Nano)
	return p, nil
}

const policyColumns = `project_id::text,environment,retention_days,revision,created_at,updated_at`

func (s *Store) GetPolicy(ctx context.Context, actor, project, environment string) (retention.Policy, bool, error) {
	tx, e := s.Pool.Begin(ctx)
	if e != nil {
		return retention.Policy{}, false, e
	}
	defer tx.Rollback(context.Background())
	if e = retentionOwnerTx(ctx, tx, actor, project); e != nil {
		return retention.Policy{}, false, e
	}
	row := tx.QueryRow(ctx, `SELECT `+policyColumns+` FROM retention_policies
 WHERE project_id=$1 AND environment=$2`, project, environment)
	p, e := scanPolicy(row)
	if errors.Is(e, pgx.ErrNoRows) {
		return retention.Policy{}, false, tx.Commit(ctx)
	}
	if e != nil {
		return retention.Policy{}, false, e
	}
	return p, true, tx.Commit(ctx)
}

func (s *Store) PutPolicy(ctx context.Context, in retention.Input, expectedRevision int64) (retention.Policy, error) {
	tx, e := s.Pool.Begin(ctx)
	if e != nil {
		return retention.Policy{}, e
	}
	defer tx.Rollback(context.Background())
	if e = retentionOwnerTx(ctx, tx, in.ActorID, in.ProjectID); e != nil {
		return retention.Policy{}, e
	}
	// Serialize concurrent first-time creates for the same scope on the project.
	if _, e = tx.Exec(ctx, `SELECT id FROM projects WHERE id=$1 FOR UPDATE`, in.ProjectID); e != nil {
		return retention.Policy{}, e
	}
	// Idempotent replay returns the originally accepted snapshot; a reused key
	// with a different body conflicts before any row is touched.
	var priorHash string
	var response []byte
	e = tx.QueryRow(ctx, `SELECT request_hash,response FROM retention_policy_mutations
 WHERE project_id=$1 AND actor_id=$2 AND idempotency_key=$3`,
		in.ProjectID, in.ActorID, in.IdempotencyKey).Scan(&priorHash, &response)
	if e == nil {
		if priorHash != in.RequestHash {
			return retention.Policy{}, fault.New(fault.Conflict, "IDEMPOTENCY_CONFLICT", "幂等键已用于不同请求")
		}
		var replay retention.Policy
		if e = json.Unmarshal(response, &replay); e != nil {
			return retention.Policy{}, e
		}
		return replay, tx.Commit(ctx)
	}
	if !errors.Is(e, pgx.ErrNoRows) {
		return retention.Policy{}, e
	}

	var p retention.Policy
	if expectedRevision == retention.CreateRevision {
		p, e = scanPolicy(tx.QueryRow(ctx, `INSERT INTO retention_policies(project_id,environment,retention_days)
 VALUES($1,$2,$3) RETURNING `+policyColumns, in.ProjectID, in.Environment, in.RetentionDays))
		var pgErr *pgconn.PgError
		if errors.As(e, &pgErr) && pgErr.Code == "23505" {
			return retention.Policy{}, retentionRevisionConflict()
		}
		if e != nil {
			return retention.Policy{}, e
		}
	} else {
		p, e = scanPolicy(tx.QueryRow(ctx, `UPDATE retention_policies
 SET retention_days=$3,revision=revision+1,updated_at=clock_timestamp()
 WHERE project_id=$1 AND environment=$2 AND revision=$4 RETURNING `+policyColumns,
			in.ProjectID, in.Environment, in.RetentionDays, expectedRevision))
		if errors.Is(e, pgx.ErrNoRows) {
			return retention.Policy{}, retentionRevisionConflict()
		}
		if e != nil {
			return retention.Policy{}, e
		}
	}

	if _, e = tx.Exec(ctx, `INSERT INTO audit_entries(project_id,actor_id,action,resource_key)
 VALUES($1,$2,'retention.policy_updated',$3)`, in.ProjectID, in.ActorID, in.Environment); e != nil {
		return retention.Policy{}, e
	}
	if response, e = json.Marshal(p); e != nil {
		return retention.Policy{}, e
	}
	if _, e = tx.Exec(ctx, `INSERT INTO retention_policy_mutations(project_id,actor_id,idempotency_key,request_hash,response)
 VALUES($1,$2,$3,$4,$5)`, in.ProjectID, in.ActorID, in.IdempotencyKey, in.RequestHash, response); e != nil {
		return retention.Policy{}, e
	}
	return p, tx.Commit(ctx)
}

// ListRetentionPolicies is a worker-scoped read with no human actor or owner
// re-check. The retention worker runs as a system process and must see every
// configured project/environment scope.
func (s *Store) ListRetentionPolicies(ctx context.Context) ([]retention.Policy, error) {
	rows, e := s.Pool.Query(ctx, `SELECT `+policyColumns+` FROM retention_policies ORDER BY project_id, environment`)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	policies := make([]retention.Policy, 0)
	for rows.Next() {
		p, err := scanPolicy(rows)
		if err != nil {
			return nil, err
		}
		policies = append(policies, p)
	}
	return policies, rows.Err()
}

// RecordSweep persists one sweep audit row. It is worker-scoped and therefore
// does not carry an idempotency key: every evaluation is a distinct audit event.
func (s *Store) RecordSweep(ctx context.Context, record retention.SweepRecord) error {
	_, e := s.Pool.Exec(ctx, `INSERT INTO retention_sweeps
 (id,project_id,environment,retention_days,cutoff_at,dry_run,rows_candidates,status,detail,created_at,completed_at)
 VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)`,
		record.ID, record.ProjectID, record.Environment, record.RetentionDays,
		record.Cutoff, record.DryRun, record.RowsCandidates, record.Status, record.Detail,
		record.CreatedAt, record.CompletedAt)
	return e
}
