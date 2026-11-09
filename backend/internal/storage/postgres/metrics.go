package postgres

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"sort"
	"strings"

	"github.com/howtomakeaname/Evia/backend/internal/analysis"
	"github.com/howtomakeaname/Evia/backend/internal/platform/canonical"
	"github.com/howtomakeaname/Evia/backend/internal/platform/fault"
	"github.com/howtomakeaname/Evia/backend/internal/platform/ids"
	"github.com/howtomakeaname/Evia/backend/internal/reporting"
	"github.com/jackc/pgx/v5"
)

var _ reporting.MetricStore = (*Store)(nil)

const metricColumns = `metric_id::text,project_id::text,environment,title,draft_revision,COALESCE(published_version,0),created_at,updated_at`
const metricVersionColumns = `metric_id::text,project_id::text,environment,version,draft_revision,definition,dependencies,definition_hash,dependency_hash,validation_hash,created_at,created_by::text`

func metricMissing() error {
	return fault.New(fault.NotFound, "METRIC_NOT_FOUND", "指标不存在或不可见")
}
func metricVersionMissing() error {
	return fault.New(fault.NotFound, "METRIC_VERSION_NOT_FOUND", "指标版本不存在或不可见")
}
func metricConflict(code string) error {
	return fault.New(fault.Conflict, code, "指标已变更，请重新加载并校验")
}
func scanMetricRecord(row pgx.Row) (reporting.MetricRecord, error) {
	var out reporting.MetricRecord
	e := row.Scan(&out.ID, &out.ProjectID, &out.Environment, &out.Title, &out.DraftRevision, &out.PublishedVersion, &out.CreatedAt, &out.UpdatedAt)
	if errors.Is(e, pgx.ErrNoRows) {
		e = metricMissing()
	}
	return out, e
}
func decodeMetric(data []byte, out any) error {
	d := json.NewDecoder(bytes.NewReader(data))
	d.UseNumber()
	return d.Decode(out)
}
func readMetricDraftTx(ctx context.Context, tx pgx.Tx, project, id string) (reporting.MetricDraft, error) {
	var out reporting.MetricDraft
	var data []byte
	e := tx.QueryRow(ctx, `SELECT `+metricColumns+`,definition FROM metrics WHERE project_id=$1 AND metric_id=$2`, project, id).Scan(&out.ID, &out.ProjectID, &out.Environment, &out.Title, &out.DraftRevision, &out.PublishedVersion, &out.CreatedAt, &out.UpdatedAt, &data)
	if errors.Is(e, pgx.ErrNoRows) {
		return out, metricMissing()
	}
	if e != nil {
		return out, e
	}
	e = decodeMetric(data, &out.Definition)
	return out, e
}
func scanMetricVersion(row pgx.Row) (reporting.MetricVersion, error) {
	var out reporting.MetricVersion
	var def, deps []byte
	e := row.Scan(&out.MetricID, &out.ProjectID, &out.Environment, &out.Version, &out.DraftRevision, &def, &deps, &out.DefinitionHash, &out.DependencyHash, &out.ValidationHash, &out.CreatedAt, &out.CreatedBy)
	if errors.Is(e, pgx.ErrNoRows) {
		return out, metricVersionMissing()
	}
	if e != nil {
		return out, e
	}
	if e = decodeMetric(def, &out.Definition); e != nil {
		return out, e
	}
	e = decodeMetric(deps, &out.Dependencies)
	return out, e
}

