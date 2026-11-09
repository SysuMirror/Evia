package postgres

import (
	"context"
	"errors"
	"strings"

	"github.com/howtomakeaname/Evia/backend/internal/access"
	"github.com/howtomakeaname/Evia/backend/internal/platform/fault"
	"github.com/jackc/pgx/v5"
)

func machineDenied() error {
	return fault.New(fault.Forbidden, "MACHINE_FORBIDDEN", "机器主体无权执行此操作")
}
func projectInvisible() error {
	return fault.New(fault.NotFound, "PROJECT_NOT_FOUND", "项目不存在或不可见")
}

func setAuditCredential(ctx context.Context, tx pgx.Tx, credential string) error {
	_, e := tx.Exec(ctx, `SELECT set_config('evia.credential_id',$1,true)`, credential)
	return e
}

// lockMachineIdentity checks the exact bearer credential and keeps revocation
// from racing an authorized transaction. Context metadata never establishes grants.
func lockMachineIdentity(ctx context.Context, tx pgx.Tx, actor, project string) (access.Identity, error) {
	identity, ok := access.IdentityFromContext(ctx)
	if !ok || identity.PrincipalID != actor || identity.CredentialID == "" {
		return access.Identity{}, machineDenied()
	}
	var current access.Identity
	e := tx.QueryRow(ctx, `SELECT m.id::text,c.id::text,m.project_id::text,m.environment,m.name,m.permissions,c.expires_at
 FROM machine_principals m JOIN machine_credentials c ON c.principal_id=m.id JOIN principals p ON p.id=m.id AND p.kind='machine'
 WHERE m.id=$1 AND m.project_id=$2 AND c.id=$3 AND m.revoked_at IS NULL AND c.revoked_at IS NULL AND c.expires_at>clock_timestamp()
 FOR SHARE OF m,c`, actor, project, identity.CredentialID).Scan(&current.PrincipalID, &current.CredentialID, &current.ProjectID, &current.Environment, &current.Name, &current.Permissions, &current.ExpiresAt)
	if errors.Is(e, pgx.ErrNoRows) {
		return access.Identity{}, machineDenied()
	}
	if e != nil {
		return current, e
	}
	if e = setAuditCredential(ctx, tx, current.CredentialID); e != nil {
		return access.Identity{}, e
	}
	return current, nil
}

// authorizeOperation is called with the stored resource environment before
// replay, side effects, or returning data. A baseline project lock is insufficient.
func (s *Store) authorizeOperation(ctx context.Context, tx pgx.Tx, actor, project, environment, permission string) error {
	if !access.KnownMachinePermission(permission) {
		return machineDenied()
	}
	identity, _ := access.IdentityFromContext(ctx)
	if identity.PrincipalID != "" || identity.CredentialID != "" {
		current, e := lockMachineIdentity(ctx, tx, actor, project)
		if e != nil {
			return e
		}
		if environment == "" {
			if permission != access.PermissionProjectRead && permission != access.PermissionCatalogDraftsRead && permission != access.PermissionCatalogDraftsWrite {
				return machineDenied()
			}
		} else if current.Environment != environment {
			return machineDenied()
		}
		for _, p := range current.Permissions {
			if p == permission {
				return nil
			}
		}
		return machineDenied()
	}
	var role string
	e := tx.QueryRow(ctx, `SELECT m.role FROM project_members m JOIN accounts a ON a.id=m.user_id WHERE m.project_id=$1 AND m.user_id=$2 AND NOT a.disabled FOR SHARE OF m,a`, project, actor).Scan(&role)
	if errors.Is(e, pgx.ErrNoRows) {
		return projectInvisible()
	}
	if e != nil {
		return e
	}
	write := permission == access.PermissionQueriesRawRead || strings.HasSuffix(permission, ".write") || strings.HasSuffix(permission, ".publish") || strings.HasSuffix(permission, ".activate") || strings.HasSuffix(permission, ".rotate")
	if write && role != "owner" && role != "editor" {
		return fault.New(fault.Forbidden, "PROJECT_FORBIDDEN", "当前角色不能执行此操作")
	}
	return setAuditCredential(ctx, tx, "")
}
