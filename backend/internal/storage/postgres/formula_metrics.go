package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"time"

	"github.com/howtomakeaname/Evia/backend/internal/analysis"
	"github.com/howtomakeaname/Evia/backend/internal/platform/canonical"
	"github.com/howtomakeaname/Evia/backend/internal/platform/fault"
	"github.com/howtomakeaname/Evia/backend/internal/platform/ids"
	"github.com/howtomakeaname/Evia/backend/internal/reporting"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

var _ reporting.FormulaMetricStore = (*Store)(nil)

const formulaMetricColumns = `metric_id::text,project_id::text,environment,title,draft_revision,COALESCE(published_version,0),created_at,updated_at,archived_at,COALESCE(archived_by::text,'')`
const formulaMetricVersionColumns = `metric_id::text,project_id::text,environment,version,draft_revision,definition,dependencies,definition_hash,dependency_hash,validation_hash,created_at,created_by::text`

func formulaMetricMissing() error {
	return fault.New(fault.NotFound, "FORMULA_METRIC_NOT_FOUND", "复合指标不存在或不可见")
}
func formulaMetricVersionMissing() error {
	return fault.New(fault.NotFound, "FORMULA_METRIC_VERSION_NOT_FOUND", "复合指标版本不存在或不可见")
}

func scanFormulaMetricRecord(row pgx.Row) (reporting.FormulaMetricRecord, error) {
	var out reporting.FormulaMetricRecord
	e := row.Scan(&out.ID, &out.ProjectID, &out.Environment, &out.Title, &out.DraftRevision, &out.PublishedVersion, &out.CreatedAt, &out.UpdatedAt, &out.ArchivedAt, &out.ArchivedBy)
	if errors.Is(e, pgx.ErrNoRows) {
		e = formulaMetricMissing()
	}
	out.CreatedAt = out.CreatedAt.UTC()
	out.UpdatedAt = out.UpdatedAt.UTC()
	if out.ArchivedAt != nil {
		archivedAt := out.ArchivedAt.UTC()
		out.ArchivedAt = &archivedAt
	}
	return out, e
}

func readFormulaMetricDraftTx(ctx context.Context, tx pgx.Tx, project, id string) (reporting.FormulaMetricDraft, error) {
	var out reporting.FormulaMetricDraft
	var data []byte
	e := tx.QueryRow(ctx, `SELECT `+formulaMetricColumns+`,definition FROM formula_metrics WHERE project_id=$1 AND metric_id=$2`, project, id).Scan(&out.ID, &out.ProjectID, &out.Environment, &out.Title, &out.DraftRevision, &out.PublishedVersion, &out.CreatedAt, &out.UpdatedAt, &out.ArchivedAt, &out.ArchivedBy, &data)
	if errors.Is(e, pgx.ErrNoRows) {
		return out, formulaMetricMissing()
	}
	if e != nil {
		return out, e
	}
	out.CreatedAt = out.CreatedAt.UTC()
	out.UpdatedAt = out.UpdatedAt.UTC()
	if out.ArchivedAt != nil {
		archivedAt := out.ArchivedAt.UTC()
		out.ArchivedAt = &archivedAt
	}
	e = decodeMetric(data, &out.Definition)
	return out, e
}

func scanFormulaMetricVersion(row pgx.Row) (reporting.FormulaMetricVersion, error) {
	var out reporting.FormulaMetricVersion
	var def, deps []byte
	e := row.Scan(&out.MetricID, &out.ProjectID, &out.Environment, &out.Version, &out.DraftRevision, &def, &deps, &out.DefinitionHash, &out.DependencyHash, &out.ValidationHash, &out.CreatedAt, &out.CreatedBy)
	if errors.Is(e, pgx.ErrNoRows) {
		return out, formulaMetricVersionMissing()
	}
	if e != nil {
		return out, e
	}
	if e = decodeMetric(def, &out.Definition); e != nil {
		return out, e
	}
	out.CreatedAt = out.CreatedAt.UTC()
	e = decodeMetric(deps, &out.Dependencies)
	return out, e
}

