package postgres

import (
	"context"
	"errors"
	"time"

	"github.com/howtomakeaname/Evia/backend/internal/access"
	"github.com/howtomakeaname/Evia/backend/internal/delivery"
	"github.com/howtomakeaname/Evia/backend/internal/platform/fault"
	"github.com/jackc/pgx/v5"
)

var _ delivery.Store = (*Store)(nil)

func activationConflict() error {
	return fault.New(fault.Conflict, "ACTIVE_REVISION_CONFLICT", "环境的激活版本已变化，请重新加载后重试")
}
func activationReplay(ctx context.Context, tx pgx.Tx, actor, project, key, hash, environment string) (*delivery.Activation, error) {
	var actual string
	var out delivery.Activation
	e := tx.QueryRow(ctx, `SELECT i.request_hash,r.environment,r.revision FROM catalog_idempotency i JOIN catalog_releases r ON r.id=i.release_id WHERE i.actor_id=$1 AND i.project_id=$2 AND i.environment=$3 AND i.operation='release.activate' AND i.idempotency_key=$4`, actor, project, environment, key).Scan(&actual, &out.Environment, &out.ActiveReleaseRevision)
	if errors.Is(e, pgx.ErrNoRows) {
		return nil, nil
	}
	if e != nil {
		return nil, e
	}
	if actual != hash {
		return nil, fault.New(fault.Conflict, "IDEMPOTENCY_CONFLICT", "幂等键已用于不同的激活请求")
	}
	return &out, nil
}

const deliveryCandidateColumns = `r.id::text,r.project_id::text,r.environment,r.revision,r.manifest_hash,r.manifest,COALESCE(d.ingestion_ready AND rd.ready_until>clock_timestamp(),false),COALESCE(rd.ready_until,'epoch'::timestamptz)`
const deliveryCandidateTables = `catalog_releases r JOIN catalog_release_delivery d ON d.release_id=r.id AND d.project_id=r.project_id AND d.environment=r.environment LEFT JOIN catalog_release_readiness rd ON rd.release_id=r.id AND rd.manifest_hash=r.manifest_hash`

