package postgres

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/howtomakeaname/Evia/backend/internal/access"
	"github.com/howtomakeaname/Evia/backend/internal/ingestion"
	"github.com/howtomakeaname/Evia/backend/internal/platform/fault"
	"github.com/jackc/pgx/v5"
)

var _ ingestion.SourceStore = (*Store)(nil)

const sourceColumns = `id::text,project_id::text,environment,name,kind,write_key,status,revision,allowed_origins,CASE WHEN observed_at+interval '2 seconds'<=clock_timestamp() THEN 'applied' ELSE 'pending' END`

type rowScanner interface{ Scan(...any) error }

func scanSource(row rowScanner) (ingestion.Source, error) {
	var out ingestion.Source
	e := row.Scan(&out.ID, &out.ProjectID, &out.Environment, &out.Name, &out.Kind, &out.WriteKey, &out.Status, &out.Revision, &out.AllowedOrigins, &out.ApplicationState)
	if errors.Is(e, pgx.ErrNoRows) {
		e = ingestion.MissingSource()
	}
	return out, e
}
func insertSourceCredential(ctx context.Context, tx pgx.Tx, c ingestion.Credential) error {
	if c.SecretCiphertext == nil {
		c.SecretCiphertext = []byte{}
	}
	_, e := tx.Exec(ctx, `INSERT INTO ingestion_credentials(id,source_id,write_key,secret_digest,secret_ciphertext) VALUES($1,$2,$3,$4,$5)`, c.ID, c.SourceID, c.WriteKey, c.SecretDigest, c.SecretCiphertext)
	return e
}
func (s *Store) CreateSource(ctx context.Context, actor, project string, in ingestion.Source, c ingestion.Credential) (ingestion.Source, error) {
	tx, e := s.catalogTx(ctx, actor, project, true)
	if e != nil {
		return ingestion.Source{}, e
	}
	defer tx.Rollback(context.Background())
	if e = s.authorizeOperation(ctx, tx, actor, project, in.Environment, access.PermissionSourcesWrite); e != nil {
		return ingestion.Source{}, e
	}
	out, e := scanSource(tx.QueryRow(ctx, `INSERT INTO ingestion_sources(id,project_id,environment,name,kind,write_key,status,allowed_origins) VALUES($1,$2,$3,$4,$5,$6,'active',$7) RETURNING `+sourceColumns, in.ID, project, in.Environment, in.Name, in.Kind, c.WriteKey, in.AllowedOrigins))
	if e != nil {
		return out, e
	}
	if e = insertSourceCredential(ctx, tx, c); e != nil {
		return out, e
	}
	if _, e = tx.Exec(ctx, `INSERT INTO audit_entries(project_id,actor_id,action,resource_key) VALUES($1,$2,'source.created',$3)`, project, actor, in.ID); e != nil {
		return out, e
	}
	return out, tx.Commit(ctx)
}
func (s *Store) GetSource(ctx context.Context, actor, project, id string) (ingestion.Source, error) {
	return s.readSourceForOperation(ctx, actor, project, id, access.PermissionSourcesRead, true)
}
func (s *Store) GetSourceForUpdate(ctx context.Context, actor, project, id string) (ingestion.Source, error) {
	return s.readSourceForOperation(ctx, actor, project, id, access.PermissionSourcesWrite, false)
}
func (s *Store) GetSourceForRotation(ctx context.Context, actor, project, id string) (ingestion.Source, error) {
	return s.readSourceForOperation(ctx, actor, project, id, access.PermissionSourcesRotate, false)
}
func (s *Store) readSourceForOperation(ctx context.Context, actor, project, id, permission string, observe bool) (ingestion.Source, error) {
	tx, e := s.catalogTx(ctx, actor, project, false)
	if e != nil {
		return ingestion.Source{}, e
	}
	defer tx.Rollback(context.Background())
	var environment string
	e = tx.QueryRow(ctx, `SELECT environment FROM ingestion_sources WHERE project_id=$1 AND id=$2`, project, id).Scan(&environment)
	if errors.Is(e, pgx.ErrNoRows) {
		e = ingestion.MissingSource()
	}
	if e != nil {
		return ingestion.Source{}, e
	}
	if e = s.authorizeOperation(ctx, tx, actor, project, environment, permission); e != nil {
		return ingestion.Source{}, e
	}
	// This observation follows the visible configuration commit. Mutation
	// preflights read metadata without marking configuration as observed.
	if observe {
		if _, e = tx.Exec(ctx, `UPDATE ingestion_sources SET observed_at=clock_timestamp() WHERE project_id=$1 AND id=$2 AND observed_at IS NULL`, project, id); e != nil {
			return ingestion.Source{}, e
		}
	}
	out, e := scanSource(tx.QueryRow(ctx, `SELECT `+sourceColumns+` FROM ingestion_sources WHERE project_id=$1 AND id=$2`, project, id))
	if e != nil {
		return out, e
	}
	return out, tx.Commit(ctx)
}
func (s *Store) ListSources(ctx context.Context, actor, project, env, after string, limit int) ([]ingestion.Source, error) {
	if limit < 1 || limit > 201 {
		return nil, ingestion.Invalid("INVALID_LIMIT", "分页大小无效")
	}
	tx, e := s.catalogTx(ctx, actor, project, false)
	if e != nil {
		return nil, e
	}
	defer tx.Rollback(context.Background())
	if e = s.authorizeOperation(ctx, tx, actor, project, env, access.PermissionSourcesRead); e != nil {
		return nil, e
	}
	if _, e = tx.Exec(ctx, `UPDATE ingestion_sources SET observed_at=clock_timestamp() WHERE observed_at IS NULL AND id IN (SELECT id FROM ingestion_sources WHERE project_id=$1 AND ($2='' OR environment=$2) AND ($3='' OR id>nullif($3,'')::uuid) ORDER BY id LIMIT $4)`, project, env, after, limit); e != nil {
		return nil, e
	}
	rows, e := tx.Query(ctx, `SELECT `+sourceColumns+` FROM ingestion_sources WHERE project_id=$1 AND ($2='' OR environment=$2) AND ($3='' OR id>nullif($3,'')::uuid) ORDER BY id LIMIT $4`, project, env, after, limit)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := make([]ingestion.Source, 0)
	for rows.Next() {
		v, e := scanSource(rows)
		if e != nil {
			return nil, e
		}
		out = append(out, v)
	}
	if e = rows.Err(); e != nil {
		return nil, e
	}
	rows.Close()
	return out, tx.Commit(ctx)
}
func (s *Store) UpdateSource(ctx context.Context, actor, project, id string, revision int64, in ingestion.SourceUpdate) (ingestion.Source, error) {
	tx, e := s.catalogTx(ctx, actor, project, true)
	if e != nil {
		return ingestion.Source{}, e
	}
	defer tx.Rollback(context.Background())
	out, e := scanSource(tx.QueryRow(ctx, `SELECT `+sourceColumns+` FROM ingestion_sources WHERE project_id=$1 AND id=$2 FOR UPDATE`, project, id))
	if e != nil {
		return out, e
	}
	if e = s.authorizeOperation(ctx, tx, actor, project, out.Environment, access.PermissionSourcesWrite); e != nil {
		return ingestion.Source{}, e
	}
	if out.Revision != revision {
		return out, ingestion.RevisionConflict()
	}
	if in.Status != nil {
		out.Status = *in.Status
	}
	if in.AllowedOrigins != nil {
		out.AllowedOrigins = *in.AllowedOrigins
	}
	if out.Kind == ingestion.SourceKindBrowser && len(out.AllowedOrigins) == 0 {
		return out, ingestion.Invalid("INVALID_ORIGINS", "浏览器来源必须配置至少一个 Origin")
	}
	if out.Kind == ingestion.SourceKindServer && len(out.AllowedOrigins) != 0 {
		return out, ingestion.Invalid("INVALID_ORIGINS", "服务端来源不使用浏览器 Origin 配置")
	}
	out, e = scanSource(tx.QueryRow(ctx, `UPDATE ingestion_sources SET status=$3,allowed_origins=$4,revision=revision+1,updated_at=clock_timestamp(),observed_at=NULL WHERE project_id=$1 AND id=$2 RETURNING `+sourceColumns, project, id, out.Status, out.AllowedOrigins))
	if e != nil {
		return out, e
	}
	if _, e = tx.Exec(ctx, `INSERT INTO audit_entries(project_id,actor_id,action,resource_key) VALUES($1,$2,'source.updated',$3)`, project, actor, id); e != nil {
		return out, e
	}
	return out, tx.Commit(ctx)
}
func (s *Store) RotateSource(ctx context.Context, actor, project, id, key string, overlap int64, c ingestion.Credential) (ingestion.Source, ingestion.Credential, error) {
	tx, e := s.catalogTx(ctx, actor, project, true)
	if e != nil {
		return ingestion.Source{}, ingestion.Credential{}, e
	}
	defer tx.Rollback(context.Background())
	out, e := scanSource(tx.QueryRow(ctx, `SELECT `+sourceColumns+` FROM ingestion_sources WHERE project_id=$1 AND id=$2 FOR UPDATE`, project, id))
	if e != nil {
		return out, c, e
	}
	if e = s.authorizeOperation(ctx, tx, actor, project, out.Environment, access.PermissionSourcesRotate); e != nil {
		return ingestion.Source{}, ingestion.Credential{}, e
	}
	var previousOverlap int64
	var snapshot []byte
	var credentialID string
	e = tx.QueryRow(ctx, `SELECT overlap_seconds,source_snapshot,credential_id::text FROM ingestion_rotations WHERE actor_id=$1 AND project_id=$2 AND source_id=$3 AND idempotency_key=$4`, actor, project, id, key).Scan(&previousOverlap, &snapshot, &credentialID)
	if e == nil {
		if previousOverlap != overlap {
			return out, c, fault.New(fault.Conflict, "IDEMPOTENCY_CONFLICT", "幂等键已用于不同的请求")
		}
		if e = json.Unmarshal(snapshot, &out); e != nil {
			return out, c, e
		}
		c, e = readCredential(ctx, tx, credentialID)
		if e != nil {
			return out, c, e
		}
		return out, c, tx.Commit(ctx)
	}
	if !errors.Is(e, pgx.ErrNoRows) {
		return out, c, e
	}
	if out.Revision >= ingestion.MaxRevision {
		return out, c, ingestion.RevisionConflict()
	}
	if _, e = tx.Exec(ctx, `UPDATE ingestion_credentials SET expires_at=least(COALESCE(expires_at,'infinity'::timestamptz),now()+$2*interval '1 second') WHERE source_id=$1`, id, overlap); e != nil {
		return out, c, e
	}
	if e = insertSourceCredential(ctx, tx, c); e != nil {
		return out, c, e
	}
	out, e = scanSource(tx.QueryRow(ctx, `UPDATE ingestion_sources SET write_key=$3,revision=revision+1,updated_at=clock_timestamp(),observed_at=NULL WHERE project_id=$1 AND id=$2 RETURNING `+sourceColumns, project, id, c.WriteKey))
	if e != nil {
		return out, c, e
	}
	snapshot, e = json.Marshal(out)
	if e != nil {
		return out, c, e
	}
	if _, e = tx.Exec(ctx, `INSERT INTO ingestion_rotations(actor_id,project_id,environment,source_id,idempotency_key,overlap_seconds,credential_id,source_snapshot) VALUES($1,$2,$3,$4,$5,$6,$7,$8)`, actor, project, out.Environment, id, key, overlap, c.ID, snapshot); e != nil {
		return out, c, e
	}
	if _, e = tx.Exec(ctx, `INSERT INTO audit_entries(project_id,actor_id,action,resource_key) VALUES($1,$2,'source.credential_rotated',$3)`, project, actor, id); e != nil {
		return out, c, e
	}
	return out, c, tx.Commit(ctx)
}
func readCredential(ctx context.Context, tx pgx.Tx, id string) (ingestion.Credential, error) {
	var c ingestion.Credential
	e := tx.QueryRow(ctx, `SELECT id::text,source_id::text,write_key,secret_digest,secret_ciphertext,expires_at FROM ingestion_credentials WHERE id=$1`, id).Scan(&c.ID, &c.SourceID, &c.WriteKey, &c.SecretDigest, &c.SecretCiphertext, &c.ExpiresAt)
	return c, e
}
func (s *Store) ResolveSource(ctx context.Context, key string) (ingestion.AdmissionSource, error) {
	var out ingestion.AdmissionSource
	e := s.Pool.QueryRow(ctx, `SELECT s.id::text,s.project_id::text,s.environment,s.name,s.kind,s.write_key,s.status,s.revision,s.allowed_origins,CASE WHEN s.observed_at+interval '2 seconds'<=clock_timestamp() THEN 'applied' ELSE 'pending' END,c.id::text,c.source_id::text,c.write_key,c.secret_digest,c.secret_ciphertext,c.expires_at FROM ingestion_credentials c JOIN ingestion_sources s ON s.id=c.source_id WHERE c.write_key=$1 AND (c.expires_at IS NULL OR c.expires_at>now())`, key).Scan(&out.Source.ID, &out.Source.ProjectID, &out.Source.Environment, &out.Source.Name, &out.Source.Kind, &out.Source.WriteKey, &out.Source.Status, &out.Source.Revision, &out.Source.AllowedOrigins, &out.Source.ApplicationState, &out.Credential.ID, &out.Credential.SourceID, &out.Credential.WriteKey, &out.Credential.SecretDigest, &out.Credential.SecretCiphertext, &out.Credential.ExpiresAt)
	if errors.Is(e, pgx.ErrNoRows) {
		e = fault.New(fault.Unauthenticated, "SOURCE_CREDENTIAL_INVALID", "来源凭据无效")
	}
	return out, e
}