func readFormulaMetricVersionTx(ctx context.Context, tx pgx.Tx, project, id string, version int64) (reporting.FormulaMetricVersion, error) {
	return scanFormulaMetricVersion(tx.QueryRow(ctx, `SELECT `+formulaMetricVersionColumns+` FROM formula_metric_versions WHERE project_id=$1 AND metric_id=$2 AND version=$3`, project, id, version))
}

// pgFormulaResolver expands formula references against immutable atomic metric
// versions in the same authorized transaction. The foreign key in migration 013
// additionally enforces publication and scope at write time.
type pgFormulaResolver struct {
	tx          pgx.Tx
	store       *Store
	actor       string
	project     string
	environment string
}

var _ reporting.FormulaMetricResolver = (*pgFormulaResolver)(nil)

func (r *pgFormulaResolver) ResolveFormulaMetric(ctx context.Context, ref reporting.MetricReference) (reporting.FormulaResolvedMetric, error) {
	var composite bool
	e := r.tx.QueryRow(ctx, `SELECT true FROM formula_metric_versions WHERE project_id=$1 AND metric_id=$2 AND version=$3`, r.project, ref.MetricID, ref.Version).Scan(&composite)
	if e != nil && !errors.Is(e, pgx.ErrNoRows) {
		return reporting.FormulaResolvedMetric{}, e
	}
	if e == nil {
		version, readErr := readFormulaMetricVersionTx(ctx, r.tx, r.project, ref.MetricID, ref.Version)
		if readErr != nil {
			return reporting.FormulaResolvedMetric{}, readErr
		}
		if version.Environment != r.environment {
			return reporting.FormulaResolvedMetric{}, analysis.Invalid("INVALID_METRIC_REFERENCE", "指标引用必须属于同一项目与环境", "/formula/expression")
		}
		if e = r.store.authorizeOperation(ctx, r.tx, r.actor, r.project, r.environment, "metrics.read"); e != nil {
			return reporting.FormulaResolvedMetric{}, e
		}
		normalized, e := reporting.NormalizeFormulaMetricDefinition(version.Definition)
		if e != nil {
			return reporting.FormulaResolvedMetric{}, e
		}
		return reporting.FormulaResolvedMetric{
			Ref:            ref,
			Unit:           normalized.Formula.Unit,
			DefinitionHash: version.DefinitionHash,
			Composite:      true,
			References:     reporting.FormulaReferences(normalized.Formula.Expression),
		}, nil
	}
	metric, e := readMetricVersionTx(ctx, r.tx, r.project, ref.MetricID, ref.Version)
	if e != nil {
		return reporting.FormulaResolvedMetric{}, e
	}
	if metric.Environment != r.environment {
		return reporting.FormulaResolvedMetric{}, analysis.Invalid("INVALID_METRIC_REFERENCE", "指标引用必须属于同一项目与环境", "/formula/expression")
	}
	if e = r.store.authorizeOperation(ctx, r.tx, r.actor, r.project, r.environment, "metrics.read"); e != nil {
		return reporting.FormulaResolvedMetric{}, e
	}
	return reporting.FormulaResolvedMetric{Ref: ref, Unit: metric.Definition.Unit, DefinitionHash: metric.DefinitionHash, Composite: false}, nil
}

func (s *Store) authorizeFormulaResource(ctx context.Context, tx pgx.Tx, actor, project, id, table, column, permission string) error {
	var environment string
	e := tx.QueryRow(ctx, "SELECT environment FROM "+table+" WHERE project_id=$1 AND "+column+"=$2", project, id).Scan(&environment)
	if errors.Is(e, pgx.ErrNoRows) {
		if table == "formula_metric_versions" {
			return formulaMetricVersionMissing()
		}
		return formulaMetricMissing()
	}
	if e != nil {
		return e
	}
	return s.authorizeOperation(ctx, tx, actor, project, environment, permission)
}