func candidateScan(row pgx.Row) (delivery.Candidate, error) {
	var c delivery.Candidate
	e := row.Scan(&c.ReleaseID, &c.ProjectID, &c.Environment, &c.Revision, &c.ManifestHash, &c.Manifest, &c.Ready, &c.ReadyUntil)
	return c, e
}
func (s *Store) PrepareActivation(ctx context.Context, actor, project, key, hash string, in delivery.ActivationInput) (delivery.Candidate, *delivery.Activation, error) {
	tx, e := s.catalogTx(ctx, actor, project, true)
	if e != nil {
		return delivery.Candidate{}, nil, e
	}
	defer tx.Rollback(context.Background())
	if e = s.authorizeOperation(ctx, tx, actor, project, in.Environment, access.PermissionCatalogReleasesActivate); e != nil {
		return delivery.Candidate{}, nil, e
	}
	replay, e := activationReplay(ctx, tx, actor, project, key, hash, in.Environment)
	if e != nil {
		return delivery.Candidate{}, nil, e
	}
	if replay != nil {
		return delivery.Candidate{}, replay, tx.Commit(ctx)
	}
	candidate, e := candidateScan(tx.QueryRow(ctx, `SELECT `+deliveryCandidateColumns+` FROM `+deliveryCandidateTables+` WHERE r.project_id=$1 AND r.environment=$2 AND r.id=$3`, project, in.Environment, in.ReleaseID))
	if errors.Is(e, pgx.ErrNoRows) {
		e = releaseMissing()
	}
	if e != nil {
		return candidate, nil, e
	}
	var active int64
	if e = tx.QueryRow(ctx, `SELECT active_revision FROM catalog_environments WHERE project_id=$1 AND environment=$2`, project, in.Environment).Scan(&active); e != nil {
		return candidate, nil, e
	}
	if active != in.ExpectedActiveRevision {
		return candidate, nil, activationConflict()
	}
	if candidate.Revision <= active {
		return candidate, nil, fault.New(fault.Conflict, "RELEASE_REVISION_NOT_NEWER", "只能激活更高版本，回滚请构建新发布")
	}
	return candidate, nil, tx.Commit(ctx)
}
func applyReadiness(ctx context.Context, tx pgx.Tx, c delivery.Candidate, p delivery.Proof, lease time.Duration) error {
	if p.ProjectID != c.ProjectID || p.Environment != c.Environment || p.ReleaseID != c.ReleaseID || p.Revision != c.Revision || p.ManifestHash != c.ManifestHash {
		return delivery.Unavailable("PIPELINE_PROOF_SCOPE_MISMATCH")
	}
	if e := delivery.ValidateProof(p.ProbeRequest, p, time.Now().UTC()); e != nil {
		return e
	}
	result, e := tx.Exec(ctx, `INSERT INTO delivery_probe_consumptions(nonce,release_id,manifest_hash,observed_at) VALUES($1,$2,$3,$4) ON CONFLICT DO NOTHING`, p.Nonce, c.ReleaseID, c.ManifestHash, p.ObservedAt)
	if e != nil {
		return e
	}
	if result.RowsAffected() != 1 {
		return delivery.Unavailable("PIPELINE_PROOF_REPLAYED")
	}
	_, e = tx.Exec(ctx, `INSERT INTO catalog_release_readiness(release_id,project_id,environment,manifest_hash,nonce,capability,confirmed_at,ready_until) VALUES($1,$2,$3,$4,$5,$6,$7,clock_timestamp()+$8::bigint*interval '1 millisecond') ON CONFLICT(release_id) DO UPDATE SET manifest_hash=EXCLUDED.manifest_hash,nonce=EXCLUDED.nonce,capability=EXCLUDED.capability,confirmed_at=EXCLUDED.confirmed_at,ready_until=EXCLUDED.ready_until`, c.ReleaseID, c.ProjectID, c.Environment, c.ManifestHash, p.Nonce, p.Capability, p.ObservedAt, lease.Milliseconds())
	if e != nil {
		return e
	}
	_, e = tx.Exec(ctx, `UPDATE catalog_release_delivery SET ingestion_ready=true,updated_at=clock_timestamp(),next_probe_at=clock_timestamp() WHERE release_id=$1 AND project_id=$2 AND environment=$3`, c.ReleaseID, c.ProjectID, c.Environment)
	return e
}
func (s *Store) CommitActivation(ctx context.Context, actor, project, key, hash string, in delivery.ActivationInput, candidate delivery.Candidate, proof delivery.Proof, window, lease time.Duration) (delivery.Activation, error) {
	tx, e := s.catalogTx(ctx, actor, project, true)
	if e != nil {
		return delivery.Activation{}, e
	}
	defer tx.Rollback(context.Background())
	if e = s.authorizeOperation(ctx, tx, actor, project, in.Environment, access.PermissionCatalogReleasesActivate); e != nil {
		return delivery.Activation{}, e
	}
	replay, e := activationReplay(ctx, tx, actor, project, key, hash, in.Environment)
	if e != nil {
		return delivery.Activation{}, e
	}
	if replay != nil {
		return *replay, tx.Commit(ctx)
	}
	actual, e := candidateScan(tx.QueryRow(ctx, `SELECT `+deliveryCandidateColumns+` FROM `+deliveryCandidateTables+` WHERE r.project_id=$1 AND r.environment=$2 AND r.id=$3`, project, in.Environment, in.ReleaseID))
	if e != nil {
		return delivery.Activation{}, e
	}
	if actual.ManifestHash != candidate.ManifestHash || actual.Revision != candidate.Revision {
		return delivery.Activation{}, delivery.Unavailable("RELEASE_MANIFEST_CHANGED")
	}
	var active int64
	if e = tx.QueryRow(ctx, `SELECT active_revision FROM catalog_environments WHERE project_id=$1 AND environment=$2 FOR UPDATE`, project, in.Environment).Scan(&active); e != nil {
		return delivery.Activation{}, e
	}
	if active != in.ExpectedActiveRevision {
		return delivery.Activation{}, activationConflict()
	}
	if actual.Revision <= active {
		return delivery.Activation{}, fault.New(fault.Conflict, "RELEASE_REVISION_NOT_NEWER", "只能激活更高版本")
	}
	if e = applyReadiness(ctx, tx, actual, proof, lease); e != nil {
		return delivery.Activation{}, e
	}
	if _, e = tx.Exec(ctx, `UPDATE catalog_release_activations SET accept_until=clock_timestamp()+$4::bigint*interval '1 millisecond' WHERE project_id=$1 AND environment=$2 AND revision=$3 AND accept_until IS NULL`, project, in.Environment, active, window.Milliseconds()); e != nil {
		return delivery.Activation{}, e
	}
	if _, e = tx.Exec(ctx, `INSERT INTO catalog_release_activations(release_id,project_id,environment,revision) VALUES($1,$2,$3,$4)`, actual.ReleaseID, project, in.Environment, actual.Revision); e != nil {
		return delivery.Activation{}, e
	}
	if _, e = tx.Exec(ctx, `UPDATE catalog_environments SET active_revision=$3 WHERE project_id=$1 AND environment=$2`, project, in.Environment, actual.Revision); e != nil {
		return delivery.Activation{}, e
	}
	if _, e = tx.Exec(ctx, `INSERT INTO catalog_idempotency(actor_id,project_id,environment,operation,idempotency_key,request_hash,release_id) VALUES($1,$2,$3,'release.activate',$4,$5,$6)`, actor, project, in.Environment, key, hash, actual.ReleaseID); e != nil {
		return delivery.Activation{}, e
	}
	if _, e = tx.Exec(ctx, `INSERT INTO audit_entries(project_id,actor_id,action,resource_key) VALUES($1,$2,'catalog.release_activated',$3)`, project, actor, actual.ReleaseID); e != nil {
		return delivery.Activation{}, e
	}
	if _, e = tx.Exec(ctx, `UPDATE catalog_outbox SET delivered_at=COALESCE(delivered_at,clock_timestamp()) WHERE release_id=$1 AND kind='catalog.release_built'`, actual.ReleaseID); e != nil {
		return delivery.Activation{}, e
	}
	out := delivery.Activation{Environment: in.Environment, ActiveReleaseRevision: actual.Revision}
	return out, tx.Commit(ctx)
}

