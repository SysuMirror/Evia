package postgres

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"

	"github.com/howtomakeaname/Evia/backend/internal/analysis"
	"github.com/howtomakeaname/Evia/backend/internal/platform/canonical"
	"github.com/howtomakeaname/Evia/backend/internal/platform/fault"
	"github.com/howtomakeaname/Evia/backend/internal/platform/ids"
	"github.com/howtomakeaname/Evia/backend/internal/reporting"
)

var _ reporting.DashboardFolderStore = (*Store)(nil)

const dashboardFolderColumns = `folder_id::text,project_id::text,environment,COALESCE(parent_id::text,''),depth,title,revision,created_by::text,created_at,updated_at`

func dashboardFolderMissing() error {
	return fault.New(fault.NotFound, "DASHBOARD_FOLDER_NOT_FOUND", "文件夹不存在或不可见")
}
func scanDashboardFolder(row pgx.Row) (reporting.DashboardFolder, error) {
	var out reporting.DashboardFolder
	e := row.Scan(&out.ID, &out.ProjectID, &out.Environment, &out.ParentID, &out.Depth, &out.Title, &out.Revision, &out.CreatedBy, &out.CreatedAt, &out.UpdatedAt)
	if errors.Is(e, pgx.ErrNoRows) {
		e = dashboardFolderMissing()
	}
	return out, e
}

// assertDashboardFolderTx verifies that a non-empty folder reference exists in
// the same project and environment. Cross-scope and missing rows share the same
// not-found answer so the API never probes foreign environments.
func assertDashboardFolderTx(ctx context.Context, tx pgx.Tx, project, environment, folderID string) error {
	if folderID == "" {
		return nil
	}
	var existing string
	e := tx.QueryRow(ctx, `SELECT environment FROM dashboard_folders WHERE project_id=$1 AND folder_id=$2`, project, folderID).Scan(&existing)
	if errors.Is(e, pgx.ErrNoRows) {
		return dashboardFolderMissing()
	}
	if e != nil {
		return e
	}
	if existing != environment {
		return dashboardFolderMissing()
	}
	return nil
}

func (s *Store) CreateDashboardFolder(ctx context.Context, actor, project, key string, in reporting.DashboardFolderCreate) (reporting.DashboardFolder, error) {
	var out reporting.DashboardFolder
	tx, e := s.catalogTx(ctx, actor, project, true)
	if e != nil {
		return out, e
	}
	defer tx.Rollback(context.Background())
	if e = s.authorizeOperation(ctx, tx, actor, project, in.Environment, "dashboards.write"); e != nil {
		return out, e
	}
	if e = savedKey(key); e != nil {
		return out, e
	}
	if e = savedEnvironment(in.Environment); e != nil {
		return out, e
	}
	in, e = reporting.NormalizeDashboardFolderCreate(in)
	if e != nil {
		return out, e
	}
	hash, e := canonical.Hash(in)
	if e != nil {
		return out, e
	}
	replay, e := dashboardIdempotencyReplay(ctx, tx, actor, project, in.Environment, "folder:create", key, hash, &out)
	if e != nil {
		return out, e
	}
	if replay {
		return out, tx.Commit(ctx)
	}
	parentID, depth := "", 1
	if in.ParentID != "" {
		var parentDepth int
		var parentEnvironment string
		e = tx.QueryRow(ctx, `SELECT depth,environment FROM dashboard_folders WHERE project_id=$1 AND folder_id=$2`, project, in.ParentID).Scan(&parentDepth, &parentEnvironment)
		if errors.Is(e, pgx.ErrNoRows) {
			return out, fault.New(fault.NotFound, "DASHBOARD_FOLDER_PARENT_NOT_FOUND", "父文件夹不存在或不可见")
		}
		if e != nil {
			return out, e
		}
		if parentEnvironment != in.Environment {
			return out, fault.New(fault.NotFound, "DASHBOARD_FOLDER_PARENT_NOT_FOUND", "父文件夹不存在或不可见")
		}
		if parentDepth >= reporting.MaxDashboardFolderDepth {
			return out, analysis.Invalid("DASHBOARD_FOLDER_TOO_DEEP", "文件夹层级不能超过 3 层", "/parent_id")
		}
		parentID, depth = in.ParentID, parentDepth+1
	}
	id := ids.New()
	if _, e = tx.Exec(ctx, `INSERT INTO dashboard_folders(project_id,folder_id,environment,parent_id,depth,title,created_by) VALUES($1,$2,$3,NULLIF($4,'')::uuid,$5,$6,$7)`, project, id, in.Environment, parentID, depth, in.Title, actor); e != nil {
		return out, e
	}
	if _, e = tx.Exec(ctx, `INSERT INTO audit_entries(project_id,actor_id,action,resource_key) VALUES($1,$2,'dashboard.folder_created',$3)`, project, actor, id); e != nil {
		return out, e
	}
	out, e = scanDashboardFolder(tx.QueryRow(ctx, `SELECT `+dashboardFolderColumns+` FROM dashboard_folders WHERE project_id=$1 AND folder_id=$2`, project, id))
	if e != nil {
		return out, e
	}
	if e = dashboardIdempotencyWrite(ctx, tx, actor, project, in.Environment, "folder:create", key, hash, out); e != nil {
		return out, e
	}
	return out, tx.Commit(ctx)
}

