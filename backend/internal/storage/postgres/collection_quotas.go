package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/howtomakeaname/Evia/backend/internal/access"
	"github.com/howtomakeaname/Evia/backend/internal/platform/fault"
	"github.com/howtomakeaname/Evia/backend/internal/quotas"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

var _ quotas.Store = (*Store)(nil)

func quotaRevisionConflict() error {
	return fault.New(fault.Conflict, "QUOTA_REVISION_CONFLICT", "采集配额修订已变化或已被创建，请重新加载")
}

func scanQuota(row queryScanner) (quotas.Policy, error) {
	var p quotas.Policy
	var createdAt, updatedAt time.Time
	if e := row.Scan(&p.ProjectID, &p.Environment, &p.Rate, &p.Burst, &p.Revision, &createdAt, &updatedAt); e != nil {
		return quotas.Policy{}, e
	}
	p.CreatedAt = createdAt.UTC().Format(time.RFC3339Nano)
	p.UpdatedAt = updatedAt.UTC().Format(time.RFC3339Nano)
	return p, nil
}

const quotaColumns = `project_id::text,environment,rate_per_second,burst,revision,created_at,updated_at`

// quotaOwnerTx reuses the owner-only human governance rule. Machines are
// rejected and non-members see the invisible-project response.
func quotaOwnerTx(ctx context.Context, tx pgx.Tx, actor, project string) error {
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
		return fault.New(fault.Forbidden, "QUOTA_FORBIDDEN", "只有项目所有者可以管理采集配额")
	}
	return setAuditCredential(ctx, tx, "")
}

func (s *Store) GetQuota(ctx context.Context, actor, project, environment string) (quotas.Policy, bool, error) {
	tx, e := s.Pool.Begin(ctx)
	if e != nil {
		return quotas.Policy{}, false, e
	}
	defer tx.Rollback(context.Background())
	if e = quotaOwnerTx(ctx, tx, actor, project); e != nil {
		return quotas.Policy{}, false, e
	}
	row := tx.QueryRow(ctx, `SELECT `+quotaColumns+` FROM collection_quotas
 WHERE project_id=$1 AND environment=$2`, project, environment)
	p, e := scanQuota(row)
	if errors.Is(e, pgx.ErrNoRows) {
		return quotas.Policy{}, false, tx.Commit(ctx)
	}
	if e != nil {
		return quotas.Policy{}, false, e
	}
	return p, true, tx.Commit(ctx)
}

func (s *Store) PutQuota(ctx context.Context, in quotas.Input, expectedRevision int64) (quotas.Policy, bool, error) {
	tx, e := s.Pool.Begin(ctx)
	if e != nil {
		return quotas.Policy{}, false, e
	}
	defer tx.Rollback(context.Background())
	if e = quotaOwnerTx(ctx, tx, in.ActorID, in.ProjectID); e != nil {
		return quotas.Policy{}, false, e
	}
	// Serialize concurrent first-time creates for the same scope.
	if _, e = tx.Exec(ctx, `SELECT id FROM projects WHERE id=$1 FOR UPDATE`, in.ProjectID); e != nil {
		return quotas.Policy{}, false, e
	}
	var priorHash string
	var response []byte
	e = tx.QueryRow(ctx, `SELECT request_hash,response FROM collection_quota_mutations
 WHERE project_id=$1 AND actor_id=$2 AND idempotency_key=$3`,
		in.ProjectID, in.ActorID, in.IdempotencyKey).Scan(&priorHash, &response)
	if e == nil {
		if priorHash != in.RequestHash {
			return quotas.Policy{}, false, fault.New(fault.Conflict, "IDEMPOTENCY_CONFLICT", "幂等键已用于不同请求")
		}
		var replay quotas.Policy
		if e = json.Unmarshal(response, &replay); e != nil {
			return quotas.Policy{}, false, e
		}
		return replay, true, tx.Commit(ctx)
	}
	if !errors.Is(e, pgx.ErrNoRows) {
		return quotas.Policy{}, false, e
	}

	var p quotas.Policy
	if expectedRevision == quotas.CreateRevision {
		p, e = scanQuota(tx.QueryRow(ctx, `INSERT INTO collection_quotas(project_id,environment,rate_per_second,burst)
 VALUES($1,$2,$3,$4) RETURNING `+quotaColumns, in.ProjectID, in.Environment, in.Rate, in.Burst))
		var pgErr *pgconn.PgError
		if errors.As(e, &pgErr) && pgErr.Code == "23505" {
			return quotas.Policy{}, false, quotaRevisionConflict()
		}
		if e != nil {
			return quotas.Policy{}, false, e
		}
	} else {
		p, e = scanQuota(tx.QueryRow(ctx, `UPDATE collection_quotas
 SET rate_per_second=$3,burst=$4,revision=revision+1,updated_at=clock_timestamp()
 WHERE project_id=$1 AND environment=$2 AND revision=$5 RETURNING `+quotaColumns,
			in.ProjectID, in.Environment, in.Rate, in.Burst, expectedRevision))
		if errors.Is(e, pgx.ErrNoRows) {
			return quotas.Policy{}, false, quotaRevisionConflict()
		}
		if e != nil {
			return quotas.Policy{}, false, e
		}
	}

	if _, e = tx.Exec(ctx, `INSERT INTO audit_entries(project_id,actor_id,action,resource_key)
 VALUES($1,$2,'collection.quota_updated',$3)`, in.ProjectID, in.ActorID, in.Environment); e != nil {
		return quotas.Policy{}, false, e
	}
	if response, e = json.Marshal(p); e != nil {
		return quotas.Policy{}, false, e
	}
	if _, e = tx.Exec(ctx, `INSERT INTO collection_quota_mutations(project_id,actor_id,idempotency_key,request_hash,response)
 VALUES($1,$2,$3,$4,$5)`, in.ProjectID, in.ActorID, in.IdempotencyKey, in.RequestHash, response); e != nil {
		return quotas.Policy{}, false, e
	}
	return p, false, tx.Commit(ctx)
}

// ListQuotas is the system-scoped boot loader without a human actor. The
// serve process needs every project/environment override to build its limiter.
func (s *Store) ListQuotas(ctx context.Context) ([]quotas.Policy, error) {
	rows, e := s.Pool.Query(ctx, `SELECT `+quotaColumns+` FROM collection_quotas ORDER BY project_id,environment`)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := make([]quotas.Policy, 0)
	for rows.Next() {
		p, err := scanQuota(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}
