package postgres

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/howtomakeaname/Evia/backend/internal/analysis"
	"github.com/howtomakeaname/Evia/backend/internal/platform/canonical"
	"github.com/howtomakeaname/Evia/backend/internal/platform/fault"
	"github.com/howtomakeaname/Evia/backend/internal/platform/ids"
	"github.com/howtomakeaname/Evia/backend/internal/reporting"
	"github.com/jackc/pgx/v5"
)

var _ reporting.DashboardStore = (*Store)(nil)

const dashboardColumns = `dashboard_id::text,project_id::text,environment,title,draft_revision,COALESCE(published_version,0),created_at,updated_at`
const dashboardVersionColumns = `dashboard_id::text,project_id::text,environment,version,draft_revision,definition,dependencies,definition_hash,dependency_hash,validation_hash,created_at,created_by::text`

func dashboardMissing() error {
	return fault.New(fault.NotFound, "DASHBOARD_NOT_FOUND", "看板不存在或不可见")
}
func dashboardVersionMissing() error {
	return fault.New(fault.NotFound, "DASHBOARD_VERSION_NOT_FOUND", "看板版本不存在或不可见")
}
func dashboardConflict(code string) error {
	return fault.New(fault.Conflict, code, "看板已变更，请重新加载并校验")
}
func scanDashboard(row pgx.Row) (reporting.Dashboard, error) {
	var out reporting.Dashboard
	e := row.Scan(&out.ID, &out.ProjectID, &out.Environment, &out.Title, &out.DraftRevision, &out.PublishedVersion, &out.CreatedAt, &out.UpdatedAt)
	if errors.Is(e, pgx.ErrNoRows) {
		e = dashboardMissing()
	}
	return out, e
}
func readDashboardDraftTx(ctx context.Context, tx pgx.Tx, project, id string) (reporting.DashboardDraft, error) {
	var out reporting.DashboardDraft
	var data []byte
	e := tx.QueryRow(ctx, `SELECT `+dashboardColumns+`,definition FROM dashboards WHERE project_id=$1 AND dashboard_id=$2`, project, id).Scan(&out.ID, &out.ProjectID, &out.Environment, &out.Title, &out.DraftRevision, &out.PublishedVersion, &out.CreatedAt, &out.UpdatedAt, &data)
	if errors.Is(e, pgx.ErrNoRows) {
		return out, dashboardMissing()
	}
	if e != nil {
		return out, e
	}
	e = decodeSaved(data, &out.Definition)
	return out, e
}
func scanDashboardVersion(row pgx.Row) (reporting.DashboardVersion, error) {
	var out reporting.DashboardVersion
	var def, deps []byte
	e := row.Scan(&out.DashboardID, &out.ProjectID, &out.Environment, &out.Version, &out.DraftRevision, &def, &deps, &out.DefinitionHash, &out.DependencyHash, &out.ValidationHash, &out.CreatedAt, &out.CreatedBy)
	if errors.Is(e, pgx.ErrNoRows) {
		return out, dashboardVersionMissing()
	}
	if e != nil {
		return out, e
	}
	if e = decodeSaved(def, &out.Definition); e != nil {
		return out, e
	}
	e = decodeSaved(deps, &out.Dependencies)
	return out, e
}