func (s *Store) ListDashboardFolders(ctx context.Context, actor, project, environment, after string, limit int) ([]reporting.DashboardFolder, error) {
	if e := savedEnvironment(environment); e != nil {
		return nil, e
	}
	if limit < 1 || limit > 101 {
		return nil, analysis.Invalid("INVALID_LIMIT", "分页大小超出范围", "/limit")
	}
	tx, e := s.catalogTx(ctx, actor, project, false)
	if e != nil {
		return nil, e
	}
	defer tx.Rollback(context.Background())
	if e = s.authorizeOperation(ctx, tx, actor, project, environment, "dashboards.read"); e != nil {
		return nil, e
	}
	rows, e := tx.Query(ctx, `SELECT `+dashboardFolderColumns+` FROM dashboard_folders WHERE project_id=$1 AND environment=$2 AND ($3='' OR folder_id>NULLIF($3,'')::uuid) ORDER BY folder_id LIMIT $4`, project, environment, after, limit)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []reporting.DashboardFolder{}
	for rows.Next() {
		folder, e := scanDashboardFolder(rows)
		if e != nil {
			return nil, e
		}
		out = append(out, folder)
	}
	if e = rows.Err(); e != nil {
		return nil, e
	}
	rows.Close()
	return out, tx.Commit(ctx)
}

func (s *Store) GetDashboardFolder(ctx context.Context, actor, project, id string) (reporting.DashboardFolder, error) {
	tx, e := s.catalogTx(ctx, actor, project, false)
	if e != nil {
		return reporting.DashboardFolder{}, e
	}
	defer tx.Rollback(context.Background())
	if e = s.authorizeReportingResource(ctx, tx, actor, project, id, "dashboard_folders", "folder_id", "dashboards.read"); e != nil {
		return reporting.DashboardFolder{}, e
	}
	out, e := scanDashboardFolder(tx.QueryRow(ctx, `SELECT `+dashboardFolderColumns+` FROM dashboard_folders WHERE project_id=$1 AND folder_id=$2`, project, id))
	if e != nil {
		return reporting.DashboardFolder{}, e
	}
	return out, tx.Commit(ctx)
}