// Caller must authorize current project membership and hold the project lock
// required for its operation. Published rows are immutable.
func readMetricVersionTx(ctx context.Context, tx pgx.Tx, project, id string, version int64) (reporting.MetricVersion, error) {
	return scanMetricVersion(tx.QueryRow(ctx, `SELECT `+metricVersionColumns+` FROM metric_versions WHERE project_id=$1 AND metric_id=$2 AND version=$3`, project, id, version))
}
func metricIdempotencyReplay(ctx context.Context, tx pgx.Tx, actor, project, environment, operation, key, hash string, out any) (bool, error) {
	var old string
	var data []byte
	e := tx.QueryRow(ctx, `SELECT request_hash,response FROM metric_idempotency WHERE actor_id=$1 AND project_id=$2 AND environment=$3 AND operation=$4 AND idempotency_key=$5`, actor, project, environment, operation, key).Scan(&old, &data)
	if errors.Is(e, pgx.ErrNoRows) {
		return false, nil
	}
	if e != nil {
		return false, e
	}
	if old != hash {
		return false, fault.New(fault.Conflict, "IDEMPOTENCY_CONFLICT", "幂等键已用于不同请求")
	}
	return true, decodeMetric(data, out)
}
func metricIdempotencyWrite(ctx context.Context, tx pgx.Tx, actor, project, environment, operation, key, hash string, out any) error {
	data, e := json.Marshal(out)
	if e != nil {
		return e
	}
	_, e = tx.Exec(ctx, `INSERT INTO metric_idempotency(actor_id,project_id,environment,operation,idempotency_key,request_hash,response) VALUES($1,$2,$3,$4,$5,$6,$7)`, actor, project, environment, operation, key, hash, data)
	return e
}
func metricKey(key string) error {
	if len(key) < 1 || len(key) > 128 || strings.IndexFunc(key, func(r rune) bool { return r < 33 || r > 126 }) >= 0 {
		return analysis.Invalid("INVALID_IDEMPOTENCY_KEY", "幂等键无效")
	}
	return nil
}