// ListReadiness leases a bounded fair scan so failed oldest releases cannot
// permanently starve later projects, and multiple workers do not duplicate work.
func (s *Store) ListReadiness(ctx context.Context, limit int) ([]delivery.Candidate, error) {
	if limit < 1 || limit > 128 {
		return nil, errors.New("invalid readiness scan bound")
	}
	tx, e := s.Pool.Begin(ctx)
	if e != nil {
		return nil, e
	}
	defer tx.Rollback(context.Background())
	rows, e := tx.Query(ctx, `SELECT `+deliveryCandidateColumns+` FROM `+deliveryCandidateTables+` LEFT JOIN catalog_release_activations a ON a.release_id=r.id JOIN catalog_environments env ON env.project_id=r.project_id AND env.environment=r.environment WHERE (r.revision>=env.active_revision OR a.accept_until>clock_timestamp()) AND d.next_probe_at<=clock_timestamp() AND COALESCE(rd.ready_until,'epoch'::timestamptz)<clock_timestamp()+interval '30 seconds' ORDER BY COALESCE(rd.ready_until,'epoch'::timestamptz),r.id LIMIT $1 FOR UPDATE OF d SKIP LOCKED`, limit)
	if e != nil {
		return nil, e
	}
	out := []delivery.Candidate{}
	for rows.Next() {
		c, e := candidateScan(rows)
		if e != nil {
			rows.Close()
			return nil, e
		}
		out = append(out, c)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return nil, e
	}
	for _, c := range out {
		if _, e = tx.Exec(ctx, `UPDATE catalog_release_delivery SET next_probe_at=clock_timestamp()+interval '2 minutes' WHERE release_id=$1`, c.ReleaseID); e != nil {
			return nil, e
		}
	}
	return out, tx.Commit(ctx)
}
func (s *Store) RenewReadiness(ctx context.Context, c delivery.Candidate, p delivery.Proof, lease time.Duration) error {
	tx, e := s.Pool.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(context.Background())
	var hash string
	e = tx.QueryRow(ctx, `SELECT r.manifest_hash FROM catalog_releases r LEFT JOIN catalog_release_activations a ON a.release_id=r.id JOIN catalog_environments env ON env.project_id=r.project_id AND env.environment=r.environment WHERE r.id=$1 AND r.project_id=$2 AND r.environment=$3 AND r.revision=$4 AND (r.revision>=env.active_revision OR a.accept_until>clock_timestamp()) FOR SHARE OF env`, c.ReleaseID, c.ProjectID, c.Environment, c.Revision).Scan(&hash)
	if errors.Is(e, pgx.ErrNoRows) {
		return delivery.Unavailable("RELEASE_NOT_ELIGIBLE")
	}
	if e != nil {
		return e
	}
	if hash != c.ManifestHash {
		return delivery.Unavailable("RELEASE_MANIFEST_CHANGED")
	}
	if e = applyReadiness(ctx, tx, c, p, lease); e != nil {
		return e
	}
	if _, e = tx.Exec(ctx, `UPDATE catalog_outbox SET delivered_at=COALESCE(delivered_at,clock_timestamp()) WHERE release_id=$1 AND kind='catalog.release_built'`, c.ReleaseID); e != nil {
		return e
	}
	if _, e = tx.Exec(ctx, `DELETE FROM delivery_probe_consumptions WHERE nonce IN (SELECT nonce FROM delivery_probe_consumptions WHERE consumed_at<clock_timestamp()-interval '1 hour' LIMIT 1000)`); e != nil {
		return e
	}
	return tx.Commit(ctx)
}

// GetActivation reads current authority and the active revision in one transaction.
// An environment without a release has revision zero after authorization.
func (s *Store) GetActivation(ctx context.Context, actor, project, environment string) (delivery.Activation, error) {
	tx, e := s.catalogTx(ctx, actor, project, false)
	if e != nil {
		return delivery.Activation{}, e
	}
	defer tx.Rollback(context.Background())
	if e = s.authorizeOperation(ctx, tx, actor, project, environment, access.PermissionCatalogReleasesRead); e != nil {
		return delivery.Activation{}, e
	}
	out := delivery.Activation{Environment: environment}
	e = tx.QueryRow(ctx, `SELECT COALESCE((SELECT active_revision FROM catalog_environments WHERE project_id=$1 AND environment=$2),0)`, project, environment).Scan(&out.ActiveReleaseRevision)
	if e != nil {
		return delivery.Activation{}, e
	}
	return out, tx.Commit(ctx)
}