func (s *Store) CreateFormulaMetric(ctx context.Context, actor, project, key string, in reporting.FormulaMetricCreate) (reporting.FormulaMetricDraft, error) {
	var out reporting.FormulaMetricDraft
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
	in.Definition, e = reporting.NormalizeFormulaMetricDefinition(in.Definition)
	if e != nil {
		return out, e
	}
	hash, e := canonical.Hash(in)
	if e != nil {
		return out, e
	}
	replay, e := metricIdempotencyReplay(ctx, tx, actor, project, in.Environment, "formula_create", key, hash, &out)
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
	if _, e = tx.Exec(ctx, `INSERT INTO formula_metrics(project_id,metric_id,environment,title,definition) VALUES($1,$2,$3,$4,$5)`, project, id, in.Environment, in.Definition.Title, data); e != nil {
		return out, e
	}
	out, e = readFormulaMetricDraftTx(ctx, tx, project, id)
	if e != nil {
		return out, e
	}
	if _, e = tx.Exec(ctx, `INSERT INTO audit_entries(project_id,actor_id,action,resource_key) VALUES($1,$2,'formula_metric.created',$3)`, project, actor, id); e != nil {
		return out, e
	}
	if e = metricIdempotencyWrite(ctx, tx, actor, project, in.Environment, "formula_create", key, hash, out); e != nil {
		return out, e
	}
	return out, tx.Commit(ctx)
}

func (s *Store) ListFormulaMetrics(ctx context.Context, actor, project, environment, after string, limit int, archivedOnly bool) ([]reporting.FormulaMetricRecord, error) {
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
	// The predicate is selected from the validated bool, never from request text.
	archivePredicate := "archived_at IS NULL"
	if archivedOnly {
		archivePredicate = "archived_at IS NOT NULL"
	}
	rows, e := tx.Query(ctx, `SELECT `+formulaMetricColumns+` FROM formula_metrics WHERE project_id=$1 AND environment=$2 AND `+archivePredicate+` AND ($3='' OR metric_id>NULLIF($3,'')::uuid) ORDER BY metric_id LIMIT $4`, project, environment, after, limit)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []reporting.FormulaMetricRecord{}
	for rows.Next() {
		record, e := scanFormulaMetricRecord(rows)
		if e != nil {
			return nil, e
		}
		out = append(out, record)
	}
	if e = rows.Err(); e != nil {
		return nil, e
	}
	rows.Close()
	return out, tx.Commit(ctx)
}

// assertFormulaMetricActiveTx blocks mutating operations on an archived formula
// metric. Caller must already hold write authorization and the project lock.
func assertFormulaMetricActiveTx(ctx context.Context, tx pgx.Tx, project, id string) error {
	var archivedAt *time.Time
	e := tx.QueryRow(ctx, `SELECT archived_at FROM formula_metrics WHERE project_id=$1 AND metric_id=$2`, project, id).Scan(&archivedAt)
	if errors.Is(e, pgx.ErrNoRows) {
		return formulaMetricMissing()
	}
	if e != nil {
		return e
	}
	if archivedAt != nil {
		return fault.New(fault.Conflict, "FORMULA_METRIC_ARCHIVED", "复合指标已归档，请先恢复后再修改")
	}
	return nil
}

