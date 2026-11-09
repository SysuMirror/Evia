package postgres

import (
	"context"
	"errors"
	"time"

	"github.com/howtomakeaname/Evia/backend/internal/access"
	"github.com/howtomakeaname/Evia/backend/internal/platform/fault"
	"github.com/jackc/pgx/v5"
)

var _ access.MachineStore = (*Store)(nil)

const machinePrincipalColumns = `id::text,project_id::text,environment,name,permissions,revision,created_at,revoked_at`
const machineCredentialColumns = `id::text,principal_id::text,created_at,expires_at,revoked_at`

func machineMissing() error {
	return fault.New(fault.NotFound, "MACHINE_PRINCIPAL_NOT_FOUND", "机器主体不存在或不可见")
}
func machineRevision() error {
	return fault.New(fault.Conflict, "MACHINE_REVISION_CONFLICT", "机器主体修订已变化，请重新加载")
}
func machineRevoked() error {
	return fault.New(fault.Conflict, "MACHINE_PRINCIPAL_REVOKED", "机器主体已永久撤销")
}
func scanMachinePrincipal(row queryScanner) (access.MachinePrincipal, error) {
	var p access.MachinePrincipal
	e := row.Scan(&p.ID, &p.ProjectID, &p.Environment, &p.Name, &p.Permissions, &p.Revision, &p.CreatedAt, &p.RevokedAt)
	if errors.Is(e, pgx.ErrNoRows) {
		e = machineMissing()
	}
	return p, e
}
func scanMachineCredential(row queryScanner) (access.MachineCredential, error) {
	var c access.MachineCredential
	e := row.Scan(&c.ID, &c.PrincipalID, &c.CreatedAt, &c.ExpiresAt, &c.RevokedAt)
	if errors.Is(e, pgx.ErrNoRows) {
		e = fault.New(fault.NotFound, "MACHINE_CREDENTIAL_NOT_FOUND", "机器凭据不存在或不可见")
	}
	return c, e
}
func (s *Store) machineOwnerTx(ctx context.Context, actor, project string) (pgx.Tx, error) {
	identity, _ := access.IdentityFromContext(ctx)
	if identity.PrincipalID != "" || identity.CredentialID != "" {
		return nil, machineDenied()
	}
	// Lifecycle locks the target principal before the project, matching machine
	// mutations. Holding the project first would deadlock a revoking owner.
	tx, e := s.catalogTx(ctx, actor, project, false)
	if e != nil {
		return nil, e
	}
	var role string
	e = tx.QueryRow(ctx, `SELECT role FROM project_members WHERE project_id=$1 AND user_id=$2`, project, actor).Scan(&role)
	if e == nil && role != "owner" {
		e = fault.New(fault.Forbidden, "MACHINE_MANAGEMENT_FORBIDDEN", "只有项目所有者可以管理机器主体")
	}
	if e != nil {
		tx.Rollback(context.Background())
		return nil, e
	}
	return tx, nil
}
func machineProjectLock(ctx context.Context, tx pgx.Tx, project string) error {
	_, e := tx.Exec(ctx, `SELECT id FROM projects WHERE id=$1 FOR UPDATE`, project)
	return e
}
func readMachinePrincipal(ctx context.Context, tx pgx.Tx, project, id string, lock bool) (access.MachinePrincipal, error) {
	query := `SELECT ` + machinePrincipalColumns + ` FROM machine_principals WHERE project_id=$1 AND id=$2`
	if lock {
		query += ` FOR UPDATE`
	}
	return scanMachinePrincipal(tx.QueryRow(ctx, query, project, id))
}
func (s *Store) ListMachinePrincipals(ctx context.Context, actor, project, after string, limit int) ([]access.MachinePrincipal, error) {
	if limit < 1 || limit > 201 {
		return nil, fault.New(fault.Invalid, "INVALID_LIMIT", "分页大小超出范围")
	}
	tx, e := s.machineOwnerTx(ctx, actor, project)
	if e != nil {
		return nil, e
	}
	defer tx.Rollback(context.Background())
	rows, e := tx.Query(ctx, `SELECT `+machinePrincipalColumns+` FROM machine_principals WHERE project_id=$1 AND ($2='' OR id>nullif($2,'')::uuid) ORDER BY id LIMIT $3`, project, after, limit)
	if e != nil {
		return nil, e
	}
	out := []access.MachinePrincipal{}
	for rows.Next() {
		p, e := scanMachinePrincipal(rows)
		if e != nil {
			rows.Close()
			return nil, e
		}
		out = append(out, p)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return nil, e
	}
	return out, tx.Commit(ctx)
}
func (s *Store) GetMachinePrincipal(ctx context.Context, actor, project, id string) (access.MachinePrincipalDetail, error) {
	var out access.MachinePrincipalDetail
	tx, e := s.machineOwnerTx(ctx, actor, project)
	if e != nil {
		return out, e
	}
	defer tx.Rollback(context.Background())
	out.Principal, e = readMachinePrincipal(ctx, tx, project, id, false)
	if e != nil {
		return out, e
	}
	rows, e := tx.Query(ctx, `SELECT `+machineCredentialColumns+` FROM machine_credentials WHERE principal_id=$1 ORDER BY created_at,id LIMIT 128`, id)
	if e != nil {
		return out, e
	}
	out.Credentials = []access.MachineCredential{}
	for rows.Next() {
		c, e := scanMachineCredential(rows)
		if e != nil {
			rows.Close()
			return out, e
		}
		out.Credentials = append(out.Credentials, c)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return out, e
	}
	return out, tx.Commit(ctx)
}
func replayMachineIssuance(ctx context.Context, tx pgx.Tx, actor, project, operation, key, hash string) (access.MachineIssued, bool, error) {
	var out access.MachineIssued
	var old, id, credential string
	e := tx.QueryRow(ctx, `SELECT request_hash,principal_id::text,credential_id::text FROM machine_idempotency WHERE actor_id=$1 AND project_id=$2 AND operation=$3 AND idempotency_key=$4`, actor, project, operation, key).Scan(&old, &id, &credential)
	if errors.Is(e, pgx.ErrNoRows) {
		return out, false, nil
	}
	if e != nil {
		return out, false, e
	}
	if old != hash {
		return out, false, fault.New(fault.Conflict, "IDEMPOTENCY_CONFLICT", "幂等键已用于不同请求")
	}
	out.Principal, e = readMachinePrincipal(ctx, tx, project, id, false)
	if e != nil {
		return out, false, e
	}
	out.Credential, e = scanMachineCredential(tx.QueryRow(ctx, `SELECT `+machineCredentialColumns+` FROM machine_credentials WHERE id=$1 AND principal_id=$2`, credential, id))
	return out, true, e
}
func writeMachineIssuance(ctx context.Context, tx pgx.Tx, actor, project, operation, key, hash string, out access.MachineIssued) error {
	_, e := tx.Exec(ctx, `INSERT INTO machine_idempotency(actor_id,project_id,operation,idempotency_key,request_hash,principal_id,credential_id) VALUES($1,$2,$3,$4,$5,$6,$7)`, actor, project, operation, key, hash, out.Principal.ID, out.Credential.ID)
	return e
}
func insertMachineCredential(ctx context.Context, tx pgx.Tx, c access.MachineCredential, digest string) (access.MachineCredential, error) {
	return scanMachineCredential(tx.QueryRow(ctx, `INSERT INTO machine_credentials(id,principal_id,secret_digest,expires_at) VALUES($1,$2,$3,$4) RETURNING `+machineCredentialColumns, c.ID, c.PrincipalID, digest, c.ExpiresAt))
}
func checkMachineExpiry(ctx context.Context, tx pgx.Tx, expiry time.Time) error {
	var now time.Time
	if e := tx.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&now); e != nil {
		return e
	}
	return access.ValidateMachineCredentialExpiry(expiry, now)
}
func machineAudit(ctx context.Context, tx pgx.Tx, project, actor, action, resource string) error {
	_, e := tx.Exec(ctx, `INSERT INTO audit_entries(project_id,actor_id,action,resource_key) VALUES($1,$2,$3,$4)`, project, actor, action, resource)
	return e
}
func (s *Store) CreateMachinePrincipal(ctx context.Context, actor, project, key, hash string, p access.MachinePrincipal, c access.MachineCredential, digest string) (access.MachineIssued, bool, error) {
	var out access.MachineIssued
	tx, e := s.machineOwnerTx(ctx, actor, project)
	if e != nil {
		return out, false, e
	}
	defer tx.Rollback(context.Background())
	if e = machineProjectLock(ctx, tx, project); e != nil {
		return out, false, e
	}
	out, replay, e := replayMachineIssuance(ctx, tx, actor, project, "create", key, hash)
	if e != nil {
		return out, false, e
	}
	if replay {
		return out, false, tx.Commit(ctx)
	}
	if e = checkMachineExpiry(ctx, tx, c.ExpiresAt); e != nil {
		return out, false, e
	}
	var count int
	if e = tx.QueryRow(ctx, `SELECT count(*) FROM machine_principals WHERE project_id=$1`, project).Scan(&count); e != nil {
		return out, false, e
	}
	if count >= 256 {
		return out, false, fault.New(fault.Limited, "MACHINE_PRINCIPAL_LIMIT", "项目机器主体数量已达到上限")
	}
	if _, e = tx.Exec(ctx, `INSERT INTO principals(id,kind) VALUES($1,'machine')`, p.ID); e != nil {
		return out, false, e
	}
	out.Principal, e = scanMachinePrincipal(tx.QueryRow(ctx, `INSERT INTO machine_principals(id,project_id,environment,name,permissions,created_by) VALUES($1,$2,$3,$4,$5,$6) RETURNING `+machinePrincipalColumns, p.ID, project, p.Environment, p.Name, p.Permissions, actor))
	if e != nil {
		return out, false, e
	}
	out.Credential, e = insertMachineCredential(ctx, tx, c, digest)
	if e != nil {
		return out, false, e
	}
	if e = writeMachineIssuance(ctx, tx, actor, project, "create", key, hash, out); e != nil {
		return out, false, e
	}
	if e = machineAudit(ctx, tx, project, actor, "machine.principal_created", p.ID); e != nil {
		return out, false, e
	}
	return out, true, tx.Commit(ctx)
}
func (s *Store) RotateMachineCredential(ctx context.Context, actor, project, id string, revision int64, key, hash string, c access.MachineCredential, digest string, overlap int) (access.MachineIssued, bool, error) {
	var out access.MachineIssued
	tx, e := s.machineOwnerTx(ctx, actor, project)
	if e != nil {
		return out, false, e
	}
	defer tx.Rollback(context.Background())
	p, e := readMachinePrincipal(ctx, tx, project, id, true)
	if e != nil {
		return out, false, e
	}
	if e = machineProjectLock(ctx, tx, project); e != nil {
		return out, false, e
	}
	operation := "rotate:" + id
	out, replay, e := replayMachineIssuance(ctx, tx, actor, project, operation, key, hash)
	if e != nil {
		return out, false, e
	}
	if replay {
		return out, false, tx.Commit(ctx)
	}
	if p.RevokedAt != nil {
		return out, false, machineRevoked()
	}
	if p.Revision != revision {
		return out, false, machineRevision()
	}
	if e = checkMachineExpiry(ctx, tx, c.ExpiresAt); e != nil {
		return out, false, e
	}
	var count int
	if e = tx.QueryRow(ctx, `SELECT count(*) FROM machine_credentials WHERE principal_id=$1`, id).Scan(&count); e != nil {
		return out, false, e
	}
	if count >= 128 {
		return out, false, fault.New(fault.Limited, "MACHINE_CREDENTIAL_LIMIT", "机器凭据数量已达到上限，请创建新主体")
	}
	if _, e = tx.Exec(ctx, `UPDATE machine_credentials SET expires_at=LEAST(expires_at,clock_timestamp()+$2::double precision*interval '1 second') WHERE principal_id=$1 AND revoked_at IS NULL AND expires_at>clock_timestamp()`, id, overlap); e != nil {
		return out, false, e
	}
	out.Credential, e = insertMachineCredential(ctx, tx, c, digest)
	if e != nil {
		return out, false, e
	}
	out.Principal, e = scanMachinePrincipal(tx.QueryRow(ctx, `UPDATE machine_principals SET revision=revision+1 WHERE id=$1 RETURNING `+machinePrincipalColumns, id))
	if e != nil {
		return out, false, e
	}
	if e = writeMachineIssuance(ctx, tx, actor, project, operation, key, hash, out); e != nil {
		return out, false, e
	}
	if e = machineAudit(ctx, tx, project, actor, "machine.credential_rotated", out.Credential.ID); e != nil {
		return out, false, e
	}
	return out, true, tx.Commit(ctx)
}
func (s *Store) RevokeMachinePrincipal(ctx context.Context, actor, project, id string, revision int64) (access.MachinePrincipal, error) {
	var p access.MachinePrincipal
	tx, e := s.machineOwnerTx(ctx, actor, project)
	if e != nil {
		return p, e
	}
	defer tx.Rollback(context.Background())
	p, e = readMachinePrincipal(ctx, tx, project, id, true)
	if e != nil {
		return p, e
	}
	if p.RevokedAt != nil {
		return p, tx.Commit(ctx)
	}
	if p.Revision != revision {
		return p, machineRevision()
	}
	if e = machineProjectLock(ctx, tx, project); e != nil {
		return p, e
	}
	if _, e = tx.Exec(ctx, `UPDATE machine_credentials SET revoked_at=clock_timestamp() WHERE principal_id=$1 AND revoked_at IS NULL`, id); e != nil {
		return p, e
	}
	p, e = scanMachinePrincipal(tx.QueryRow(ctx, `UPDATE machine_principals SET revoked_at=clock_timestamp(),revision=revision+1 WHERE id=$1 RETURNING `+machinePrincipalColumns, id))
	if e != nil {
		return p, e
	}
	if e = machineAudit(ctx, tx, project, actor, "machine.principal_revoked", id); e != nil {
		return p, e
	}
	return p, tx.Commit(ctx)
}
func (s *Store) RevokeMachineCredential(ctx context.Context, actor, project, id, credential string, revision int64) (access.MachinePrincipal, error) {
	var p access.MachinePrincipal
	tx, e := s.machineOwnerTx(ctx, actor, project)
	if e != nil {
		return p, e
	}
	defer tx.Rollback(context.Background())
	p, e = readMachinePrincipal(ctx, tx, project, id, true)
	if e != nil {
		return p, e
	}
	c, e := scanMachineCredential(tx.QueryRow(ctx, `SELECT `+machineCredentialColumns+` FROM machine_credentials WHERE principal_id=$1 AND id=$2 FOR UPDATE`, id, credential))
	if e != nil {
		return p, e
	}
	if c.RevokedAt != nil {
		return p, tx.Commit(ctx)
	}
	if p.Revision != revision {
		return p, machineRevision()
	}
	if e = machineProjectLock(ctx, tx, project); e != nil {
		return p, e
	}
	if _, e = tx.Exec(ctx, `UPDATE machine_credentials SET revoked_at=clock_timestamp() WHERE id=$1`, credential); e != nil {
		return p, e
	}
	p, e = scanMachinePrincipal(tx.QueryRow(ctx, `UPDATE machine_principals SET revision=revision+1 WHERE id=$1 RETURNING `+machinePrincipalColumns, id))
	if e != nil {
		return p, e
	}
	if e = machineAudit(ctx, tx, project, actor, "machine.credential_revoked", credential); e != nil {
		return p, e
	}
	return p, tx.Commit(ctx)
}
func (s *Store) FindMachineIdentity(ctx context.Context, digest string) (access.Identity, error) {
	var out access.Identity
	e := s.Pool.QueryRow(ctx, `SELECT m.id::text,c.id::text,m.project_id::text,m.environment,m.name,m.permissions,c.expires_at FROM machine_credentials c JOIN machine_principals m ON m.id=c.principal_id JOIN principals p ON p.id=m.id AND p.kind='machine' WHERE c.secret_digest=$1 AND c.revoked_at IS NULL AND c.expires_at>clock_timestamp() AND m.revoked_at IS NULL`, digest).Scan(&out.PrincipalID, &out.CredentialID, &out.ProjectID, &out.Environment, &out.Name, &out.Permissions, &out.ExpiresAt)
	if errors.Is(e, pgx.ErrNoRows) {
		e = access.ErrMissing
	}
	return out, e
}
