package postgres

import (
	"context"
	"errors"

	"github.com/howtomakeaname/Evia/backend/internal/delivery"
	"github.com/howtomakeaname/Evia/backend/internal/platform/fault"
	"github.com/jackc/pgx/v5"
)

const configSourceQuery = `SELECT s.id::text,s.project_id::text,s.environment,s.kind,s.status,s.allowed_origins,c.secret_digest,c.expires_at FROM ingestion_credentials c JOIN ingestion_sources s ON s.id=c.source_id WHERE c.write_key=$1 AND (c.expires_at IS NULL OR c.expires_at>clock_timestamp())`

func scanConfigSource(row pgx.Row) (delivery.Source, error) {
	var source delivery.Source
	e := row.Scan(&source.ID, &source.ProjectID, &source.Environment, &source.Kind, &source.Status, &source.AllowedOrigins, &source.SecretDigest, &source.ExpiresAt)
	if errors.Is(e, pgx.ErrNoRows) {
		e = fault.New(fault.Unauthenticated, "SOURCE_CREDENTIAL_INVALID", "来源凭据无效")
	}
	return source, e
}
func (s *Store) ConfigSource(ctx context.Context, key string) (delivery.Source, error) {
	return scanConfigSource(s.Pool.QueryRow(ctx, configSourceQuery, key))
}
func (s *Store) ReadConfig(ctx context.Context, key string, current int64) (delivery.ConfigSnapshot, error) {
	var out delivery.ConfigSnapshot
	tx, e := s.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if e != nil {
		return out, e
	}
	defer tx.Rollback(context.Background())
	out.Source, e = scanConfigSource(tx.QueryRow(ctx, configSourceQuery, key))
	if e != nil {
		return out, e
	}
	out.Active, e = candidateScan(tx.QueryRow(ctx, `SELECT `+deliveryCandidateColumns+` FROM `+deliveryCandidateTables+` JOIN catalog_environments env ON env.project_id=r.project_id AND env.environment=r.environment AND env.active_revision=r.revision JOIN catalog_release_activations a ON a.release_id=r.id WHERE r.project_id=$1 AND r.environment=$2`, out.Source.ProjectID, out.Source.Environment))
	if errors.Is(e, pgx.ErrNoRows) {
		return out, delivery.Unavailable("RELEASE_NOT_ACTIVE")
	}
	if e != nil {
		return out, e
	}
	if current > 0 && current != out.Active.Revision {
		candidate, err := candidateScan(tx.QueryRow(ctx, `SELECT `+deliveryCandidateColumns+` FROM `+deliveryCandidateTables+` JOIN catalog_release_activations a ON a.release_id=r.id WHERE r.project_id=$1 AND r.environment=$2 AND r.revision=$3`, out.Source.ProjectID, out.Source.Environment, current))
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return out, err
		}
		if err == nil {
			out.Current = &candidate
			if e = tx.QueryRow(ctx, `SELECT accept_until>clock_timestamp() FROM catalog_release_activations WHERE release_id=$1`, candidate.ReleaseID).Scan(&out.CurrentAccepted); e != nil {
				return out, e
			}
		}
	}
	return out, tx.Commit(ctx)
}