func (s *Store) ArchiveFormulaMetric(ctx context.Context, actor, project, id string) (reporting.FormulaMetricRecord, error) {
	tx, e := s.catalogTx(ctx, actor, project, true)
	if e != nil {
		return reporting.FormulaMetricRecord{}, e
	}
	defer tx.Rollback(context.Background())
	if e = s.authorizeFormulaResource(ctx, tx, actor, project, id, "formula_metrics", "metric_id", "metrics.write"); e != nil {
		return reporting.FormulaMetricRecord{}, e
	}
	var archivedAt *time.Time
	e = tx.QueryRow(ctx, `SELECT archived_at FROM formula_metrics WHERE project_id=$1 AND metric_id=$2`, project, id).Scan(&archivedAt)
	if errors.Is(e, pgx.ErrNoRows) {
		return reporting.FormulaMetricRecord{}, formulaMetricMissing()
	}
	if e != nil {
		return reporting.FormulaMetricRecord{}, e
	}
	if archivedAt != nil {
		return reporting.FormulaMetricRecord{}, fault.New(fault.Conflict, "FORMULA_METRIC_ALREADY_ARCHIVED", "复合指标已处于归档状态")
	}
	if _, e = tx.Exec(ctx, `UPDATE formula_metrics SET archived_at=clock_timestamp(), archived_by=$3::uuid, updated_at=clock_timestamp() WHERE project_id=$1 AND metric_id=$2`, project, id, actor); e != nil {
		return reporting.FormulaMetricRecord{}, e
	}
	if _, e = tx.Exec(ctx, `INSERT INTO audit_entries(project_id,actor_id,action,resource_key) VALUES($1,$2,'formula_metric.archived',$3)`, project, actor, id); e != nil {
		return reporting.FormulaMetricRecord{}, e
	}
	out, e := scanFormulaMetricRecord(tx.QueryRow(ctx, `SELECT `+formulaMetricColumns+` FROM formula_metrics WHERE project_id=$1 AND metric_id=$2`, project, id))
	if e != nil {
		return reporting.FormulaMetricRecord{}, e
	}
	return out, tx.Commit(ctx)
}

func (s *Store) UnarchiveFormulaMetric(ctx context.Context, actor, project, id string) (reporting.FormulaMetricRecord, error) {
	tx, e := s.catalogTx(ctx, actor, project, true)
	if e != nil {
		return reporting.FormulaMetricRecord{}, e
	}
	defer tx.Rollback(context.Background())
	if e = s.authorizeFormulaResource(ctx, tx, actor, project, id, "formula_metrics", "metric_id", "metrics.write"); e != nil {
		return reporting.FormulaMetricRecord{}, e
	}
	var archivedAt *time.Time
	e = tx.QueryRow(ctx, `SELECT archived_at FROM formula_metrics WHERE project_id=$1 AND metric_id=$2`, project, id).Scan(&archivedAt)
	if errors.Is(e, pgx.ErrNoRows) {
		return reporting.FormulaMetricRecord{}, formulaMetricMissing()
	}
	if e != nil {
		return reporting.FormulaMetricRecord{}, e
	}
	if archivedAt == nil {
		return reporting.FormulaMetricRecord{}, fault.New(fault.Conflict, "FORMULA_METRIC_NOT_ARCHIVED", "复合指标未归档，无需恢复")
	}
	if _, e = tx.Exec(ctx, `UPDATE formula_metrics SET archived_at=NULL, archived_by=NULL, updated_at=clock_timestamp() WHERE project_id=$1 AND metric_id=$2`, project, id); e != nil {
		return reporting.FormulaMetricRecord{}, e
	}
	if _, e = tx.Exec(ctx, `INSERT INTO audit_entries(project_id,actor_id,action,resource_key) VALUES($1,$2,'formula_metric.unarchived',$3)`, project, actor, id); e != nil {
		return reporting.FormulaMetricRecord{}, e
	}
	out, e := scanFormulaMetricRecord(tx.QueryRow(ctx, `SELECT `+formulaMetricColumns+` FROM formula_metrics WHERE project_id=$1 AND metric_id=$2`, project, id))
	if e != nil {
		return reporting.FormulaMetricRecord{}, e
	}
	return out, tx.Commit(ctx)
}

func (s *Store) GetFormulaMetric(ctx context.Context, actor, project, id string) (reporting.FormulaMetricRecord, error) {
	tx, e := s.catalogTx(ctx, actor, project, false)
	if e != nil {
		return reporting.FormulaMetricRecord{}, e
	}
	defer tx.Rollback(context.Background())
	if e = s.authorizeFormulaResource(ctx, tx, actor, project, id, "formula_metrics", "metric_id", "metrics.read"); e != nil {
		return reporting.FormulaMetricRecord{}, e
	}
	out, e := scanFormulaMetricRecord(tx.QueryRow(ctx, `SELECT `+formulaMetricColumns+` FROM formula_metrics WHERE project_id=$1 AND metric_id=$2`, project, id))
	if e != nil {
		return out, e
	}
	return out, tx.Commit(ctx)
}

