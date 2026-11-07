package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"sort"

	"github.com/howtomakeaname/Evia/backend/internal/catalog"
	"github.com/howtomakeaname/Evia/backend/internal/platform/contract"
	"github.com/howtomakeaname/Evia/backend/internal/platform/fault"
	"github.com/howtomakeaname/Evia/backend/internal/platform/ids"
	"github.com/jackc/pgx/v5"
)

type manifestEvent struct {
	Key                string         `json:"event_key"`
	SchemaVersion      int64          `json:"schema_version"`
	DisplayName        string         `json:"display_name"`
	Schema             map[string]any `json:"schema"`
	FieldMetadata      map[string]any `json:"field_metadata,omitzero"`
	Trigger            string         `json:"trigger"`
	AllowedSourceKinds []string       `json:"allowed_source_kinds"`
	FactKind           string         `json:"fact_kind"`
	ExampleProperties  map[string]any `json:"example_properties"`
}
type releaseManifest struct {
	Format      string          `json:"format"`
	Status      string          `json:"status"`
	ProjectKey  string          `json:"project_key"`
	Environment string          `json:"environment"`
	Revision    int64           `json:"release_revision"`
	Events      []manifestEvent `json:"events"`
}

const releaseColumns = `r.id::text,r.revision,r.environment,r.state,(d.ingestion_ready AND EXISTS (SELECT 1 FROM catalog_release_readiness rd WHERE rd.release_id=r.id AND rd.project_id=r.project_id AND rd.environment=r.environment AND rd.manifest_hash=r.manifest_hash AND rd.ready_until>clock_timestamp())),d.acceleration_state,r.manifest_hash`
const releaseTables = `catalog_releases r JOIN catalog_release_delivery d ON d.release_id=r.id AND d.project_id=r.project_id AND d.environment=r.environment`