// Caller must authorize current project membership and hold the project lock
// required for its operation. Published rows are immutable.
func readDashboardVersionTx(ctx context.Context, tx pgx.Tx, project, id string, version int64) (reporting.DashboardVersion, error) {
	return scanDashboardVersion(tx.QueryRow(ctx, `SELECT `+dashboardVersionColumns+` FROM dashboard_versions WHERE project_id=$1 AND dashboard_id=$2 AND version=$3`, project, id, version))
}
func dashboardIdempotencyReplay(ctx context.Context, tx pgx.Tx, actor, project, environment, operation, key, hash string, out any) (bool, error) {
	var old string
	var data []byte
	e := tx.QueryRow(ctx, `SELECT request_hash,response FROM dashboard_idempotency WHERE actor_id=$1 AND project_id=$2 AND environment=$3 AND operation=$4 AND idempotency_key=$5`, actor, project, environment, operation, key).Scan(&old, &data)
	if errors.Is(e, pgx.ErrNoRows) {
		return false, nil
	}
	if e != nil {
		return false, e
	}
	if old != hash {
		return false, fault.New(fault.Conflict, "IDEMPOTENCY_CONFLICT", "幂等键已用于不同请求")
	}
	return true, decodeSaved(data, out)
}
func dashboardIdempotencyWrite(ctx context.Context, tx pgx.Tx, actor, project, environment, operation, key, hash string, out any) error {
	data, e := json.Marshal(out)
	if e != nil {
		return e
	}
	_, e = tx.Exec(ctx, `INSERT INTO dashboard_idempotency(actor_id,project_id,environment,operation,idempotency_key,request_hash,response) VALUES($1,$2,$3,$4,$5,$6,$7)`, actor, project, environment, operation, key, hash, data)
	return e
}
func (s *Store) CreateDashboard(ctx context.Context, actor, project, key string, in reporting.DashboardCreate) (reporting.DashboardDraft, error) {
	var out reporting.DashboardDraft
	tx, e := s.catalogTx(ctx, actor, project, true)
	if e != nil {
		return out, e
	}
	defer tx.Rollback(context.Background())
	if e = savedKey(key); e != nil {
		return out, e
	}
	if e = savedEnvironment(in.Environment); e != nil {
		return out, e
	}
	in.Definition, e = reporting.NormalizeDashboardDefinition(in.Definition)
	if e != nil {
		return out, e
	}
	hash, e := canonical.Hash(in)
	if e != nil {
		return out, e
	}
	replay, e := dashboardIdempotencyReplay(ctx, tx, actor, project, in.Environment, "create", key, hash, &out)
	if e != nil {
		return out, e
	}
	if replay {
		return out, tx.Commit(ctx)
	}
	data, e := json.Marshal(in.Definition)
	if e != nil {
		return out, e
	}
	id := ids.New()
	_, e = tx.Exec(ctx, `INSERT INTO dashboards(project_id,dashboard_id,environment,title,definition) VALUES($1,$2,$3,$4,$5)`, project, id, in.Environment, in.Definition.Title, data)
	if e != nil {
		return out, e
	}
	out, e = readDashboardDraftTx(ctx, tx, project, id)
	if e != nil {
		return out, e
	}
	if _, e = tx.Exec(ctx, `INSERT INTO audit_entries(project_id,actor_id,action,resource_key) VALUES($1,$2,'dashboard.created',$3)`, project, actor, id); e != nil {
		return out, e
	}
	if e = dashboardIdempotencyWrite(ctx, tx, actor, project, in.Environment, "create", key, hash, out); e != nil {
		return out, e
	}
	return out, tx.Commit(ctx)
}
func (s *Store) ListDashboards(ctx context.Context, actor, project, environment, after string, limit int) ([]reporting.Dashboard, error) {
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
	rows, e := tx.Query(ctx, `SELECT `+dashboardColumns+` FROM dashboards WHERE project_id=$1 AND environment=$2 AND ($3='' OR dashboard_id>NULLIF($3,'')::uuid) ORDER BY dashboard_id LIMIT $4`, project, environment, after, limit)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []reporting.Dashboard{}
	for rows.Next() {
		v, e := scanDashboard(rows)
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
func (s *Store) GetDashboard(ctx context.Context, actor, project, id string) (reporting.Dashboard, error) {
	tx, e := s.catalogTx(ctx, actor, project, false)
	if e != nil {
		return reporting.Dashboard{}, e
	}
	defer tx.Rollback(context.Background())
	out, e := scanDashboard(tx.QueryRow(ctx, `SELECT `+dashboardColumns+` FROM dashboards WHERE project_id=$1 AND dashboard_id=$2`, project, id))
	if e != nil {
		return out, e
	}
	return out, tx.Commit(ctx)
}
func (s *Store) GetDashboardDraft(ctx context.Context, actor, project, id string) (reporting.DashboardDraft, error) {
	tx, e := s.catalogTx(ctx, actor, project, false)
	if e != nil {
		return reporting.DashboardDraft{}, e
	}
	defer tx.Rollback(context.Background())
	out, e := readDashboardDraftTx(ctx, tx, project, id)
	if e != nil {
		return out, e
	}
	return out, tx.Commit(ctx)
}
func (s *Store) SaveDashboardDraft(ctx context.Context, actor, project, id string, revision int64, in reporting.DashboardDefinition) (reporting.DashboardDraft, error) {
	var out reporting.DashboardDraft
	tx, e := s.catalogTx(ctx, actor, project, true)
	if e != nil {
		return out, e
	}
	defer tx.Rollback(context.Background())
	old, e := readDashboardDraftTx(ctx, tx, project, id)
	if e != nil {
		return out, e
	}
	if revision < 1 {
		return out, fault.New(fault.Invalid, "REVISION_REQUIRED", "需要草稿修订号")
	}
	if old.DraftRevision != revision {
		return out, dashboardConflict("DASHBOARD_REVISION_CONFLICT")
	}
	if revision >= analysis.MaxSafeInteger {
		return out, dashboardConflict("DASHBOARD_REVISION_EXHAUSTED")
	}
	in, e = reporting.NormalizeDashboardDefinition(in)
	if e != nil {
		return out, e
	}
	data, e := json.Marshal(in)
	if e != nil {
		return out, e
	}
	if _, e = tx.Exec(ctx, `UPDATE dashboards SET definition=$3,title=$4,draft_revision=draft_revision+1,updated_at=clock_timestamp() WHERE project_id=$1 AND dashboard_id=$2`, project, id, data, in.Title); e != nil {
		return out, e
	}
	if _, e = tx.Exec(ctx, `INSERT INTO audit_entries(project_id,actor_id,action,resource_key) VALUES($1,$2,'dashboard.draft_saved',$3)`, project, actor, id); e != nil {
		return out, e
	}
	out, e = readDashboardDraftTx(ctx, tx, project, id)
	if e != nil {
		return out, e
	}
	return out, tx.Commit(ctx)
}

func dashboardSnapshotTx(ctx context.Context, tx pgx.Tx, project, id string, in reporting.DashboardValidate) (reporting.DashboardSnapshot, error) {
	var out reporting.DashboardSnapshot
	var e error
	out.Draft, e = readDashboardDraftTx(ctx, tx, project, id)
	if e != nil {
		return out, e
	}
	if in.DraftRevision != out.Draft.DraftRevision {
		return out, dashboardConflict("DASHBOARD_REVISION_CONFLICT")
	}
	if in.ExpectedPublishedVersion != out.Draft.PublishedVersion {
		return out, dashboardConflict("PUBLISHED_VERSION_CONFLICT")
	}
	if out.Draft.PublishedVersion > 0 {
		v, e := readDashboardVersionTx(ctx, tx, project, id, out.Draft.PublishedVersion)
		if e != nil {
			return out, e
		}
		out.Baseline = &v
	}
	out.Draft.Definition, e = reporting.NormalizeDashboardDefinition(out.Draft.Definition)
	if e != nil {
		return out, e
	}
	out.Dependencies = reporting.DashboardDependencies{ProjectID: project, Environment: out.Draft.Environment, Analyses: []reporting.DashboardDependency{}}
	for _, widget := range out.Draft.Definition.Widgets {
		v, e := readSavedAnalysisVersionTx(ctx, tx, project, widget.AnalysisID, widget.AnalysisVersion)
		if e != nil {
			return out, e
		}
		if v.Environment != out.Draft.Environment {
			return out, analysis.Invalid("DASHBOARD_ANALYSIS_ENVIRONMENT_MISMATCH", "看板组件分析版本不属于当前环境", "/definition/widgets")
		}
		out.Dependencies.Analyses = append(out.Dependencies.Analyses, reporting.DashboardDependency{WidgetID: widget.ID, AnalysisID: widget.AnalysisID, AnalysisVersion: widget.AnalysisVersion, DefinitionHash: v.DefinitionHash, DependencyHash: v.DependencyHash, ValidationHash: v.ValidationHash})
	}
	return out, nil
}
func (s *Store) DashboardValidationSnapshot(ctx context.Context, actor, project, id string, in reporting.DashboardValidate) (reporting.DashboardSnapshot, error) {
	tx, e := s.catalogTx(ctx, actor, project, false)
	if e != nil {
		return reporting.DashboardSnapshot{}, e
	}
	defer tx.Rollback(context.Background())
	if _, e = tx.Exec(ctx, `SELECT id FROM projects WHERE id=$1 FOR SHARE`, project); e != nil {
		return reporting.DashboardSnapshot{}, e
	}
	out, e := dashboardSnapshotTx(ctx, tx, project, id, in)
	if e != nil {
		return out, e
	}
	return out, tx.Commit(ctx)
}
func (s *Store) PublishDashboard(ctx context.Context, actor, project, id, key string, in reporting.DashboardPublish) (reporting.DashboardVersion, error) {
	var out reporting.DashboardVersion
	tx, e := s.catalogTx(ctx, actor, project, true)
	if e != nil {
		return out, e
	}
	defer tx.Rollback(context.Background())
	if e = savedKey(key); e != nil {
		return out, e
	}
	draft, e := readDashboardDraftTx(ctx, tx, project, id)
	if e != nil {
		return out, e
	}
	operation := "publish:" + id
	hash, e := canonical.Hash(struct {
		DashboardID string                     `json:"dashboard_id"`
		Input       reporting.DashboardPublish `json:"input"`
	}{id, in})
	if e != nil {
		return out, e
	}
	replay, e := dashboardIdempotencyReplay(ctx, tx, actor, project, draft.Environment, operation, key, hash, &out)
	if e != nil {
		return out, e
	}
	if replay {
		return out, tx.Commit(ctx)
	}
	snapshot, e := dashboardSnapshotTx(ctx, tx, project, id, reporting.DashboardValidate{DraftRevision: in.DraftRevision, ExpectedPublishedVersion: in.ExpectedPublishedVersion})
	if e != nil {
		return out, e
	}
	validation, e := reporting.BuildDashboardValidation(snapshot)
	if e != nil {
		return out, e
	}
	if validation.ValidationHash != in.ValidationHash {
		return out, dashboardConflict("VALIDATION_HASH_CONFLICT")
	}
	if draft.PublishedVersion >= analysis.MaxSafeInteger {
		return out, dashboardConflict("DASHBOARD_VERSION_EXHAUSTED")
	}
	version := draft.PublishedVersion + 1
	definition, e := json.Marshal(validation.NormalizedDefinition)
	if e != nil {
		return out, e
	}
	deps, e := json.Marshal(validation.Dependencies)
	if e != nil {
		return out, e
	}
	if _, e = tx.Exec(ctx, `INSERT INTO dashboard_versions(project_id,dashboard_id,environment,version,draft_revision,definition,dependencies,definition_hash,dependency_hash,validation_hash,created_by) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)`, project, id, draft.Environment, version, draft.DraftRevision, definition, deps, validation.DefinitionHash, validation.DependencyHash, validation.ValidationHash, actor); e != nil {
		return out, e
	}
	for _, d := range validation.Dependencies.Analyses {
		if _, e = tx.Exec(ctx, `INSERT INTO dashboard_dependencies(project_id,environment,dashboard_id,version,widget_id,analysis_id,analysis_version,definition_hash,dependency_hash,validation_hash) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`, project, draft.Environment, id, version, d.WidgetID, d.AnalysisID, d.AnalysisVersion, d.DefinitionHash, d.DependencyHash, d.ValidationHash); e != nil {
			return out, e
		}
	}
	if _, e = tx.Exec(ctx, `UPDATE dashboards SET published_version=$3,updated_at=clock_timestamp() WHERE project_id=$1 AND dashboard_id=$2`, project, id, version); e != nil {
		return out, e
	}
	if _, e = tx.Exec(ctx, `INSERT INTO audit_entries(project_id,actor_id,action,resource_key) VALUES($1,$2,'dashboard.published',$3)`, project, actor, id); e != nil {
		return out, e
	}
	out, e = readDashboardVersionTx(ctx, tx, project, id, version)
	if e != nil {
		return out, e
	}
	if e = dashboardIdempotencyWrite(ctx, tx, actor, project, draft.Environment, operation, key, hash, out); e != nil {
		return out, e
	}
	return out, tx.Commit(ctx)
}
func (s *Store) GetDashboardVersion(ctx context.Context, actor, project, id string, version int64) (reporting.DashboardVersion, error) {
	tx, e := s.catalogTx(ctx, actor, project, false)
	if e != nil {
		return reporting.DashboardVersion{}, e
	}
	defer tx.Rollback(context.Background())
	out, e := readDashboardVersionTx(ctx, tx, project, id, version)
	if e != nil {
		return out, e
	}
	return out, tx.Commit(ctx)
}
func (s *Store) ListDashboardVersions(ctx context.Context, actor, project, id string, after int64, limit int) ([]reporting.DashboardVersion, error) {
	if limit < 1 || limit > 51 {
		return nil, analysis.Invalid("INVALID_LIMIT", "分页大小超出范围", "/limit")
	}
	if after < 0 || after > analysis.MaxSafeInteger {
		return nil, analysis.Invalid("INVALID_CURSOR", "分页游标无效", "/cursor")
	}
	tx, e := s.catalogTx(ctx, actor, project, false)
	if e != nil {
		return nil, e
	}
	defer tx.Rollback(context.Background())
	if _, e = scanDashboard(tx.QueryRow(ctx, `SELECT `+dashboardColumns+` FROM dashboards WHERE project_id=$1 AND dashboard_id=$2`, project, id)); e != nil {
		return nil, e
	}
	rows, e := tx.Query(ctx, `SELECT `+dashboardVersionColumns+` FROM dashboard_versions WHERE project_id=$1 AND dashboard_id=$2 AND version>$3 ORDER BY version LIMIT $4`, project, id, after, limit)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []reporting.DashboardVersion{}
	for rows.Next() {
		v, e := scanDashboardVersion(rows)
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
