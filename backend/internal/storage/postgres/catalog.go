package postgres

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/howtomakeaname/Evia/backend/internal/access"
	"github.com/howtomakeaname/Evia/backend/internal/catalog"
	"github.com/howtomakeaname/Evia/backend/internal/platform/fault"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

var _ catalog.Store = (*Store)(nil)

func catalogMissing() error {
	return fault.New(fault.NotFound, "EVENT_NOT_FOUND", "事件不存在或不可见")
}
func releaseMissing() error {
	return fault.New(fault.NotFound, "RELEASE_NOT_FOUND", "发布版本不存在或不可见")
}

// Membership rows remain locked until commit so concurrent revocation cannot
// invalidate an authorized mutation halfway through. Project row locks serialize
// draft edits and builds, including multi-event version allocation.
func (s *Store) catalogTx(ctx context.Context, actor, project string, write bool) (pgx.Tx, error) {
	tx, e := s.Pool.Begin(ctx)
	if e != nil {
		return nil, e
	}
	identity, _ := access.IdentityFromContext(ctx)
	if identity.PrincipalID != "" || identity.CredentialID != "" {
		_, e = lockMachineIdentity(ctx, tx, actor, project)
	} else {
		var role string
		e = tx.QueryRow(ctx, `SELECT m.role FROM project_members m JOIN accounts a ON a.id=m.user_id WHERE m.project_id=$1 AND m.user_id=$2 AND NOT a.disabled FOR SHARE OF m,a`, project, actor).Scan(&role)
		if errors.Is(e, pgx.ErrNoRows) {
			e = projectInvisible()
		}
		if e == nil && write && role != "owner" && role != "editor" {
			e = fault.New(fault.Forbidden, "PROJECT_FORBIDDEN", "当前角色不能修改此项目")
		}
		if e == nil {
			e = setAuditCredential(ctx, tx, "")
		}
	}
	if e == nil && write {
		_, e = tx.Exec(ctx, `SELECT id FROM projects WHERE id=$1 FOR UPDATE`, project)
	}
	if e != nil {
		tx.Rollback(context.Background())
		return nil, e
	}
	return tx, nil
}
func (s *Store) CreateEvent(ctx context.Context, actor, project string, in catalog.EventInput) (catalog.Event, error) {
	tx, e := s.catalogTx(ctx, actor, project, true)
	if e != nil {
		return catalog.Event{}, e
	}
	defer tx.Rollback(context.Background())
	if e = s.authorizeOperation(ctx, tx, actor, project, "", access.PermissionCatalogDraftsWrite); e != nil {
		return catalog.Event{}, e
	}
	definition := catalog.DraftInput{DisplayName: &in.DisplayName, Schema: map[string]any{"type": "object", "properties": map[string]any{}, "additionalProperties": false}, FieldMetadata: map[string]any{}, ExampleProperties: map[string]any{}}
	data, e := json.Marshal(definition)
	if e != nil {
		return catalog.Event{}, e
	}
	out := catalog.Event{Key: in.Key, DisplayName: in.DisplayName, PublishedVersions: []int64{}}
	e = tx.QueryRow(ctx, `INSERT INTO catalog_events(project_id,event_key,display_name,description,draft_definition) VALUES($1,$2,$3,$4,$5) RETURNING event_type_id`, project, in.Key, in.DisplayName, in.Description, data).Scan(&out.TypeID)
	var pgerr *pgconn.PgError
	if errors.As(e, &pgerr) && pgerr.Code == "23505" {
		return catalog.Event{}, fault.New(fault.Conflict, "EVENT_KEY_EXISTS", "事件标识已存在")
	}
	if e != nil {
		return catalog.Event{}, e
	}
	if _, e = tx.Exec(ctx, `INSERT INTO audit_entries(project_id,actor_id,action,resource_key) VALUES($1,$2,'event.created',$3)`, project, actor, in.Key); e != nil {
		return catalog.Event{}, e
	}
	if e = tx.Commit(ctx); e != nil {
		return catalog.Event{}, e
	}
	return out, nil
}
func (s *Store) ListEvents(ctx context.Context, actor, project, environment, after string, limit int) ([]catalog.Event, error) {
	if limit < 1 || limit > 201 {
		return nil, catalog.Invalid("INVALID_LIMIT", "分页大小超出范围")
	}
	tx, e := s.catalogTx(ctx, actor, project, false)
	if e != nil {
		return nil, e
	}
	defer tx.Rollback(context.Background())
	if e = s.authorizeOperation(ctx, tx, actor, project, "", access.PermissionCatalogDraftsRead); e != nil {
		return nil, e
	}
	if identity, machine := access.IdentityFromContext(ctx); machine && identity.PrincipalID != "" && environment == "" {
		return nil, catalog.Invalid("INVALID_ENVIRONMENT", "机器调用必须指定环境")
	}
	if environment != "" {
		if e = s.authorizeOperation(ctx, tx, actor, project, environment, access.PermissionCatalogReleasesRead); e != nil {
			return nil, e
		}
	}
	// Event definitions are project-scoped. Environment filters versions through
	// manifests. Current names and unpublished events expose project-wide draft
	// metadata, so machine callers need explicit draft and release read grants.
	rows, e := tx.Query(ctx, `SELECT e.event_type_id,e.event_key,e.display_name,COALESCE((SELECT array_agg(v.schema_version ORDER BY v.schema_version) FROM catalog_versions v WHERE v.project_id=e.project_id AND v.event_key=e.event_key AND ($2='' OR EXISTS(SELECT 1 FROM catalog_release_events re WHERE re.project_id=v.project_id AND re.environment=$2 AND re.event_key=v.event_key AND re.schema_version=v.schema_version))),ARRAY[]::bigint[]) FROM catalog_events e WHERE e.project_id=$1 AND e.event_key>$3 ORDER BY e.event_key LIMIT $4`, project, environment, after, limit)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := make([]catalog.Event, 0)
	for rows.Next() {
		var v catalog.Event
		if e = rows.Scan(&v.TypeID, &v.Key, &v.DisplayName, &v.PublishedVersions); e != nil {
			return nil, e
		}
		out = append(out, v)
	}
	if e = rows.Err(); e != nil {
		return nil, e
	}
	rows.Close()
	if e = tx.Commit(ctx); e != nil {
		return nil, e
	}
	return out, nil
}
func readDraft(ctx context.Context, tx pgx.Tx, project, key string) (catalog.Draft, error) {
	var out catalog.Draft
	var data []byte
	e := tx.QueryRow(ctx, `SELECT draft_revision,draft_definition FROM catalog_events WHERE project_id=$1 AND event_key=$2`, project, key).Scan(&out.Revision, &data)
	if errors.Is(e, pgx.ErrNoRows) {
		return out, catalogMissing()
	}
	if e != nil {
		return out, e
	}
	e = json.Unmarshal(data, &out.Definition)
	return out, e
}
func (s *Store) GetDraft(ctx context.Context, actor, project, key string) (catalog.Draft, error) {
	tx, e := s.catalogTx(ctx, actor, project, false)
	if e != nil {
		return catalog.Draft{}, e
	}
	defer tx.Rollback(context.Background())
	if e = s.authorizeOperation(ctx, tx, actor, project, "", access.PermissionCatalogDraftsRead); e != nil {
		return catalog.Draft{}, e
	}
	out, e := readDraft(ctx, tx, project, key)
	if e != nil {
		return out, e
	}
	return out, tx.Commit(ctx)
}
func (s *Store) SaveDraft(ctx context.Context, actor, project, key string, revision int64, in catalog.DraftInput) (catalog.Draft, error) {
	tx, e := s.catalogTx(ctx, actor, project, true)
	if e != nil {
		return catalog.Draft{}, e
	}
	defer tx.Rollback(context.Background())
	if e = s.authorizeOperation(ctx, tx, actor, project, "", access.PermissionCatalogDraftsWrite); e != nil {
		return catalog.Draft{}, e
	}
	old, e := readDraft(ctx, tx, project, key)
	if e != nil {
		return catalog.Draft{}, e
	}
	if old.Revision != revision {
		return catalog.Draft{}, catalog.RevisionConflict()
	}
	// PATCH replaces the supplied schema, while omitted optional metadata retains
	// its prior value. Empty objects explicitly clear object-valued metadata.
	if in.DisplayName == nil {
		in.DisplayName = old.Definition.DisplayName
	}
	if in.Trigger == nil {
		in.Trigger = old.Definition.Trigger
	}
	if in.FactKind == nil {
		in.FactKind = old.Definition.FactKind
	}
	if in.AllowedSourceKinds == nil {
		in.AllowedSourceKinds = old.Definition.AllowedSourceKinds
	}
	if in.FieldMetadata == nil {
		in.FieldMetadata = old.Definition.FieldMetadata
	}
	if in.ExampleProperties == nil {
		in.ExampleProperties = old.Definition.ExampleProperties
	}
	if issues := catalog.CheckDefinition(in, false); len(issues) > 0 {
		return catalog.Draft{}, catalog.IssuesError(issues)
	}
	data, e := json.Marshal(in)
	if e != nil {
		return catalog.Draft{}, e
	}
	out := catalog.Draft{Revision: revision + 1, Definition: in}
	if _, e = tx.Exec(ctx, `UPDATE catalog_events SET draft_revision=$3,draft_definition=$4,display_name=$5 WHERE project_id=$1 AND event_key=$2`, project, key, out.Revision, data, *in.DisplayName); e != nil {
		return catalog.Draft{}, e
	}
	if _, e = tx.Exec(ctx, `INSERT INTO audit_entries(project_id,actor_id,action,resource_key) VALUES($1,$2,'event.draft_saved',$3)`, project, actor, key); e != nil {
		return catalog.Draft{}, e
	}
	if e = tx.Commit(ctx); e != nil {
		return catalog.Draft{}, e
	}
	return out, nil
}
func (s *Store) ValidationDraft(ctx context.Context, actor, project, key string, revision int64) (catalog.Draft, *catalog.DraftInput, error) {
	tx, e := s.catalogTx(ctx, actor, project, false)
	if e != nil {
		return catalog.Draft{}, nil, e
	}
	defer tx.Rollback(context.Background())
	if e = s.authorizeOperation(ctx, tx, actor, project, "", access.PermissionCatalogDraftsRead); e != nil {
		return catalog.Draft{}, nil, e
	}
	// Lock this event across both reads to compare one draft with its latest
	// published version, even if another transaction is building a release.
	if _, e = tx.Exec(ctx, `SELECT event_key FROM catalog_events WHERE project_id=$1 AND event_key=$2 FOR SHARE`, project, key); e != nil {
		return catalog.Draft{}, nil, e
	}
	draft, e := readDraft(ctx, tx, project, key)
	if e != nil {
		return draft, nil, e
	}
	if draft.Revision != revision {
		return draft, nil, catalog.RevisionConflict()
	}
	var data []byte
	var previous *catalog.DraftInput
	e = tx.QueryRow(ctx, `SELECT definition FROM catalog_versions WHERE project_id=$1 AND event_key=$2 ORDER BY schema_version DESC LIMIT 1`, project, key).Scan(&data)
	if e == nil {
		previous = &catalog.DraftInput{}
		e = json.Unmarshal(data, previous)
	} else if errors.Is(e, pgx.ErrNoRows) {
		e = nil
	}
	if e != nil {
		return draft, nil, e
	}
	return draft, previous, tx.Commit(ctx)
}

