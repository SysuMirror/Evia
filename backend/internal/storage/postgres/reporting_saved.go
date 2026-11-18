package postgres

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"sort"
	"time"

	"github.com/howtomakeaname/Evia/backend/internal/analysis"
	"github.com/howtomakeaname/Evia/backend/internal/platform/canonical"
	"github.com/howtomakeaname/Evia/backend/internal/platform/fault"
	"github.com/howtomakeaname/Evia/backend/internal/platform/ids"
	"github.com/howtomakeaname/Evia/backend/internal/reporting"
	"github.com/jackc/pgx/v5"
)

var _ reporting.SavedAnalysisStore = (*Store)(nil)

const savedAnalysisColumns = `analysis_id::text,project_id::text,environment,title,kind,draft_revision,COALESCE(published_version,0),created_at,updated_at,archived_at,COALESCE(archived_by::text,'')`
const savedVersionColumns = `analysis_id::text,project_id::text,environment,version,draft_revision,definition,dependencies,definition_hash,dependency_hash,validation_hash,created_at,created_by::text`

func savedMissing() error {
	return fault.New(fault.NotFound, "SAVED_ANALYSIS_NOT_FOUND", "保存分析不存在或不可见")
}
func savedVersionMissing() error {
	return fault.New(fault.NotFound, "SAVED_ANALYSIS_VERSION_NOT_FOUND", "保存分析版本不存在或不可见")
}
func savedConflict(code string) error {
	return fault.New(fault.Conflict, code, "保存分析已变更，请重新加载并校验")
}
func scanSavedAnalysis(row pgx.Row) (reporting.SavedAnalysis, error) {
	var out reporting.SavedAnalysis
	e := row.Scan(&out.ID, &out.ProjectID, &out.Environment, &out.Title, &out.Kind, &out.DraftRevision, &out.PublishedVersion, &out.CreatedAt, &out.UpdatedAt, &out.ArchivedAt, &out.ArchivedBy)
	if errors.Is(e, pgx.ErrNoRows) {
		e = savedMissing()
	}
	out.CreatedAt = out.CreatedAt.UTC()
	out.UpdatedAt = out.UpdatedAt.UTC()
	if out.ArchivedAt != nil {
		archivedAt := out.ArchivedAt.UTC()
		out.ArchivedAt = &archivedAt
	}
	return out, e
}
func decodeSaved(data []byte, out any) error {
	d := json.NewDecoder(bytes.NewReader(data))
	d.UseNumber()
	return d.Decode(out)
}
func readSavedAnalysisDraftTx(ctx context.Context, tx pgx.Tx, project, id string) (reporting.SavedAnalysisDraft, error) {
	var out reporting.SavedAnalysisDraft
	var data []byte
	e := tx.QueryRow(ctx, `SELECT `+savedAnalysisColumns+`,definition FROM saved_analyses WHERE project_id=$1 AND analysis_id=$2`, project, id).Scan(&out.ID, &out.ProjectID, &out.Environment, &out.Title, &out.Kind, &out.DraftRevision, &out.PublishedVersion, &out.CreatedAt, &out.UpdatedAt, &out.ArchivedAt, &out.ArchivedBy, &data)
	if errors.Is(e, pgx.ErrNoRows) {
		return out, savedMissing()
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
	e = decodeSaved(data, &out.Definition)
	return out, e
}
func scanSavedVersion(row pgx.Row) (reporting.SavedAnalysisVersion, error) {
	var out reporting.SavedAnalysisVersion
	var def, deps []byte
	e := row.Scan(&out.AnalysisID, &out.ProjectID, &out.Environment, &out.Version, &out.DraftRevision, &def, &deps, &out.DefinitionHash, &out.DependencyHash, &out.ValidationHash, &out.CreatedAt, &out.CreatedBy)
	if errors.Is(e, pgx.ErrNoRows) {
		return out, savedVersionMissing()
	}
	if e != nil {
		return out, e
	}
	out.CreatedAt = out.CreatedAt.UTC()
	if e = decodeSaved(def, &out.Definition); e != nil {
		return out, e
	}
	e = decodeSaved(deps, &out.Dependencies)
	return out, e
}

// Caller must authorize current project membership and hold the project lock
// required for its operation. Published rows are immutable.
func readSavedAnalysisVersionTx(ctx context.Context, tx pgx.Tx, project, id string, version int64) (reporting.SavedAnalysisVersion, error) {
	return scanSavedVersion(tx.QueryRow(ctx, `SELECT `+savedVersionColumns+` FROM saved_analysis_versions WHERE project_id=$1 AND analysis_id=$2 AND version=$3`, project, id, version))
}
func savedIdempotencyReplay(ctx context.Context, tx pgx.Tx, actor, project, environment, operation, key, hash string, out any) (bool, error) {
	var old string
	var data []byte
	e := tx.QueryRow(ctx, `SELECT request_hash,response FROM saved_analysis_idempotency WHERE actor_id=$1 AND project_id=$2 AND environment=$3 AND operation=$4 AND idempotency_key=$5`, actor, project, environment, operation, key).Scan(&old, &data)
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
func savedIdempotencyWrite(ctx context.Context, tx pgx.Tx, actor, project, environment, operation, key, hash string, out any) error {
	data, e := json.Marshal(out)
	if e != nil {
		return e
	}
	_, e = tx.Exec(ctx, `INSERT INTO saved_analysis_idempotency(actor_id,project_id,environment,operation,idempotency_key,request_hash,response) VALUES($1,$2,$3,$4,$5,$6,$7)`, actor, project, environment, operation, key, hash, data)
	return e
}
func savedKey(key string) error {
	if len(key) < 1 || len(key) > 200 {
		return analysis.Invalid("INVALID_IDEMPOTENCY_KEY", "幂等键无效")
	}
	return nil
}
func savedEnvironment(environment string) error {
	switch environment {
	case "development", "staging", "production":
		return nil
	}
	return analysis.Invalid("INVALID_ENVIRONMENT", "环境无效", "/environment")
}

// resolveSavedDefinitionTx expands a metric-referencing saved analysis into its
// executable query inside the authorized transaction. Inline definitions are
// returned unchanged. The referenced metric version must be published, belong
// to the same project and environment, and be readable under metrics.read; the
// immutable version (never a latest pointer) supplies the derived query.
func (s *Store) resolveSavedDefinitionTx(ctx context.Context, tx pgx.Tx, actor, project, environment string, in reporting.SavedAnalysisDefinition) (reporting.SavedAnalysisDefinition, error) {
	if in.MetricRef == nil {
		return in, nil
	}
	if in.MetricRef.IsComposite() {
		formula, e := readFormulaMetricVersionTx(ctx, tx, project, in.MetricRef.MetricID, in.MetricRef.Version)
		if e != nil {
			return in, e
		}
		if formula.Environment != environment {
			return in, analysis.Invalid("INVALID_METRIC_REFERENCE", "指标引用必须属于同一项目与环境", "/metric_ref")
		}
		if e = s.authorizeOperation(ctx, tx, actor, project, formula.Environment, "metrics.read"); e != nil {
			return in, e
		}
		return reporting.ResolveCompositeMetricReference(in, formula)
	}
	metric, e := readMetricVersionTx(ctx, tx, project, in.MetricRef.MetricID, in.MetricRef.Version)
	if e != nil {
		return in, e
	}
	if metric.Environment != environment {
		return in, analysis.Invalid("INVALID_METRIC_REFERENCE", "指标引用必须属于同一项目与环境", "/metric_ref")
	}
	if e = s.authorizeOperation(ctx, tx, actor, project, metric.Environment, "metrics.read"); e != nil {
		return in, e
	}
	return reporting.ResolveMetricReference(in, metric)
}
func (s *Store) CreateSavedAnalysis(ctx context.Context, actor, project, key string, in reporting.SavedAnalysisCreate) (reporting.SavedAnalysisDraft, error) {
	var out reporting.SavedAnalysisDraft
	tx, e := s.catalogTx(ctx, actor, project, true)
	if e != nil {
		return out, e
	}
	defer tx.Rollback(context.Background())
	if e = s.authorizeOperation(ctx, tx, actor, project, in.Environment, "analyses.write"); e != nil {
		return out, e
	}
	if e = savedKey(key); e != nil {
		return out, e
	}
	if e = savedEnvironment(in.Environment); e != nil {
		return out, e
	}
	in.Definition, e = reporting.NormalizeSavedAnalysisDefinition(in.Definition)
	if e != nil {
		return out, e
	}
	in.Definition, e = s.resolveSavedDefinitionTx(ctx, tx, actor, project, in.Environment, in.Definition)
	if e != nil {
		return out, e
	}
	hash, e := canonical.Hash(in)
	if e != nil {
		return out, e
	}
	replay, e := savedIdempotencyReplay(ctx, tx, actor, project, in.Environment, "create", key, hash, &out)
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
	_, e = tx.Exec(ctx, `INSERT INTO saved_analyses(project_id,analysis_id,environment,title,kind,definition) VALUES($1,$2,$3,$4,$5,$6)`, project, id, in.Environment, in.Definition.Title, in.Definition.Query.Kind, data)
	if e != nil {
		return out, e
	}
	out, e = readSavedAnalysisDraftTx(ctx, tx, project, id)
	if e != nil {
		return out, e
	}
	if _, e = tx.Exec(ctx, `INSERT INTO audit_entries(project_id,actor_id,action,resource_key) VALUES($1,$2,'analysis.saved_created',$3)`, project, actor, id); e != nil {
		return out, e
	}
	if e = savedIdempotencyWrite(ctx, tx, actor, project, in.Environment, "create", key, hash, out); e != nil {
		return out, e
	}
	return out, tx.Commit(ctx)
}
func (s *Store) ListSavedAnalyses(ctx context.Context, actor, project, environment, after string, limit int, archivedOnly bool, search string) ([]reporting.SavedAnalysis, error) {
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
	if e = s.authorizeOperation(ctx, tx, actor, project, environment, "analyses.read"); e != nil {
		return nil, e
	}
	// The archive predicate is selected from the validated bool; search is an
	// escaped LIKE literal passed only as a bound parameter. $5='' disables
	// the title substring filter.
	archivePredicate := "archived_at IS NULL"
	if archivedOnly {
		archivePredicate = "archived_at IS NOT NULL"
	}
	rows, e := tx.Query(ctx, `SELECT `+savedAnalysisColumns+` FROM saved_analyses WHERE project_id=$1 AND environment=$2 AND `+archivePredicate+` AND ($5='' OR title ILIKE '%' || $5 || '%' ESCAPE '\') AND ($3='' OR analysis_id>NULLIF($3,'')::uuid) ORDER BY analysis_id LIMIT $4`, project, environment, after, limit, search)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []reporting.SavedAnalysis{}
	for rows.Next() {
		v, e := scanSavedAnalysis(rows)
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

// assertSavedAnalysisActiveTx blocks mutating operations on an archived analysis.
// Caller must already hold write authorization and the project lock.
func assertSavedAnalysisActiveTx(ctx context.Context, tx pgx.Tx, project, id string) error {
	var archivedAt *time.Time
	e := tx.QueryRow(ctx, `SELECT archived_at FROM saved_analyses WHERE project_id=$1 AND analysis_id=$2`, project, id).Scan(&archivedAt)
	if errors.Is(e, pgx.ErrNoRows) {
		return savedMissing()
	}
	if e != nil {
		return e
	}
	if archivedAt != nil {
		return fault.New(fault.Conflict, "SAVED_ANALYSIS_ARCHIVED", "保存分析已归档，请先恢复后再修改")
	}
	return nil
}

func (s *Store) ArchiveSavedAnalysis(ctx context.Context, actor, project, id string) (reporting.SavedAnalysis, error) {
	tx, e := s.catalogTx(ctx, actor, project, true)
	if e != nil {
		return reporting.SavedAnalysis{}, e
	}
	defer tx.Rollback(context.Background())
	if e = s.authorizeReportingResource(ctx, tx, actor, project, id, "saved_analyses", "analysis_id", "analyses.write"); e != nil {
		return reporting.SavedAnalysis{}, e
	}
	var archivedAt *time.Time
	e = tx.QueryRow(ctx, `SELECT archived_at FROM saved_analyses WHERE project_id=$1 AND analysis_id=$2`, project, id).Scan(&archivedAt)
	if errors.Is(e, pgx.ErrNoRows) {
		return reporting.SavedAnalysis{}, savedMissing()
	}
	if e != nil {
		return reporting.SavedAnalysis{}, e
	}
	if archivedAt != nil {
		return reporting.SavedAnalysis{}, fault.New(fault.Conflict, "SAVED_ANALYSIS_ALREADY_ARCHIVED", "保存分析已处于归档状态")
	}
	if _, e = tx.Exec(ctx, `UPDATE saved_analyses SET archived_at=clock_timestamp(), archived_by=$3::uuid, updated_at=clock_timestamp() WHERE project_id=$1 AND analysis_id=$2`, project, id, actor); e != nil {
		return reporting.SavedAnalysis{}, e
	}
	if _, e = tx.Exec(ctx, `INSERT INTO audit_entries(project_id,actor_id,action,resource_key) VALUES($1,$2,'analysis.archived',$3)`, project, actor, id); e != nil {
		return reporting.SavedAnalysis{}, e
	}
	out, e := scanSavedAnalysis(tx.QueryRow(ctx, `SELECT `+savedAnalysisColumns+` FROM saved_analyses WHERE project_id=$1 AND analysis_id=$2`, project, id))
	if e != nil {
		return reporting.SavedAnalysis{}, e
	}
	return out, tx.Commit(ctx)
}

func (s *Store) UnarchiveSavedAnalysis(ctx context.Context, actor, project, id string) (reporting.SavedAnalysis, error) {
	tx, e := s.catalogTx(ctx, actor, project, true)
	if e != nil {
		return reporting.SavedAnalysis{}, e
	}
	defer tx.Rollback(context.Background())
	if e = s.authorizeReportingResource(ctx, tx, actor, project, id, "saved_analyses", "analysis_id", "analyses.write"); e != nil {
		return reporting.SavedAnalysis{}, e
	}
	var archivedAt *time.Time
	e = tx.QueryRow(ctx, `SELECT archived_at FROM saved_analyses WHERE project_id=$1 AND analysis_id=$2`, project, id).Scan(&archivedAt)
	if errors.Is(e, pgx.ErrNoRows) {
		return reporting.SavedAnalysis{}, savedMissing()
	}
	if e != nil {
		return reporting.SavedAnalysis{}, e
	}
	if archivedAt == nil {
		return reporting.SavedAnalysis{}, fault.New(fault.Conflict, "SAVED_ANALYSIS_NOT_ARCHIVED", "保存分析未归档，无需恢复")
	}
	if _, e = tx.Exec(ctx, `UPDATE saved_analyses SET archived_at=NULL, archived_by=NULL, updated_at=clock_timestamp() WHERE project_id=$1 AND analysis_id=$2`, project, id); e != nil {
		return reporting.SavedAnalysis{}, e
	}
	if _, e = tx.Exec(ctx, `INSERT INTO audit_entries(project_id,actor_id,action,resource_key) VALUES($1,$2,'analysis.unarchived',$3)`, project, actor, id); e != nil {
		return reporting.SavedAnalysis{}, e
	}
	out, e := scanSavedAnalysis(tx.QueryRow(ctx, `SELECT `+savedAnalysisColumns+` FROM saved_analyses WHERE project_id=$1 AND analysis_id=$2`, project, id))
	if e != nil {
		return reporting.SavedAnalysis{}, e
	}
	return out, tx.Commit(ctx)
}

func (s *Store) GetSavedAnalysis(ctx context.Context, actor, project, id string) (reporting.SavedAnalysis, error) {
	tx, e := s.catalogTx(ctx, actor, project, false)
	if e != nil {
		return reporting.SavedAnalysis{}, e
	}
	defer tx.Rollback(context.Background())
	if e = s.authorizeReportingResource(ctx, tx, actor, project, id, "saved_analyses", "analysis_id", "analyses.read"); e != nil {
		return reporting.SavedAnalysis{}, e
	}
	out, e := scanSavedAnalysis(tx.QueryRow(ctx, `SELECT `+savedAnalysisColumns+` FROM saved_analyses WHERE project_id=$1 AND analysis_id=$2`, project, id))
	if e != nil {
		return out, e
	}
	return out, tx.Commit(ctx)
}
func (s *Store) GetSavedAnalysisDraft(ctx context.Context, actor, project, id string) (reporting.SavedAnalysisDraft, error) {
	tx, e := s.catalogTx(ctx, actor, project, false)
	if e != nil {
		return reporting.SavedAnalysisDraft{}, e
	}
	defer tx.Rollback(context.Background())
	if e = s.authorizeReportingResource(ctx, tx, actor, project, id, "saved_analyses", "analysis_id", "analyses.read"); e != nil {
		return reporting.SavedAnalysisDraft{}, e
	}
	out, e := readSavedAnalysisDraftTx(ctx, tx, project, id)
	if e != nil {
		return out, e
	}
	return out, tx.Commit(ctx)
}
func (s *Store) SaveSavedAnalysisDraft(ctx context.Context, actor, project, id string, revision int64, in reporting.SavedAnalysisDefinition) (reporting.SavedAnalysisDraft, error) {
	var out reporting.SavedAnalysisDraft
	tx, e := s.catalogTx(ctx, actor, project, true)
	if e != nil {
		return out, e
	}
	defer tx.Rollback(context.Background())
	if e = s.authorizeReportingResource(ctx, tx, actor, project, id, "saved_analyses", "analysis_id", "analyses.write"); e != nil {
		return out, e
	}
	old, e := readSavedAnalysisDraftTx(ctx, tx, project, id)
	if e != nil {
		return out, e
	}
	if old.ArchivedAt != nil {
		return out, fault.New(fault.Conflict, "SAVED_ANALYSIS_ARCHIVED", "保存分析已归档，请先恢复后再修改")
	}
	if revision < 1 {
		return out, fault.New(fault.Invalid, "REVISION_REQUIRED", "需要草稿修订号")
	}
	if old.DraftRevision != revision {
		return out, savedConflict("SAVED_ANALYSIS_REVISION_CONFLICT")
	}
	if revision >= analysis.MaxSafeInteger {
		return out, savedConflict("SAVED_ANALYSIS_REVISION_EXHAUSTED")
	}
	in, e = reporting.NormalizeSavedAnalysisDefinition(in)
	if e != nil {
		return out, e
	}
	in, e = s.resolveSavedDefinitionTx(ctx, tx, actor, project, old.Environment, in)
	if e != nil {
		return out, e
	}
	data, e := json.Marshal(in)
	if e != nil {
		return out, e
	}
	if _, e = tx.Exec(ctx, `UPDATE saved_analyses SET definition=$3,title=$4,kind=$5,draft_revision=draft_revision+1,updated_at=clock_timestamp() WHERE project_id=$1 AND analysis_id=$2`, project, id, data, in.Title, in.Query.Kind); e != nil {
		return out, e
	}
	if _, e = tx.Exec(ctx, `INSERT INTO audit_entries(project_id,actor_id,action,resource_key) VALUES($1,$2,'analysis.saved_draft_saved',$3)`, project, actor, id); e != nil {
		return out, e
	}
	out, e = readSavedAnalysisDraftTx(ctx, tx, project, id)
	if e != nil {
		return out, e
	}
	return out, tx.Commit(ctx)
}

func savedAnalysisSnapshotTx(ctx context.Context, tx pgx.Tx, project, id string, in reporting.SavedAnalysisValidate) (reporting.SavedAnalysisSnapshot, error) {
	var out reporting.SavedAnalysisSnapshot
	var e error
	out.Draft, e = readSavedAnalysisDraftTx(ctx, tx, project, id)
	if e != nil {
		return out, e
	}
	if out.Draft.ArchivedAt != nil {
		return out, fault.New(fault.Conflict, "SAVED_ANALYSIS_ARCHIVED", "保存分析已归档，请先恢复后再校验或发布")
	}
	if in.DraftRevision != out.Draft.DraftRevision {
		return out, savedConflict("SAVED_ANALYSIS_REVISION_CONFLICT")
	}
	if in.ExpectedPublishedVersion != out.Draft.PublishedVersion {
		return out, savedConflict("PUBLISHED_VERSION_CONFLICT")
	}
	if out.Draft.PublishedVersion > 0 {
		v, e := readSavedAnalysisVersionTx(ctx, tx, project, id, out.Draft.PublishedVersion)
		if e != nil {
			return out, e
		}
		out.Baseline = &v
	}
	out.Draft.Definition, e = reporting.NormalizeStoredSavedDefinition(out.Draft.Definition)
	if e != nil {
		return out, e
	}
	// The projection has no execution dates or timezone. Only configuration
	// validation and catalog type resolution run, and derived types stay private.
	data, e := json.Marshal(out.Draft.Definition.Query)
	if e != nil {
		return out, e
	}
	var request analysis.Request
	if e = decodeSaved(data, &request); e != nil {
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
	if request.Funnel != nil {
		manifest.SelectionMode = "funnel_steps"
		for _, step := range request.Funnel.Steps {
			versions[analysis.EventVersion{EventKey: step.EventKey, SchemaVersion: step.SchemaVersion}] = true
		}
	}
	if request.Retention != nil {
		manifest.SelectionMode = "retention_steps"
		for _, step := range []analysis.Step{request.Retention.StartEvent, request.Retention.ReturnEvent} {
			versions[analysis.EventVersion{EventKey: step.EventKey, SchemaVersion: step.SchemaVersion}] = true
		}
	}
	if request.Segment != nil {
		manifest.SelectionMode = "segment_steps"
		step := request.Segment.Event
		versions[analysis.EventVersion{EventKey: step.EventKey, SchemaVersion: step.SchemaVersion}] = true
	}
	if request.Path != nil {
		if request.Path.StartEvent != nil {
			manifest.SelectionMode = "path_steps"
			step := *request.Path.StartEvent
			versions[analysis.EventVersion{EventKey: step.EventKey, SchemaVersion: step.SchemaVersion}] = true
		} else {
			manifest.SelectionMode = "all_events"
		}
	}
	if request.Attribution != nil {
		manifest.SelectionMode = "attribution_steps"
		for _, step := range append([]analysis.Step{request.Attribution.Conversion}, request.Attribution.Touchpoints...) {
			versions[analysis.EventVersion{EventKey: step.EventKey, SchemaVersion: step.SchemaVersion}] = true
		}
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
	// A metric-referencing analysis pins the exact immutable metric version. The
	// atomic metric's own event versions are still recorded above, so the
	// dependency set stays self-contained; the digest fails closed if the metric
	// is unavailable. A composite pin records the formula version digest instead;
	// the formula's atomic leaves stay pinned inside the immutable formula version.
	if ref := out.Draft.Definition.MetricRef; ref != nil {
		if ref.IsComposite() {
			formula, e := readFormulaMetricVersionTx(ctx, tx, project, ref.MetricID, ref.Version)
			if e != nil {
				return out, e
			}
			if formula.Environment != out.Draft.Environment {
				return out, analysis.Invalid("INVALID_METRIC_REFERENCE", "指标引用必须属于同一项目与环境", "/metric_ref")
			}
			manifest.Metric = &reporting.MetricDependency{MetricID: formula.MetricID, Version: formula.Version, DefinitionHash: formula.DefinitionHash}
		} else {
			metric, e := readMetricVersionTx(ctx, tx, project, ref.MetricID, ref.Version)
			if e != nil {
				return out, e
			}
			if metric.Environment != out.Draft.Environment {
				return out, analysis.Invalid("INVALID_METRIC_REFERENCE", "指标引用必须属于同一项目与环境", "/metric_ref")
			}
			manifest.Metric = &reporting.MetricDependency{MetricID: metric.MetricID, Version: metric.Version, DefinitionHash: metric.DefinitionHash}
		}
	}
	out.Dependencies = manifest
	return out, nil
}
func (s *Store) SavedAnalysisValidationSnapshot(ctx context.Context, actor, project, id string, in reporting.SavedAnalysisValidate) (reporting.SavedAnalysisSnapshot, error) {
	tx, e := s.catalogTx(ctx, actor, project, false)
	if e != nil {
		return reporting.SavedAnalysisSnapshot{}, e
	}
	defer tx.Rollback(context.Background())
	if e = s.authorizeReportingResource(ctx, tx, actor, project, id, "saved_analyses", "analysis_id", "analyses.read"); e != nil {
		return reporting.SavedAnalysisSnapshot{}, e
	}
	if _, e = tx.Exec(ctx, `SELECT id FROM projects WHERE id=$1 FOR SHARE`, project); e != nil {
		return reporting.SavedAnalysisSnapshot{}, e
	}
	out, e := savedAnalysisSnapshotTx(ctx, tx, project, id, in)
	if e != nil {
		return out, e
	}
	return out, tx.Commit(ctx)
}
func (s *Store) PublishSavedAnalysis(ctx context.Context, actor, project, id, key string, in reporting.SavedAnalysisPublish) (reporting.SavedAnalysisVersion, error) {
	var out reporting.SavedAnalysisVersion
	tx, e := s.catalogTx(ctx, actor, project, true)
	if e != nil {
		return out, e
	}
	defer tx.Rollback(context.Background())
	if e = s.authorizeReportingResource(ctx, tx, actor, project, id, "saved_analyses", "analysis_id", "analyses.publish"); e != nil {
		return out, e
	}
	if e = savedKey(key); e != nil {
		return out, e
	}
	draft, e := readSavedAnalysisDraftTx(ctx, tx, project, id)
	if e != nil {
		return out, e
	}
	operation := "publish:" + id
	hash, e := canonical.Hash(struct {
		AnalysisID string                         `json:"analysis_id"`
		Input      reporting.SavedAnalysisPublish `json:"input"`
	}{id, in})
	if e != nil {
		return out, e
	}
	replay, e := savedIdempotencyReplay(ctx, tx, actor, project, draft.Environment, operation, key, hash, &out)
	if e != nil {
		return out, e
	}
	if replay {
		return out, tx.Commit(ctx)
	}
	snapshot, e := savedAnalysisSnapshotTx(ctx, tx, project, id, reporting.SavedAnalysisValidate{DraftRevision: in.DraftRevision, ExpectedPublishedVersion: in.ExpectedPublishedVersion})
	if e != nil {
		return out, e
	}
	validation, e := reporting.BuildSavedAnalysisValidation(snapshot)
	if e != nil {
		return out, e
	}
	if validation.ValidationHash != in.ValidationHash {
		return out, savedConflict("VALIDATION_HASH_CONFLICT")
	}
	if draft.PublishedVersion >= analysis.MaxSafeInteger {
		return out, savedConflict("SAVED_ANALYSIS_VERSION_EXHAUSTED")
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
	if _, e = tx.Exec(ctx, `INSERT INTO saved_analysis_versions(project_id,analysis_id,environment,version,draft_revision,definition,dependencies,definition_hash,dependency_hash,validation_hash,created_by) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)`, project, id, draft.Environment, version, draft.DraftRevision, definition, deps, validation.DefinitionHash, validation.DependencyHash, validation.ValidationHash, actor); e != nil {
		return out, e
	}
	// A deterministic existing membership row witnesses publication in this exact
	// environment. No catalog manifest is reparsed or copied.
	for _, d := range validation.Dependencies.EventVersions {
		result, e := tx.Exec(ctx, `INSERT INTO saved_analysis_dependencies(project_id,environment,analysis_id,version,event_key,schema_version,definition_hash,release_id) SELECT $1,$2,$3,$4,$5,$6,$7,re.release_id FROM catalog_release_events re WHERE re.project_id=$1 AND re.environment=$2 AND re.event_key=$5 AND re.schema_version=$6 ORDER BY re.release_id LIMIT 1`, project, draft.Environment, id, version, d.EventKey, d.SchemaVersion, d.DefinitionHash)
		if e != nil {
			return out, e
		}
		if result.RowsAffected() != 1 {
			return out, analysis.Invalid("QUERY_EVENT_VERSION_NOT_PUBLISHED", "事件版本不存在或尚未发布")
		}
	}
	if _, e = tx.Exec(ctx, `UPDATE saved_analyses SET published_version=$3,updated_at=clock_timestamp() WHERE project_id=$1 AND analysis_id=$2`, project, id, version); e != nil {
		return out, e
	}
	if _, e = tx.Exec(ctx, `INSERT INTO audit_entries(project_id,actor_id,action,resource_key) VALUES($1,$2,'analysis.saved_published',$3)`, project, actor, id); e != nil {
		return out, e
	}
	out, e = readSavedAnalysisVersionTx(ctx, tx, project, id, version)
	if e != nil {
		return out, e
	}
	if e = savedIdempotencyWrite(ctx, tx, actor, project, draft.Environment, operation, key, hash, out); e != nil {
		return out, e
	}
	return out, tx.Commit(ctx)
}
func (s *Store) GetSavedAnalysisVersion(ctx context.Context, actor, project, id string, version int64) (reporting.SavedAnalysisVersion, error) {
	tx, e := s.catalogTx(ctx, actor, project, false)
	if e != nil {
		return reporting.SavedAnalysisVersion{}, e
	}
	defer tx.Rollback(context.Background())
	if e = s.authorizeReportingResource(ctx, tx, actor, project, id, "saved_analysis_versions", "analysis_id", "analyses.read"); e != nil {
		return reporting.SavedAnalysisVersion{}, e
	}
	out, e := readSavedAnalysisVersionTx(ctx, tx, project, id, version)
	if e != nil {
		return out, e
	}
	return out, tx.Commit(ctx)
}
func (s *Store) ListSavedAnalysisVersions(ctx context.Context, actor, project, id string, after int64, limit int) ([]reporting.SavedAnalysisVersion, error) {
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
	if e = s.authorizeReportingResource(ctx, tx, actor, project, id, "saved_analyses", "analysis_id", "analyses.read"); e != nil {
		return nil, e
	}
	if _, e = scanSavedAnalysis(tx.QueryRow(ctx, `SELECT `+savedAnalysisColumns+` FROM saved_analyses WHERE project_id=$1 AND analysis_id=$2`, project, id)); e != nil {
		return nil, e
	}
	rows, e := tx.Query(ctx, `SELECT `+savedVersionColumns+` FROM saved_analysis_versions WHERE project_id=$1 AND analysis_id=$2 AND version>$3 ORDER BY version LIMIT $4`, project, id, after, limit)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []reporting.SavedAnalysisVersion{}
	for rows.Next() {
		v, e := scanSavedVersion(rows)
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
