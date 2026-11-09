package postgres

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/howtomakeaname/Evia/backend/internal/access"
	"github.com/howtomakeaname/Evia/backend/internal/platform/fault"
	"github.com/jackc/pgx/v5"
)

var _ access.SettingsStore = (*Store)(nil)

// settingsTx locks membership/account before the project, matching catalog and
// membership mutation ordering. Authorization precedes data and replay reads.
func (s *Store) settingsTx(ctx context.Context, actor, project string, write bool) (pgx.Tx, error) {
	if _, machine := access.IdentityFromContext(ctx); machine {
		return nil, machineDenied()
	}
	tx, e := s.Pool.Begin(ctx)
	if e != nil {
		return nil, e
	}
	var role string
	e = tx.QueryRow(ctx, `SELECT m.role FROM project_members m JOIN accounts a ON a.id=m.user_id
 WHERE m.project_id=$1 AND m.user_id=$2 AND NOT a.disabled FOR SHARE OF m,a`, project, actor).Scan(&role)
	if errors.Is(e, pgx.ErrNoRows) {
		e = projectInvisible()
	}
	if e == nil && write && role != "owner" {
		e = fault.New(fault.Forbidden, "PROJECT_SETTINGS_FORBIDDEN", "只有项目所有者可以修改项目设置")
	}
	if e == nil && write {
		_, e = tx.Exec(ctx, `SELECT id FROM projects WHERE id=$1 FOR UPDATE`, project)
	}
	if e == nil {
		e = setAuditCredential(ctx, tx, "")
	}
	if e != nil {
		tx.Rollback(context.Background())
		return nil, e
	}
	return tx, nil
}

func (s *Store) GetProjectSettings(ctx context.Context, actor, project string) (access.ProjectSettings, error) {
	var out access.ProjectSettings
	tx, e := s.settingsTx(ctx, actor, project, false)
	if e != nil {
		return out, e
	}
	defer tx.Rollback(context.Background())
	e = tx.QueryRow(ctx, `SELECT id::text,name,timezone,settings_revision FROM projects WHERE id=$1`, project).Scan(&out.ProjectID, &out.Name, &out.Timezone, &out.Revision)
	if e != nil {
		return out, e
	}
	return out, tx.Commit(ctx)
}

func settingsRevisionConflict() error {
	return fault.New(fault.Conflict, "PROJECT_SETTINGS_REVISION_CONFLICT", "项目设置修订已变化或已达上限，请重新加载")
}

func (s *Store) UpdateProjectSettings(ctx context.Context, actor, project string, revision int64, key, hash string, in access.SettingsUpdate) (access.ProjectSettings, error) {
	var out access.ProjectSettings
	tx, e := s.settingsTx(ctx, actor, project, true)
	if e != nil {
		return out, e
	}
	defer tx.Rollback(context.Background())
	var priorHash string
	var response []byte
	e = tx.QueryRow(ctx, `SELECT request_hash,response FROM project_governance_mutations
 WHERE actor_id=$1 AND project_id=$2 AND operation='settings.update' AND idempotency_key=$3`, actor, project, key).Scan(&priorHash, &response)
	if e == nil {
		if priorHash != hash {
			return out, fault.New(fault.Conflict, "IDEMPOTENCY_CONFLICT", "幂等键已用于不同请求")
		}
		if e = json.Unmarshal(response, &out); e != nil {
			return out, e
		}
		return out, tx.Commit(ctx)
	}
	if !errors.Is(e, pgx.ErrNoRows) {
		return out, e
	}
	e = tx.QueryRow(ctx, `UPDATE projects SET name=$3,timezone=$4,settings_revision=settings_revision+1
 WHERE id=$1 AND settings_revision=$2 AND settings_revision<9007199254740991
 RETURNING id::text,name,timezone,settings_revision`, project, revision, in.Name, in.Timezone).Scan(&out.ProjectID, &out.Name, &out.Timezone, &out.Revision)
	if errors.Is(e, pgx.ErrNoRows) {
		return out, settingsRevisionConflict()
	}
	if e != nil {
		return out, e
	}
	if _, e = tx.Exec(ctx, `INSERT INTO audit_entries(project_id,actor_id,action,resource_key)
 VALUES($1,$2,'project.settings_updated',$3)`, project, actor, project); e != nil {
		return out, e
	}
	response, e = json.Marshal(out)
	if e != nil {
		return out, e
	}
	if _, e = tx.Exec(ctx, `INSERT INTO project_governance_mutations(actor_id,project_id,operation,idempotency_key,request_hash,response)
 VALUES($1,$2,'settings.update',$3,$4,$5)`, actor, project, key, hash, response); e != nil {
		return out, e
	}
	return out, tx.Commit(ctx)
}