func readRelease(ctx context.Context, tx pgx.Tx, project, id string) (catalog.Release, error) {
	var r catalog.Release
	e := tx.QueryRow(ctx, `SELECT `+releaseColumns+` FROM `+releaseTables+` WHERE r.project_id=$1 AND r.id=$2`, project, id).Scan(&r.ID, &r.Revision, &r.Environment, &r.State, &r.IngestionReady, &r.AccelerationState, &r.ManifestHash)
	if errors.Is(e, pgx.ErrNoRows) {
		e = releaseMissing()
	}
	return r, e
}
func (s *Store) CreateRelease(ctx context.Context, actor, project, key string, in catalog.ReleaseInput) (catalog.Release, error) {
	// Sorting makes a request's event set independent of UI selection order and
	// ensures every manifest has a deterministic event order.
	in.Events = append([]catalog.ReleaseEvent(nil), in.Events...)
	sort.Slice(in.Events, func(i, j int) bool { return in.Events[i].Key < in.Events[j].Key })
	requestHash, e := catalog.Hash(in)
	if e != nil {
		return catalog.Release{}, e
	}
	tx, e := s.catalogTx(ctx, actor, project, true)
	if e != nil {
		return catalog.Release{}, e
	}
	defer tx.Rollback(context.Background())
	var existingHash, existingID string
	e = tx.QueryRow(ctx, `SELECT request_hash,release_id::text FROM catalog_idempotency WHERE actor_id=$1 AND project_id=$2 AND environment=$3 AND operation='release.create' AND idempotency_key=$4`, actor, project, in.Environment, key).Scan(&existingHash, &existingID)
	if e == nil {
		if existingHash != requestHash {
			return catalog.Release{}, fault.New(fault.Conflict, "IDEMPOTENCY_CONFLICT", "幂等键已用于不同的请求")
		}
		r, e := readRelease(ctx, tx, project, existingID)
		if e != nil {
			return r, e
		}
		return r, tx.Commit(ctx)
	}
	if !errors.Is(e, pgx.ErrNoRows) {
		return catalog.Release{}, e
	}
	if _, e = tx.Exec(ctx, `INSERT INTO catalog_environments(project_id,environment) VALUES($1,$2) ON CONFLICT DO NOTHING`, project, in.Environment); e != nil {
		return catalog.Release{}, e
	}
	var active, metadata int64
	if e = tx.QueryRow(ctx, `SELECT active_revision,metadata_revision FROM catalog_environments WHERE project_id=$1 AND environment=$2 FOR UPDATE`, project, in.Environment).Scan(&active, &metadata); e != nil {
		return catalog.Release{}, e
	}
	if active != in.ExpectedActiveRevision {
		return catalog.Release{}, fault.New(fault.Conflict, "ACTIVE_REVISION_CONFLICT", "环境的激活版本已变化，请重新加载后重试")
	}
	if metadata >= catalog.MaxInteger {
		return catalog.Release{}, fault.New(fault.Conflict, "RELEASE_REVISION_EXHAUSTED", "发布版本已达到上限")
	}
	r := catalog.Release{ID: ids.New(), Revision: metadata + 1, Environment: in.Environment, State: "published", IngestionReady: false, AccelerationState: "not_requested"}
	manifest := releaseManifest{Format: "activity.event-plan.v1", Status: "published", ProjectKey: project, Environment: in.Environment, Revision: r.Revision, Events: make([]manifestEvent, 0, len(in.Events))}
	for _, selected := range in.Events {
		draft, e := readDraft(ctx, tx, project, selected.Key)
		if e != nil {
			return catalog.Release{}, e
		}
		if draft.Revision != selected.DraftRevision {
			return catalog.Release{}, catalog.RevisionConflict()
		}
		if issues := catalog.CheckDefinition(draft.Definition, true); len(issues) > 0 {
			return catalog.Release{}, catalog.IssuesError(issues)
		}
		hash, e := catalog.Hash(draft.Definition)
		if e != nil {
			return catalog.Release{}, e
		}
		var version int64
		e = tx.QueryRow(ctx, `SELECT schema_version FROM catalog_versions WHERE project_id=$1 AND event_key=$2 AND definition_hash=$3`, project, selected.Key, hash).Scan(&version)
		if errors.Is(e, pgx.ErrNoRows) {
			if e = tx.QueryRow(ctx, `SELECT COALESCE(max(schema_version),0)+1 FROM catalog_versions WHERE project_id=$1 AND event_key=$2`, project, selected.Key).Scan(&version); e != nil {
				return catalog.Release{}, e
			}
			if version > catalog.MaxInteger {
				return catalog.Release{}, fault.New(fault.Conflict, "EVENT_VERSION_EXHAUSTED", "事件版本已达到上限")
			}
			data, e := json.Marshal(draft.Definition)
			if e != nil {
				return catalog.Release{}, e
			}
			if _, e = tx.Exec(ctx, `INSERT INTO catalog_versions(project_id,event_key,schema_version,draft_revision,definition,definition_hash) VALUES($1,$2,$3,$4,$5,$6)`, project, selected.Key, version, draft.Revision, data, hash); e != nil {
				return catalog.Release{}, e
			}
		} else if e != nil {
			return catalog.Release{}, e
		}
		definition := draft.Definition
		example := definition.ExampleProperties
		if example == nil {
			example = map[string]any{}
		}
		manifest.Events = append(manifest.Events, manifestEvent{Key: selected.Key, SchemaVersion: version, DisplayName: *definition.DisplayName, Schema: definition.Schema, FieldMetadata: definition.FieldMetadata, Trigger: *definition.Trigger, AllowedSourceKinds: definition.AllowedSourceKinds, FactKind: *definition.FactKind, ExampleProperties: example})
	}
	r.ManifestHash, e = catalog.Hash(manifest)
	if e != nil {
		return catalog.Release{}, e
	}
	data, e := catalog.CanonicalJSON(manifest)
	if e != nil {
		return catalog.Release{}, e
	}
	if len(data) > contract.MaxReleaseBytes {
		return catalog.Release{}, catalog.Invalid("RELEASE_MANIFEST_TOO_LARGE", "发布清单超过 16 MiB，请减少事件数量或字段说明")
	}
	if _, e = tx.Exec(ctx, `INSERT INTO catalog_releases(id,project_id,environment,revision,state,manifest_hash,manifest,created_by) VALUES($1,$2,$3,$4,'published',$5,$6,$7)`, r.ID, project, in.Environment, r.Revision, r.ManifestHash, data, actor); e != nil {
		return catalog.Release{}, e
	}
	if _, e = tx.Exec(ctx, `INSERT INTO catalog_release_events(project_id,environment,event_key,schema_version,release_id) SELECT $1,$2,event_key,schema_version,$3 FROM jsonb_to_recordset($4::jsonb->'events') AS event(event_key text,schema_version bigint)`, project, in.Environment, r.ID, data); e != nil {
		return catalog.Release{}, e
	}
	if _, e = tx.Exec(ctx, `INSERT INTO catalog_release_delivery(release_id,project_id,environment) VALUES($1,$2,$3)`, r.ID, project, in.Environment); e != nil {
		return catalog.Release{}, e
	}
	if _, e = tx.Exec(ctx, `UPDATE catalog_environments SET metadata_revision=$3 WHERE project_id=$1 AND environment=$2`, project, in.Environment, r.Revision); e != nil {
		return catalog.Release{}, e
	}
	if _, e = tx.Exec(ctx, `INSERT INTO catalog_idempotency(actor_id,project_id,environment,operation,idempotency_key,request_hash,release_id) VALUES($1,$2,$3,'release.create',$4,$5,$6)`, actor, project, in.Environment, key, requestHash, r.ID); e != nil {
		return catalog.Release{}, e
	}
	if _, e = tx.Exec(ctx, `INSERT INTO audit_entries(project_id,actor_id,action,resource_key) VALUES($1,$2,'catalog.release_built',$3)`, project, actor, r.ID); e != nil {
		return catalog.Release{}, e
	}
	if _, e = tx.Exec(ctx, `INSERT INTO catalog_outbox(project_id,environment,release_id,kind) VALUES($1,$2,$3,'catalog.release_built')`, project, in.Environment, r.ID); e != nil {
		return catalog.Release{}, e
	}
	if e = tx.Commit(ctx); e != nil {
		return catalog.Release{}, e
	}
	return r, nil
}
func (s *Store) GetRelease(ctx context.Context, actor, project, id string) (catalog.Release, error) {
	tx, e := s.catalogTx(ctx, actor, project, false)
	if e != nil {
		return catalog.Release{}, e
	}
	defer tx.Rollback(context.Background())
	r, e := readRelease(ctx, tx, project, id)
	if e != nil {
		return r, e
	}
	return r, tx.Commit(ctx)
}
func (s *Store) ListReleases(ctx context.Context, actor, project, environment, after string, limit int) ([]catalog.Release, error) {
	if limit < 1 || limit > 201 {
		return nil, catalog.Invalid("INVALID_LIMIT", "分页大小超出范围")
	}
	tx, e := s.catalogTx(ctx, actor, project, false)
	if e != nil {
		return nil, e
	}
	defer tx.Rollback(context.Background())
	rows, e := tx.Query(ctx, `SELECT `+releaseColumns+` FROM `+releaseTables+` WHERE r.project_id=$1 AND ($2='' OR r.environment=$2) AND ($3='' OR r.id>nullif($3,'')::uuid) ORDER BY r.id LIMIT $4`, project, environment, after, limit)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := make([]catalog.Release, 0)
	for rows.Next() {
		var r catalog.Release
		if e = rows.Scan(&r.ID, &r.Revision, &r.Environment, &r.State, &r.IngestionReady, &r.AccelerationState, &r.ManifestHash); e != nil {
			return nil, e
		}
		out = append(out, r)
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