func (s *Store) GetFormulaMetricDraft(ctx context.Context, actor, project, id string) (reporting.FormulaMetricDraft, error) {
	tx, e := s.catalogTx(ctx, actor, project, false)
	if e != nil {
		return reporting.FormulaMetricDraft{}, e
	}
	defer tx.Rollback(context.Background())
	if e = s.authorizeFormulaResource(ctx, tx, actor, project, id, "formula_metrics", "metric_id", "metrics.read"); e != nil {
		return reporting.FormulaMetricDraft{}, e
	}
	out, e := readFormulaMetricDraftTx(ctx, tx, project, id)
	if e != nil {
		return out, e
	}
	return out, tx.Commit(ctx)
}

func (s *Store) SaveFormulaMetricDraft(ctx context.Context, actor, project, id string, revision int64, in reporting.FormulaMetricDefinition) (reporting.FormulaMetricDraft, error) {
	var out reporting.FormulaMetricDraft
	tx, e := s.catalogTx(ctx, actor, project, true)
	if e != nil {
		return out, e
	}
	defer tx.Rollback(context.Background())
	if e = s.authorizeFormulaResource(ctx, tx, actor, project, id, "formula_metrics", "metric_id", "metrics.write"); e != nil {
		return out, e
	}
	old, e := readFormulaMetricDraftTx(ctx, tx, project, id)
	if e != nil {
		return out, e
	}
	if old.ArchivedAt != nil {
		return out, fault.New(fault.Conflict, "FORMULA_METRIC_ARCHIVED", "复合指标已归档，请先恢复后再修改")
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
	in, e = reporting.NormalizeFormulaMetricDefinition(in)
	if e != nil {
		return out, e
	}
	data, e := json.Marshal(in)
	if e != nil {
		return out, e
	}
	if _, e = tx.Exec(ctx, `UPDATE formula_metrics SET definition=$3,title=$4,draft_revision=draft_revision+1,updated_at=clock_timestamp() WHERE project_id=$1 AND metric_id=$2`, project, id, data, in.Title); e != nil {
		return out, e
	}
	if _, e = tx.Exec(ctx, `INSERT INTO audit_entries(project_id,actor_id,action,resource_key) VALUES($1,$2,'formula_metric.draft_saved',$3)`, project, actor, id); e != nil {
		return out, e
	}
	out, e = readFormulaMetricDraftTx(ctx, tx, project, id)
	if e != nil {
		return out, e
	}
	return out, tx.Commit(ctx)
}

func formulaSnapshotTx(ctx context.Context, tx pgx.Tx, store *Store, actor, project, id string, in reporting.FormulaMetricValidate) (reporting.FormulaMetricSnapshot, error) {
	var out reporting.FormulaMetricSnapshot
	draft, e := readFormulaMetricDraftTx(ctx, tx, project, id)
	if e != nil {
		return out, e
	}
	if draft.ArchivedAt != nil {
		return out, fault.New(fault.Conflict, "FORMULA_METRIC_ARCHIVED", "复合指标已归档，请先恢复后再校验或发布")
	}
	if in.DraftRevision != draft.DraftRevision {
		return out, metricConflict("METRIC_REVISION_CONFLICT")
	}
	if in.ExpectedPublishedVersion != draft.PublishedVersion {
		return out, metricConflict("PUBLISHED_VERSION_CONFLICT")
	}
	if draft.PublishedVersion > 0 {
		version, e := readFormulaMetricVersionTx(ctx, tx, project, id, draft.PublishedVersion)
		if e != nil {
			return out, e
		}
		out.Baseline = &version
	}
	// Resolve the graph while the transaction is open; authorization, scope and
	// publication of every referenced metric are enforced through the resolver.
	normalized, e := reporting.NormalizeFormulaMetricDefinition(draft.Definition)
	if e != nil {
		return out, e
	}
	self := reporting.MetricReference{MetricID: draft.ID, Version: draft.PublishedVersion}
	resolver := &pgFormulaResolver{tx: tx, store: store, actor: actor, project: project, environment: draft.Environment}
	graph, e := reporting.ValidateFormulaDAG(ctx, self, normalized.Formula, resolver)
	if e != nil {
		return out, e
	}
	if e = reporting.VerifyFormulaUnit(normalized.Formula, graph); e != nil {
		return out, e
	}
	out.Draft = draft
	out.Graph = graph
	return out, nil
}

func (s *Store) FormulaMetricValidationSnapshot(ctx context.Context, actor, project, id string, in reporting.FormulaMetricValidate) (reporting.FormulaMetricSnapshot, error) {
	tx, e := s.catalogTx(ctx, actor, project, false)
	if e != nil {
		return reporting.FormulaMetricSnapshot{}, e
	}
	defer tx.Rollback(context.Background())
	if e = s.authorizeFormulaResource(ctx, tx, actor, project, id, "formula_metrics", "metric_id", "metrics.write"); e != nil {
		return reporting.FormulaMetricSnapshot{}, e
	}
	if _, e = tx.Exec(ctx, `SELECT id FROM projects WHERE id=$1 FOR SHARE`, project); e != nil {
		return reporting.FormulaMetricSnapshot{}, e
	}
	out, e := formulaSnapshotTx(ctx, tx, s, actor, project, id, in)
	if e != nil {
		return reporting.FormulaMetricSnapshot{}, e
	}
	return out, tx.Commit(ctx)
}

func (s *Store) PublishFormulaMetric(ctx context.Context, actor, project, id, key string, in reporting.FormulaMetricPublish) (reporting.FormulaMetricVersion, error) {
	var out reporting.FormulaMetricVersion
	tx, e := s.catalogTx(ctx, actor, project, true)
	if e != nil {
		return out, e
	}
	defer tx.Rollback(context.Background())
	if e = s.authorizeFormulaResource(ctx, tx, actor, project, id, "formula_metrics", "metric_id", "metrics.publish"); e != nil {
		return out, e
	}
	if e = metricKey(key); e != nil {
		return out, e
	}
	draft, e := readFormulaMetricDraftTx(ctx, tx, project, id)
	if e != nil {
		return out, e
	}
	operation := "formula_publish:" + id
	hash, e := canonical.Hash(struct {
		MetricID string                         `json:"metric_id"`
		Input    reporting.FormulaMetricPublish `json:"input"`
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
	snapshot, e := formulaSnapshotTx(ctx, tx, s, actor, project, id, reporting.FormulaMetricValidate{DraftRevision: in.DraftRevision, ExpectedPublishedVersion: in.ExpectedPublishedVersion})
	if e != nil {
		return out, e
	}
	validation, e := reporting.BuildFormulaMetricValidation(snapshot)
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
	if _, e = tx.Exec(ctx, `INSERT INTO formula_metric_versions(project_id,metric_id,environment,version,draft_revision,definition,dependencies,definition_hash,dependency_hash,validation_hash,created_by) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)`, project, id, draft.Environment, version, draft.DraftRevision, definition, deps, validation.DefinitionHash, validation.DependencyHash, validation.ValidationHash, actor); e != nil {
		return out, e
	}
	// Direct references that are not atomic leaves are themselves composite
	// versions, so their witness must target formula_metric_versions.
	atomicLeaves := make(map[string]bool, len(snapshot.Graph.AtomicLeaves))
	for _, leaf := range snapshot.Graph.AtomicLeaves {
		atomicLeaves[leaf.MetricID+"#"+strconv.FormatInt(leaf.Version, 10)] = true
	}
	for _, dep := range validation.Dependencies.Metrics {
		var result pgconn.CommandTag
		if atomicLeaves[dep.MetricID+"#"+strconv.FormatInt(dep.Version, 10)] {
			result, e = tx.Exec(ctx, `INSERT INTO formula_metric_dependencies(project_id,environment,metric_id,version,ref_metric_id,ref_version,ref_atomic_metric_id,ref_atomic_version,definition_hash) VALUES($1,$2,$3,$4,$5,$6,$5,$6,$7)`, project, draft.Environment, id, version, dep.MetricID, dep.Version, dep.DefinitionHash)
		} else {
			result, e = tx.Exec(ctx, `INSERT INTO formula_metric_dependencies(project_id,environment,metric_id,version,ref_metric_id,ref_version,ref_formula_metric_id,ref_formula_version,definition_hash) VALUES($1,$2,$3,$4,$5,$6,$5,$6,$7)`, project, draft.Environment, id, version, dep.MetricID, dep.Version, dep.DefinitionHash)
		}
		if e != nil {
			return out, e
		}
		if result.RowsAffected() != 1 {
			return out, analysis.Invalid("INVALID_METRIC_REFERENCE", "指标依赖见证写入失败", "/formula/expression")
		}
	}
	if _, e = tx.Exec(ctx, `UPDATE formula_metrics SET published_version=$3,updated_at=clock_timestamp() WHERE project_id=$1 AND metric_id=$2`, project, id, version); e != nil {
		return out, e
	}
	if _, e = tx.Exec(ctx, `INSERT INTO audit_entries(project_id,actor_id,action,resource_key) VALUES($1,$2,'formula_metric.published',$3)`, project, actor, id); e != nil {
		return out, e
	}
	out, e = readFormulaMetricVersionTx(ctx, tx, project, id, version)
	if e != nil {
		return out, e
	}
	if e = metricIdempotencyWrite(ctx, tx, actor, project, draft.Environment, operation, key, hash, out); e != nil {
		return out, e
	}
	return out, tx.Commit(ctx)
}

func (s *Store) GetFormulaMetricVersion(ctx context.Context, actor, project, id string, version int64) (reporting.FormulaMetricVersion, error) {
	if version < 1 || version > analysis.MaxSafeInteger {
		return reporting.FormulaMetricVersion{}, analysis.Invalid("INVALID_VERSION", "指标版本无效")
	}
	tx, e := s.catalogTx(ctx, actor, project, false)
	if e != nil {
		return reporting.FormulaMetricVersion{}, e
	}
	defer tx.Rollback(context.Background())
	if e = s.authorizeFormulaResource(ctx, tx, actor, project, id, "formula_metric_versions", "metric_id", "metrics.read"); e != nil {
		return reporting.FormulaMetricVersion{}, e
	}
	out, e := readFormulaMetricVersionTx(ctx, tx, project, id, version)
	if e != nil {
		return out, e
	}
	return out, tx.Commit(ctx)
}

func (s *Store) ListFormulaMetricVersions(ctx context.Context, actor, project, id string, after int64, limit int) ([]reporting.FormulaMetricVersion, error) {
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
	if e = s.authorizeFormulaResource(ctx, tx, actor, project, id, "formula_metrics", "metric_id", "metrics.read"); e != nil {
		return nil, e
	}
	if _, e = scanFormulaMetricRecord(tx.QueryRow(ctx, `SELECT `+formulaMetricColumns+` FROM formula_metrics WHERE project_id=$1 AND metric_id=$2`, project, id)); e != nil {
		return nil, e
	}
	rows, e := tx.Query(ctx, `SELECT `+formulaMetricVersionColumns+` FROM formula_metric_versions WHERE project_id=$1 AND metric_id=$2 AND version>$3 ORDER BY version LIMIT $4`, project, id, after, limit)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []reporting.FormulaMetricVersion{}
	for rows.Next() {
		v, e := scanFormulaMetricVersion(rows)
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