// Scope is read from the stored resource, after catalogTx checks the project.
func (s *Store) authorizeMetricResource(ctx context.Context, tx pgx.Tx, actor, project, id, table, column, permission string) error {
	var environment string
	e := tx.QueryRow(ctx, "SELECT environment FROM "+table+" WHERE project_id=$1 AND "+column+"=$2", project, id).Scan(&environment)
	if errors.Is(e, pgx.ErrNoRows) {
		if table == "metric_versions" {
			return metricVersionMissing()
		}
		return metricMissing()
	}
	if e != nil {
		return e
	}
	return s.authorizeOperation(ctx, tx, actor, project, environment, permission)
}
func (s *Store) CreateMetric(ctx context.Context, actor, project, key string, in reporting.MetricCreate) (reporting.MetricDraft, error) {
	var out reporting.MetricDraft
	tx, e := s.catalogTx(ctx, actor, project, true)
	if e != nil {
		return out, e
	}
	defer tx.Rollback(context.Background())
	if e = s.authorizeOperation(ctx, tx, actor, project, in.Environment, "metrics.write"); e != nil {
		return out, e
	}
	if e = metricKey(key); e != nil {
		return out, e
	}
	if e = savedEnvironment(in.Environment); e != nil {
		return out, e
	}
	in.Definition, e = reporting.NormalizeMetricDefinition(in.Definition)
	if e != nil {
		return out, e
	}
	hash, e := canonical.Hash(in)
	if e != nil {
		return out, e
	}
	replay, e := metricIdempotencyReplay(ctx, tx, actor, project, in.Environment, "create", key, hash, &out)
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
	_, e = tx.Exec(ctx, `INSERT INTO metrics(project_id,metric_id,environment,title,definition) VALUES($1,$2,$3,$4,$5)`, project, id, in.Environment, in.Definition.Title, data)
	if e != nil {
		return out, e
	}
	out, e = readMetricDraftTx(ctx, tx, project, id)
	if e != nil {
		return out, e
	}
	if _, e = tx.Exec(ctx, `INSERT INTO audit_entries(project_id,actor_id,action,resource_key) VALUES($1,$2,'metric.created',$3)`, project, actor, id); e != nil {
		return out, e
	}
	if e = metricIdempotencyWrite(ctx, tx, actor, project, in.Environment, "create", key, hash, out); e != nil {
		return out, e
	}
	return out, tx.Commit(ctx)
}
func (s *Store) ListMetrics(ctx context.Context, actor, project, environment, after string, limit int) ([]reporting.MetricRecord, error) {
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
	if e = s.authorizeOperation(ctx, tx, actor, project, environment, "metrics.read"); e != nil {
		return nil, e
	}
	rows, e := tx.Query(ctx, `SELECT `+metricColumns+` FROM metrics WHERE project_id=$1 AND environment=$2 AND ($3='' OR metric_id>NULLIF($3,'')::uuid) ORDER BY metric_id LIMIT $4`, project, environment, after, limit)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []reporting.MetricRecord{}
	for rows.Next() {
		v, e := scanMetricRecord(rows)
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
func (s *Store) GetMetric(ctx context.Context, actor, project, id string) (reporting.MetricRecord, error) {
	tx, e := s.catalogTx(ctx, actor, project, false)
	if e != nil {
		return reporting.MetricRecord{}, e
	}
	defer tx.Rollback(context.Background())
	if e = s.authorizeMetricResource(ctx, tx, actor, project, id, "metrics", "metric_id", "metrics.read"); e != nil {
		return reporting.MetricRecord{}, e
	}
	out, e := scanMetricRecord(tx.QueryRow(ctx, `SELECT `+metricColumns+` FROM metrics WHERE project_id=$1 AND metric_id=$2`, project, id))
	if e != nil {
		return out, e
	}
	return out, tx.Commit(ctx)
}
func (s *Store) GetMetricDraft(ctx context.Context, actor, project, id string) (reporting.MetricDraft, error) {
	tx, e := s.catalogTx(ctx, actor, project, false)
	if e != nil {
		return reporting.MetricDraft{}, e
	}
	defer tx.Rollback(context.Background())
	if e = s.authorizeMetricResource(ctx, tx, actor, project, id, "metrics", "metric_id", "metrics.read"); e != nil {
		return reporting.MetricDraft{}, e
	}
	out, e := readMetricDraftTx(ctx, tx, project, id)
	if e != nil {
		return out, e
	}
	return out, tx.Commit(ctx)
}
func (s *Store) SaveMetricDraft(ctx context.Context, actor, project, id string, revision int64, in reporting.MetricDefinition) (reporting.MetricDraft, error) {
	var out reporting.MetricDraft
	tx, e := s.catalogTx(ctx, actor, project, true)
	if e != nil {
		return out, e
	}
	defer tx.Rollback(context.Background())
	if e = s.authorizeMetricResource(ctx, tx, actor, project, id, "metrics", "metric_id", "metrics.write"); e != nil {
		return out, e
	}
	old, e := readMetricDraftTx(ctx, tx, project, id)
	if e != nil {
		return out, e
	}
	if revision < 1 {
		return out, fault.New(fault.Invalid, "REVISION_REQUIRED", "需要草稿修订号")
	}
	if old.DraftRevision != revision {
		return out, metricConflict("METRIC_REVISION_CONFLICT")
	}
	if revision >= analysis.MaxSafeInteger {
		return out, metricConflict("METRIC_REVISION_EXHAUSTED")
	}
	in, e = reporting.NormalizeMetricDefinition(in)
	if e != nil {
		return out, e
	}
	data, e := json.Marshal(in)
	if e != nil {
		return out, e
	}
	if _, e = tx.Exec(ctx, `UPDATE metrics SET definition=$3,title=$4,draft_revision=draft_revision+1,updated_at=clock_timestamp() WHERE project_id=$1 AND metric_id=$2`, project, id, data, in.Title); e != nil {
		return out, e
	}
	if _, e = tx.Exec(ctx, `INSERT INTO audit_entries(project_id,actor_id,action,resource_key) VALUES($1,$2,'metric.draft_saved',$3)`, project, actor, id); e != nil {
		return out, e
	}
	out, e = readMetricDraftTx(ctx, tx, project, id)
	if e != nil {
		return out, e
	}
	return out, tx.Commit(ctx)
}

func metricSnapshotTx(ctx context.Context, tx pgx.Tx, project, id string, in reporting.MetricValidate) (reporting.MetricSnapshot, error) {
	var out reporting.MetricSnapshot
	var e error
	out.Draft, e = readMetricDraftTx(ctx, tx, project, id)
	if e != nil {
		return out, e
	}
	if in.DraftRevision != out.Draft.DraftRevision {
		return out, metricConflict("METRIC_REVISION_CONFLICT")
	}
	if in.ExpectedPublishedVersion != out.Draft.PublishedVersion {
		return out, metricConflict("PUBLISHED_VERSION_CONFLICT")
	}
	if out.Draft.PublishedVersion > 0 {
		v, e := readMetricVersionTx(ctx, tx, project, id, out.Draft.PublishedVersion)
		if e != nil {
			return out, e
		}
		out.Baseline = &v
	}
	out.Draft.Definition, e = reporting.NormalizeMetricDefinition(out.Draft.Definition)
	if e != nil {
		return out, e
	}
	// The projection has no execution dates or timezone. Only configuration
	// validation and catalog type resolution run, and derived types stay private.
	template, e := reporting.MetricTemplate(out.Draft.Definition, reporting.MetricExecute{Interval: "day"})
	if e != nil {
		return out, e
	}
	data, e := json.Marshal(template)
	if e != nil {
		return out, e
	}
	var request analysis.Request
	if e = decodeMetric(data, &request); e != nil {
		return out, e
	}
	request.Environment = out.Draft.Environment
	if e = analysis.ValidateConfiguration(&request); e != nil {
		return out, e
	}
	if e = resolveQueryFields(ctx, tx, project, &request); e != nil {
		return out, e
	}
	manifest := reporting.DependencyManifest{ProjectID: project, Environment: out.Draft.Environment, SelectionMode: "event_versions", UnfixedEventKeys: []string{}, EventVersions: []reporting.Dependency{}}
	versions := map[analysis.EventVersion]bool{}
	for _, v := range request.EventVersions {
		versions[v] = true
	}
	for v := range versions {
		var hash string
		e = tx.QueryRow(ctx, `SELECT v.definition_hash FROM catalog_versions v WHERE v.project_id=$1 AND v.event_key=$2 AND v.schema_version=$3 AND EXISTS(SELECT 1 FROM catalog_release_events re WHERE re.project_id=v.project_id AND re.environment=$4 AND re.event_key=v.event_key AND re.schema_version=v.schema_version)`, project, v.EventKey, v.SchemaVersion, out.Draft.Environment).Scan(&hash)
		if errors.Is(e, pgx.ErrNoRows) {
			return out, analysis.Invalid("QUERY_EVENT_VERSION_NOT_PUBLISHED", "事件版本不存在或尚未发布")
		}
		if e != nil {
			return out, e
		}
		manifest.EventVersions = append(manifest.EventVersions, reporting.Dependency{EventKey: v.EventKey, SchemaVersion: v.SchemaVersion, DefinitionHash: hash})
	}
	sort.Slice(manifest.EventVersions, func(i, j int) bool {
		a, b := manifest.EventVersions[i], manifest.EventVersions[j]
		return a.EventKey < b.EventKey || a.EventKey == b.EventKey && a.SchemaVersion < b.SchemaVersion
	})
	out.Dependencies = manifest
	return out, nil
}
func (s *Store) MetricValidationSnapshot(ctx context.Context, actor, project, id string, in reporting.MetricValidate) (reporting.MetricSnapshot, error) {
	tx, e := s.catalogTx(ctx, actor, project, false)
	if e != nil {
		return reporting.MetricSnapshot{}, e
	}
	defer tx.Rollback(context.Background())
	if e = s.authorizeMetricResource(ctx, tx, actor, project, id, "metrics", "metric_id", "metrics.write"); e != nil {
		return reporting.MetricSnapshot{}, e
	}
	if _, e = tx.Exec(ctx, `SELECT id FROM projects WHERE id=$1 FOR SHARE`, project); e != nil {
		return reporting.MetricSnapshot{}, e
	}
	out, e := metricSnapshotTx(ctx, tx, project, id, in)
	if e != nil {
		return out, e
	}
	return out, tx.Commit(ctx)
}
func (s *Store) PublishMetric(ctx context.Context, actor, project, id, key string, in reporting.MetricPublish) (reporting.MetricVersion, error) {
	var out reporting.MetricVersion
	tx, e := s.catalogTx(ctx, actor, project, true)
	if e != nil {
		return out, e
	}
	defer tx.Rollback(context.Background())
	if e = s.authorizeMetricResource(ctx, tx, actor, project, id, "metrics", "metric_id", "metrics.publish"); e != nil {
		return out, e
	}
	if e = metricKey(key); e != nil {
		return out, e
	}
	draft, e := readMetricDraftTx(ctx, tx, project, id)
	if e != nil {
		return out, e
	}
	operation := "publish:" + id
	hash, e := canonical.Hash(struct {
		MetricID string                  `json:"metric_id"`
		Input    reporting.MetricPublish `json:"input"`
	}{id, in})
	if e != nil {
		return out, e
	}
	replay, e := metricIdempotencyReplay(ctx, tx, actor, project, draft.Environment, operation, key, hash, &out)
	if e != nil {
		return out, e
	}
	if replay {
		return out, tx.Commit(ctx)
	}
	snapshot, e := metricSnapshotTx(ctx, tx, project, id, reporting.MetricValidate{DraftRevision: in.DraftRevision, ExpectedPublishedVersion: in.ExpectedPublishedVersion})
	if e != nil {
		return out, e
	}
	validation, e := reporting.BuildMetricValidation(snapshot)
	if e != nil {
		return out, e
	}
	if validation.ValidationHash != in.ValidationHash {
		return out, metricConflict("VALIDATION_HASH_CONFLICT")
	}
	if draft.PublishedVersion >= analysis.MaxSafeInteger {
		return out, metricConflict("METRIC_VERSION_EXHAUSTED")
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
	if _, e = tx.Exec(ctx, `INSERT INTO metric_versions(project_id,metric_id,environment,version,draft_revision,definition,dependencies,definition_hash,dependency_hash,validation_hash,created_by) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)`, project, id, draft.Environment, version, draft.DraftRevision, definition, deps, validation.DefinitionHash, validation.DependencyHash, validation.ValidationHash, actor); e != nil {
		return out, e
	}
	// A deterministic existing membership row witnesses publication in this exact
	// environment. No catalog manifest is reparsed or copied.
	for _, d := range validation.Dependencies.EventVersions {
		result, e := tx.Exec(ctx, `INSERT INTO metric_dependencies(project_id,environment,metric_id,version,event_key,schema_version,definition_hash,release_id) SELECT $1,$2,$3,$4,$5,$6,$7,re.release_id FROM catalog_release_events re WHERE re.project_id=$1 AND re.environment=$2 AND re.event_key=$5 AND re.schema_version=$6 ORDER BY re.release_id LIMIT 1`, project, draft.Environment, id, version, d.EventKey, d.SchemaVersion, d.DefinitionHash)
		if e != nil {
			return out, e
		}
		if result.RowsAffected() != 1 {
			return out, analysis.Invalid("QUERY_EVENT_VERSION_NOT_PUBLISHED", "事件版本不存在或尚未发布")
		}
	}
	if _, e = tx.Exec(ctx, `UPDATE metrics SET published_version=$3,updated_at=clock_timestamp() WHERE project_id=$1 AND metric_id=$2`, project, id, version); e != nil {
		return out, e
	}
	if _, e = tx.Exec(ctx, `INSERT INTO audit_entries(project_id,actor_id,action,resource_key) VALUES($1,$2,'metric.published',$3)`, project, actor, id); e != nil {
		return out, e
	}
	out, e = readMetricVersionTx(ctx, tx, project, id, version)
	if e != nil {
		return out, e
	}
	if e = metricIdempotencyWrite(ctx, tx, actor, project, draft.Environment, operation, key, hash, out); e != nil {
		return out, e
	}
	return out, tx.Commit(ctx)
}
func (s *Store) GetMetricVersion(ctx context.Context, actor, project, id string, version int64) (reporting.MetricVersion, error) {
	tx, e := s.catalogTx(ctx, actor, project, false)
	if e != nil {
		return reporting.MetricVersion{}, e
	}
	defer tx.Rollback(context.Background())
	if e = s.authorizeMetricResource(ctx, tx, actor, project, id, "metric_versions", "metric_id", "metrics.read"); e != nil {
		return reporting.MetricVersion{}, e
	}
	out, e := readMetricVersionTx(ctx, tx, project, id, version)
	if e != nil {
		return out, e
	}
	return out, tx.Commit(ctx)
}
func (s *Store) ListMetricVersions(ctx context.Context, actor, project, id string, after int64, limit int) ([]reporting.MetricVersion, error) {
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
	if e = s.authorizeMetricResource(ctx, tx, actor, project, id, "metrics", "metric_id", "metrics.read"); e != nil {
		return nil, e
	}
	if _, e = scanMetricRecord(tx.QueryRow(ctx, `SELECT `+metricColumns+` FROM metrics WHERE project_id=$1 AND metric_id=$2`, project, id)); e != nil {
		return nil, e
	}
	rows, e := tx.Query(ctx, `SELECT `+metricVersionColumns+` FROM metric_versions WHERE project_id=$1 AND metric_id=$2 AND version>$3 ORDER BY version LIMIT $4`, project, id, after, limit)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []reporting.MetricVersion{}
	for rows.Next() {
		v, e := scanMetricVersion(rows)
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
