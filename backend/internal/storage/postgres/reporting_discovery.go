package postgres

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/howtomakeaname/Evia/backend/internal/analysis"
	"github.com/howtomakeaname/Evia/backend/internal/platform/fault"
	"github.com/howtomakeaname/Evia/backend/internal/reporting"
	"github.com/jackc/pgx/v5"
)

var _ reporting.DiscoveryStore = (*Store)(nil)

func (s *Store) AuthorizeAnalysisDiscovery(ctx context.Context, actor, project string) error {
	tx, e := s.catalogTx(ctx, actor, project, false)
	if e != nil {
		return e
	}
	defer tx.Rollback(context.Background())
	return tx.Commit(ctx)
}
func discoveryRevision(ctx context.Context, tx pgx.Tx, project, environment string) (int64, error) {
	var revision int64
	e := tx.QueryRow(ctx, `SELECT COALESCE((SELECT metadata_revision FROM catalog_environments WHERE project_id=$1 AND environment=$2),0)`, project, environment).Scan(&revision)
	return revision, e
}
func (s *Store) ListAnalysisCatalog(ctx context.Context, actor, project, environment, afterKey string, afterVersion int64, limit int) (reporting.CatalogSnapshot, error) {
	if environment != "development" && environment != "staging" && environment != "production" {
		return reporting.CatalogSnapshot{}, analysis.Invalid("INVALID_ENVIRONMENT", "环境无效", "/environment")
	}
	if limit < 1 || limit > 201 {
		return reporting.CatalogSnapshot{}, analysis.Invalid("INVALID_LIMIT", "分页大小超出范围", "/limit")
	}
	tx, e := s.catalogTx(ctx, actor, project, false)
	if e != nil {
		return reporting.CatalogSnapshot{}, e
	}
	defer tx.Rollback(context.Background())
	// Publications and activations take the project update lock. A shared lock
	// also protects the empty environment case before its first publication.
	if _, e = tx.Exec(ctx, `SELECT id FROM projects WHERE id=$1 FOR SHARE`, project); e != nil {
		return reporting.CatalogSnapshot{}, e
	}
	revision, e := discoveryRevision(ctx, tx, project, environment)
	if e != nil {
		return reporting.CatalogSnapshot{}, e
	}
	rows, e := tx.Query(ctx, `SELECT v.event_key,v.schema_version,COALESCE(v.definition->>'display_name',''),v.definition_hash,v.created_at,v.definition,
	 EXISTS(SELECT 1 FROM catalog_release_events re JOIN catalog_releases r ON r.id=re.release_id JOIN catalog_environments env ON env.project_id=r.project_id AND env.environment=r.environment AND env.active_revision=r.revision WHERE re.project_id=v.project_id AND re.environment=$2 AND re.event_key=v.event_key AND re.schema_version=v.schema_version),
	 EXISTS(SELECT 1 FROM catalog_release_events re JOIN catalog_releases r ON r.id=re.release_id JOIN catalog_environments env ON env.project_id=r.project_id AND env.environment=r.environment AND env.active_revision=r.revision JOIN catalog_release_delivery d ON d.release_id=r.id JOIN catalog_release_readiness rd ON rd.release_id=r.id AND rd.project_id=r.project_id AND rd.environment=r.environment AND rd.manifest_hash=r.manifest_hash WHERE re.project_id=v.project_id AND re.environment=$2 AND re.event_key=v.event_key AND re.schema_version=v.schema_version AND d.ingestion_ready AND rd.ready_until>clock_timestamp())
	 FROM catalog_versions v WHERE v.project_id=$1 AND (v.event_key,v.schema_version)>($3,$4)
	 AND EXISTS(SELECT 1 FROM catalog_release_events re WHERE re.project_id=v.project_id AND re.environment=$2 AND re.event_key=v.event_key AND re.schema_version=v.schema_version)
	 ORDER BY v.event_key,v.schema_version LIMIT $5`, project, environment, afterKey, afterVersion, limit)
	if e != nil {
		return reporting.CatalogSnapshot{}, e
	}
	defer rows.Close()
	out := reporting.CatalogSnapshot{Revision: revision, Items: []reporting.PublishedDefinition{}}
	for rows.Next() {
		var item reporting.PublishedDefinition
		var data []byte
		if e = rows.Scan(&item.Event.EventKey, &item.Event.SchemaVersion, &item.Event.DisplayName, &item.Event.DefinitionHash, &item.Event.CreatedAt, &data, &item.Event.Active, &item.Event.IngestionReady); e != nil {
			return reporting.CatalogSnapshot{}, e
		}
		if e = json.Unmarshal(data, &item.Definition); e != nil {
			return reporting.CatalogSnapshot{}, e
		}
		item.Event.Published = true
		out.Items = append(out.Items, item)
	}
	if e = rows.Err(); e != nil {
		return reporting.CatalogSnapshot{}, e
	}
	rows.Close()
	if e = tx.Commit(ctx); e != nil {
		return reporting.CatalogSnapshot{}, e
	}
	return out, nil
}
func (s *Store) ValidateAnalysisQuery(ctx context.Context, actor, project string, request analysis.Request) (reporting.ResolvedValidation, error) {
	// Detail access uses the same editor/owner policy as execution without taking
	// a project mutation lock or changing catalog, job or audit tables.
	tx, e := s.catalogTx(ctx, actor, project, false)
	if e != nil {
		return reporting.ResolvedValidation{}, e
	}
	defer tx.Rollback(context.Background())
	if queryDetail(request.Kind) {
		var role string
		if e = tx.QueryRow(ctx, `SELECT role FROM project_members WHERE project_id=$1 AND user_id=$2`, project, actor).Scan(&role); e != nil {
			return reporting.ResolvedValidation{}, e
		}
		if role != "owner" && role != "editor" {
			return reporting.ResolvedValidation{}, fault.New(fault.Forbidden, "PROJECT_FORBIDDEN", "当前角色不能查看用户事件明细")
		}
	}
	if _, e = tx.Exec(ctx, `SELECT id FROM projects WHERE id=$1 FOR SHARE`, project); e != nil {
		return reporting.ResolvedValidation{}, e
	}
	if e = resolveQueryFields(ctx, tx, project, &request); e != nil {
		return reporting.ResolvedValidation{}, e
	}
	revision, e := discoveryRevision(ctx, tx, project, request.Environment)
	if e != nil {
		return reporting.ResolvedValidation{}, e
	}
	versions := map[analysis.EventVersion]bool{}
	for _, version := range request.EventVersions {
		versions[version] = true
	}
	if request.Funnel != nil {
		for _, step := range request.Funnel.Steps {
			versions[analysis.EventVersion{EventKey: step.EventKey, SchemaVersion: step.SchemaVersion}] = true
		}
	}
	if e = analysis.WalkFields(&request, func(f *analysis.Field) error {
		if f.Kind == "property" {
			versions[analysis.EventVersion{EventKey: f.EventKey, SchemaVersion: f.SchemaVersion}] = true
		}
		return nil
	}); e != nil {
		return reporting.ResolvedValidation{}, e
	}
	out := reporting.ResolvedValidation{Request: request, CatalogRevision: revision, Dependencies: []reporting.Dependency{}}
	for version := range versions {
		var hash string
		e = tx.QueryRow(ctx, `SELECT v.definition_hash FROM catalog_versions v WHERE v.project_id=$1 AND v.event_key=$2 AND v.schema_version=$3 AND EXISTS(SELECT 1 FROM catalog_release_events re WHERE re.project_id=v.project_id AND re.environment=$4 AND re.event_key=v.event_key AND re.schema_version=v.schema_version)`, project, version.EventKey, version.SchemaVersion, request.Environment).Scan(&hash)
		if errors.Is(e, pgx.ErrNoRows) {
			return reporting.ResolvedValidation{}, analysis.Invalid("QUERY_EVENT_VERSION_NOT_PUBLISHED", "事件版本不存在或尚未发布")
		}
		if e != nil {
			return reporting.ResolvedValidation{}, e
		}
		out.Dependencies = append(out.Dependencies, reporting.Dependency{EventKey: version.EventKey, SchemaVersion: version.SchemaVersion, DefinitionHash: hash})
	}
	if e = tx.Commit(ctx); e != nil {
		return reporting.ResolvedValidation{}, e
	}
	return out, nil
}