func (s *Store) UpdateDashboardFolder(ctx context.Context, actor, project, id string, revision int64, in reporting.DashboardFolderUpdate) (reporting.DashboardFolder, error) {
	var out reporting.DashboardFolder
	tx, e := s.catalogTx(ctx, actor, project, true)
	if e != nil {
		return out, e
	}
	defer tx.Rollback(context.Background())
	if e = s.authorizeReportingResource(ctx, tx, actor, project, id, "dashboard_folders", "folder_id", "dashboards.write"); e != nil {
		return out, e
	}
	if revision < 1 {
		return out, fault.New(fault.Invalid, "REVISION_REQUIRED", "需要文件夹修订号")
	}
	in, e = reporting.NormalizeDashboardFolderUpdate(in)
	if e != nil {
		return out, e
	}
	var current int64
	e = tx.QueryRow(ctx, `SELECT revision FROM dashboard_folders WHERE project_id=$1 AND folder_id=$2`, project, id).Scan(&current)
	if errors.Is(e, pgx.ErrNoRows) {
		return out, dashboardFolderMissing()
	}
	if e != nil {
		return out, e
	}
	if current != revision {
		return out, fault.New(fault.Conflict, "DASHBOARD_FOLDER_REVISION_CONFLICT", "文件夹已变更，请重新加载")
	}
	if revision >= analysis.MaxSafeInteger {
		return out, fault.New(fault.Conflict, "DASHBOARD_FOLDER_REVISION_EXHAUSTED", "文件夹修订号已达上限")
	}
	if _, e = tx.Exec(ctx, `UPDATE dashboard_folders SET title=$3,revision=revision+1,updated_at=clock_timestamp() WHERE project_id=$1 AND folder_id=$2`, project, id, in.Title); e != nil {
		return out, e
	}
	if _, e = tx.Exec(ctx, `INSERT INTO audit_entries(project_id,actor_id,action,resource_key) VALUES($1,$2,'dashboard.folder_updated',$3)`, project, actor, id); e != nil {
		return out, e
	}
	out, e = scanDashboardFolder(tx.QueryRow(ctx, `SELECT `+dashboardFolderColumns+` FROM dashboard_folders WHERE project_id=$1 AND folder_id=$2`, project, id))
	if e != nil {
		return out, e
	}
	return out, tx.Commit(ctx)
}

func (s *Store) DeleteDashboardFolder(ctx context.Context, actor, project, id string) error {
	tx, e := s.catalogTx(ctx, actor, project, true)
	if e != nil {
		return e
	}
	defer tx.Rollback(context.Background())
	if e = s.authorizeReportingResource(ctx, tx, actor, project, id, "dashboard_folders", "folder_id", "dashboards.write"); e != nil {
		return e
	}
	var environment string
	e = tx.QueryRow(ctx, `SELECT environment FROM dashboard_folders WHERE project_id=$1 AND folder_id=$2`, project, id).Scan(&environment)
	if errors.Is(e, pgx.ErrNoRows) {
		return dashboardFolderMissing()
	}
	if e != nil {
		return e
	}
	// Explicit emptiness checks produce a stable 409; the RESTRICT foreign keys
	// remain the database-level backstop against races.
	var childFolders, childDashboards int
	if e = tx.QueryRow(ctx, `SELECT count(*) FROM dashboard_folders WHERE project_id=$1 AND parent_id=$2`, project, id).Scan(&childFolders); e != nil {
		return e
	}
	if childFolders > 0 {
		return fault.New(fault.Conflict, "DASHBOARD_FOLDER_NOT_EMPTY", "文件夹仍包含子文件夹，请先移动或删除后再操作")
	}
	if e = tx.QueryRow(ctx, `SELECT count(*) FROM dashboards WHERE project_id=$1 AND folder_id=$2`, project, id).Scan(&childDashboards); e != nil {
		return e
	}
	if childDashboards > 0 {
		return fault.New(fault.Conflict, "DASHBOARD_FOLDER_NOT_EMPTY", "文件夹仍包含看板，请先移动或删除后再操作")
	}
	if _, e = tx.Exec(ctx, `DELETE FROM dashboard_folders WHERE project_id=$1 AND folder_id=$2`, project, id); e != nil {
		return e
	}
	if _, e = tx.Exec(ctx, `INSERT INTO audit_entries(project_id,actor_id,action,resource_key) VALUES($1,$2,'dashboard.folder_deleted',$3)`, project, actor, id); e != nil {
		return e
	}
	return tx.Commit(ctx)
}
