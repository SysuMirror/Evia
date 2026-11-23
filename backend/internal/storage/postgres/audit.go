package postgres

import (
	"context"
	"errors"
	"time"

	"github.com/howtomakeaname/Evia/backend/internal/access"
	"github.com/howtomakeaname/Evia/backend/internal/platform/fault"
	"github.com/jackc/pgx/v5"
)

var _ access.AuditStore = (*Store)(nil)

// auditBoundary rounds upwards because stored timestamptz values have microsecond
// precision. Both >= from and < to must compare against the ceiling to preserve
// the caller's nanosecond half-open interval when pgx encodes the parameters.
func auditBoundary(value time.Time) time.Time {
	value = value.UTC()
	if remainder := value.Nanosecond() % 1000; remainder != 0 {
		value = value.Add(time.Duration(1000 - remainder))
	}
	return value
}

func (s *Store) ListAuditLogs(ctx context.Context, reader, project string, filter access.AuditFilter, after *access.AuditPosition, limit int) ([]access.AuditEntry, error) {
	if _, machine := access.IdentityFromContext(ctx); machine {
		return nil, machineDenied()
	}
	if limit < 1 || limit > 101 {
		return nil, fault.New(fault.Invalid, "INVALID_LIMIT", "分页大小超出范围")
	}
	if e := access.ValidateAuditQuery(reader, project, filter, after); e != nil {
		return nil, e
	}
	tx, e := s.Pool.Begin(ctx)
	if e != nil {
		return nil, e
	}
	defer tx.Rollback(context.Background())
	// Keep account disabling, membership removal, and role changes serialized
	// until all returned rows have been read and this transaction commits.
	var role string
	e = tx.QueryRow(ctx, `SELECT m.role FROM project_members m JOIN accounts a ON a.id=m.user_id WHERE m.project_id=$1 AND m.user_id=$2 AND NOT a.disabled FOR SHARE OF m,a`, project, reader).Scan(&role)
	if errors.Is(e, pgx.ErrNoRows) {
		return nil, projectInvisible()
	}
	if e != nil {
		return nil, e
	}
	if role != "owner" {
		return nil, fault.New(fault.Forbidden, "AUDIT_FORBIDDEN", "只有项目所有者可以读取审计记录")
	}
	var afterTime *time.Time
	var afterID string
	if after != nil {
		positionTime := after.OccurredAt.UTC()
		afterTime, afterID = &positionTime, after.ID
	}
	rows, e := tx.Query(ctx, `SELECT e.id::text,e.project_id::text,e.actor_id::text,p.kind,e.action,e.resource_key,
 e.credential_id::text,e.environment,e.outcome,e.occurred_at
 FROM audit_entries e JOIN principals p ON p.id=e.actor_id
 WHERE e.project_id=$1 AND e.occurred_at >= $2 AND e.occurred_at < $3
 AND ($4='' OR e.action=$4) AND ($5='' OR e.actor_id=nullif($5,'')::uuid)
 AND ($6='' OR e.environment=$6) AND ($7='' OR e.outcome=$7)
 AND ($8='' OR left(e.resource_key,length($8))=$8)
 AND ($9::timestamptz IS NULL OR (e.occurred_at,e.id)<($9,nullif($10,'')::bigint))
 ORDER BY e.occurred_at DESC,e.id DESC LIMIT $11`, project, auditBoundary(filter.From), auditBoundary(filter.To),
		filter.Action, filter.ActorID, filter.Environment, filter.Outcome, filter.ResourceKeyPrefix, afterTime, afterID, limit)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	entries := make([]access.AuditEntry, 0)
	for rows.Next() {
		var entry access.AuditEntry
		if e = rows.Scan(&entry.ID, &entry.ProjectID, &entry.ActorID, &entry.ActorKind, &entry.Action, &entry.ResourceKey,
			&entry.CredentialID, &entry.Environment, &entry.Outcome, &entry.OccurredAt); e != nil {
			return nil, e
		}
		entry.OccurredAt = entry.OccurredAt.UTC()
		entries = append(entries, entry)
	}
	if e = rows.Err(); e != nil {
		return nil, e
	}
	if e = tx.Commit(ctx); e != nil {
		return nil, e
	}
	return entries, nil
}