func (s *Store) GetEventVersion(ctx context.Context, actor, project, key string, version int64) (catalog.EventVersion, error) {
	tx, e := s.catalogTx(ctx, actor, project, false)
	if e != nil {
		return catalog.EventVersion{}, e
	}
	defer tx.Rollback(context.Background())
	// Versions are shared by project, but machine access is restricted to versions
	// that actually appear in a release for the credential's bound environment.
	environment := ""
	if identity, machine := access.IdentityFromContext(ctx); machine && identity.PrincipalID != "" {
		environment = identity.Environment
	}
	if e = s.authorizeOperation(ctx, tx, actor, project, environment, access.PermissionCatalogReleasesRead); e != nil {
		return catalog.EventVersion{}, e
	}
	out := catalog.EventVersion{Key: key, SchemaVersion: version}
	var raw []byte
	e = tx.QueryRow(ctx, `SELECT v.draft_revision,v.definition FROM catalog_versions v WHERE v.project_id=$1 AND v.event_key=$2 AND v.schema_version=$3 AND ($4='' OR EXISTS(SELECT 1 FROM catalog_release_events re WHERE re.project_id=v.project_id AND re.environment=$4 AND re.event_key=v.event_key AND re.schema_version=v.schema_version))`, project, key, version, environment).Scan(&out.DraftRevision, &raw)
	if errors.Is(e, pgx.ErrNoRows) {
		e = fault.New(fault.NotFound, "EVENT_VERSION_NOT_FOUND", "已发布事件版本不存在或不可见")
	}
	if e != nil {
		return catalog.EventVersion{}, e
	}
	if e = json.Unmarshal(raw, &out.Definition); e != nil {
		return catalog.EventVersion{}, e
	}
	return out, tx.Commit(ctx)
}
