package reporting

import (
	"context"
	"encoding/json"
	"time"

	"github.com/howtomakeaname/Evia/backend/internal/analysis"
	"github.com/howtomakeaname/Evia/backend/internal/platform/contract"
	"github.com/howtomakeaname/Evia/backend/internal/platform/fault"
)

// FormulaMetrics applies the public contract and request boundaries before
// delegating to the authorized FormulaMetricStore. It mirrors the atomic
// Metrics service but exposes only the management lifecycle; execution enters
// through the query worker in a later vertical.
type FormulaMetrics struct {
	store     FormulaMetricStore
	validator analysis.Validator
}

var _ FormulaMetricService = (*FormulaMetrics)(nil)

// FormulaMetricService is the management surface of composite formula metrics.
type FormulaMetricService interface {
	CreateFormulaMetric(context.Context, string, string, string, FormulaMetricCreate) (FormulaMetricDraft, error)
	ListFormulaMetrics(context.Context, string, string, string, string, int, bool) ([]FormulaMetricRecord, error)
	GetFormulaMetric(context.Context, string, string, string) (FormulaMetricRecord, error)
	GetFormulaMetricDraft(context.Context, string, string, string) (FormulaMetricDraft, error)
	SaveFormulaMetricDraft(context.Context, string, string, string, int64, FormulaMetricDefinition) (FormulaMetricDraft, error)
	ValidateFormulaMetric(context.Context, string, string, string, FormulaMetricValidate) (FormulaMetricValidation, error)
	PublishFormulaMetric(context.Context, string, string, string, string, FormulaMetricPublish) (FormulaMetricVersion, error)
	ListFormulaMetricVersions(context.Context, string, string, string, int64, int) ([]FormulaMetricVersion, error)
	GetFormulaMetricVersion(context.Context, string, string, string, int64) (FormulaMetricVersion, error)
	EnqueueFormulaMetricVersion(context.Context, string, string, string, int64, string, MetricExecute) (analysis.Job, error)
	ArchiveFormulaMetric(context.Context, string, string, string) (FormulaMetricRecord, error)
	UnarchiveFormulaMetric(context.Context, string, string, string) (FormulaMetricRecord, error)
}

func NewFormulaMetrics(store FormulaMetricStore, validator analysis.Validator) *FormulaMetrics {
	return &FormulaMetrics{store: store, validator: validator}
}

func (s *FormulaMetrics) checkInput(name string, in any) error {
	if s.validator == nil {
		return fault.New(fault.Unavailable, "REPORTING_VALIDATOR_UNAVAILABLE", "指标验证器不可用")
	}
	raw, e := json.Marshal(in)
	if e != nil {
		return analysis.Invalid("INVALID_FORMULA_METRIC", "复合指标输入不是有效 JSON")
	}
	value, e := contract.Decode(raw)
	if e != nil {
		return analysis.Invalid("INVALID_FORMULA_METRIC", "复合指标输入不是有效 JSON")
	}
	if e = s.validator.Validate(name, value); e != nil {
		return analysis.Invalid("INVALID_FORMULA_METRIC", "复合指标输入不符合接口约定")
	}
	return nil
}

func (s *FormulaMetrics) CreateFormulaMetric(ctx context.Context, actor, project, key string, in FormulaMetricCreate) (FormulaMetricDraft, error) {
	if e := metricIdempotencyKey(key); e != nil {
		return FormulaMetricDraft{}, e
	}
	if e := s.checkInput("FormulaMetricCreate", in); e != nil {
		return FormulaMetricDraft{}, e
	}
	if !metricEnvironment(in.Environment) {
		return FormulaMetricDraft{}, analysis.Invalid("INVALID_ENVIRONMENT", "环境无效", "/environment")
	}
	definition, e := NormalizeFormulaMetricDefinition(in.Definition)
	if e != nil {
		return FormulaMetricDraft{}, e
	}
	in.Definition = definition
	return s.store.CreateFormulaMetric(ctx, actor, project, key, in)
}

func (s *FormulaMetrics) ListFormulaMetrics(ctx context.Context, actor, project, environment, after string, limit int, archivedOnly bool) ([]FormulaMetricRecord, error) {
	if !metricEnvironment(environment) {
		return nil, analysis.Invalid("INVALID_ENVIRONMENT", "请选择环境", "/environment")
	}
	if limit < 1 || limit > 100 {
		return nil, analysis.Invalid("INVALID_LIMIT", "分页大小超出范围", "/limit")
	}
	return s.store.ListFormulaMetrics(ctx, actor, project, environment, after, limit+1, archivedOnly)
}

func (s *FormulaMetrics) GetFormulaMetric(ctx context.Context, actor, project, id string) (FormulaMetricRecord, error) {
	return s.store.GetFormulaMetric(ctx, actor, project, id)
}

func (s *FormulaMetrics) GetFormulaMetricDraft(ctx context.Context, actor, project, id string) (FormulaMetricDraft, error) {
	return s.store.GetFormulaMetricDraft(ctx, actor, project, id)
}

func (s *FormulaMetrics) SaveFormulaMetricDraft(ctx context.Context, actor, project, id string, revision int64, definition FormulaMetricDefinition) (FormulaMetricDraft, error) {
	if e := metricCounters(revision, 0); e != nil {
		return FormulaMetricDraft{}, e
	}
	if e := s.checkInput("FormulaMetricUpdate", FormulaMetricUpdate{Definition: definition}); e != nil {
		return FormulaMetricDraft{}, e
	}
	normalized, e := NormalizeFormulaMetricDefinition(definition)
	if e != nil {
		return FormulaMetricDraft{}, e
	}
	return s.store.SaveFormulaMetricDraft(ctx, actor, project, id, revision, normalized)
}

func (s *FormulaMetrics) ValidateFormulaMetric(ctx context.Context, actor, project, id string, in FormulaMetricValidate) (FormulaMetricValidation, error) {
	if e := s.checkInput("FormulaMetricValidate", in); e != nil {
		return FormulaMetricValidation{}, e
	}
	if e := metricCounters(in.DraftRevision, in.ExpectedPublishedVersion); e != nil {
		return FormulaMetricValidation{}, e
	}
	snapshot, e := s.store.FormulaMetricValidationSnapshot(ctx, actor, project, id, in)
	if e != nil {
		return FormulaMetricValidation{}, e
	}
	if snapshot.Draft.ID != id || snapshot.Draft.ProjectID != project {
		return FormulaMetricValidation{}, analysis.Invalid("INVALID_FORMULA_METRIC", "复合指标快照范围无效")
	}
	if snapshot.Draft.DraftRevision != in.DraftRevision {
		return FormulaMetricValidation{}, fault.New(fault.Conflict, "METRIC_REVISION_CONFLICT", "草稿已更新，请重新加载")
	}
	if snapshot.Draft.PublishedVersion != in.ExpectedPublishedVersion {
		return FormulaMetricValidation{}, fault.New(fault.Conflict, "PUBLISHED_VERSION_CONFLICT", "发布版本已更新，请重新校验")
	}
	return BuildFormulaMetricValidation(snapshot)
}

func (s *FormulaMetrics) PublishFormulaMetric(ctx context.Context, actor, project, id, key string, in FormulaMetricPublish) (FormulaMetricVersion, error) {
	if e := metricIdempotencyKey(key); e != nil {
		return FormulaMetricVersion{}, e
	}
	if e := s.checkInput("FormulaMetricPublish", in); e != nil {
		return FormulaMetricVersion{}, e
	}
	if e := metricCounters(in.DraftRevision, in.ExpectedPublishedVersion); e != nil {
		return FormulaMetricVersion{}, e
	}
	if !metricDigest(in.ValidationHash) {
		return FormulaMetricVersion{}, analysis.Invalid("INVALID_VALIDATION_HASH", "请提供有效的校验摘要", "/validation_hash")
	}
	return s.store.PublishFormulaMetric(ctx, actor, project, id, key, in)
}

func (s *FormulaMetrics) ListFormulaMetricVersions(ctx context.Context, actor, project, id string, after int64, limit int) ([]FormulaMetricVersion, error) {
	if after < 0 || after > analysis.MaxSafeInteger {
		return nil, analysis.Invalid("INVALID_CURSOR", "分页游标已失效，请重新加载", "/cursor")
	}
	if limit < 1 || limit > 50 {
		return nil, analysis.Invalid("INVALID_LIMIT", "分页大小超出范围", "/limit")
	}
	return s.store.ListFormulaMetricVersions(ctx, actor, project, id, after, limit+1)
}

func (s *FormulaMetrics) GetFormulaMetricVersion(ctx context.Context, actor, project, id string, version int64) (FormulaMetricVersion, error) {
	if version < 1 || version > analysis.MaxSafeInteger {
		return FormulaMetricVersion{}, analysis.Invalid("INVALID_VERSION", "指标版本无效")
	}
	return s.store.GetFormulaMetricVersion(ctx, actor, project, id, version)
}

// EnqueueFormulaMetricVersion applies the same runtime boundaries as an atomic
// metric run before delegating to the authorized store, which enqueues the
// internal formula marker job under the shared query budget.
func (s *FormulaMetrics) EnqueueFormulaMetricVersion(ctx context.Context, actor, project, id string, version int64, key string, in MetricExecute) (analysis.Job, error) {
	if e := metricIdempotencyKey(key); e != nil {
		return analysis.Job{}, e
	}
	if e := s.checkInput("MetricExecute", in); e != nil {
		return analysis.Job{}, e
	}
	if version < 1 || version > analysis.MaxSafeInteger {
		return analysis.Job{}, metricVersion(version)
	}
	if in.TimeRange.From.IsZero() || in.TimeRange.To.IsZero() || !in.TimeRange.From.Before(in.TimeRange.To) || in.TimeRange.From.Year() < 1970 || in.TimeRange.To.Year() > 2100 {
		return analysis.Job{}, analysis.Invalid("INVALID_TIME_RANGE", "时间范围必须为有效的非空半开区间", "/time_range")
	}
	if in.TimeRange.To.Sub(in.TimeRange.From) > 31*24*time.Hour {
		return analysis.Job{}, analysis.Invalid("QUERY_COST_EXCEEDED", "查询时间范围超过预算", "/time_range")
	}
	if _, e := time.LoadLocation(in.Timezone); e != nil || len(in.Timezone) > 64 {
		return analysis.Job{}, analysis.Invalid("INVALID_TIMEZONE", "请选择 IANA 时区", "/timezone")
	}
	if in.Interval != "" && in.Interval != "minute" && in.Interval != "hour" && in.Interval != "day" {
		return analysis.Job{}, analysis.Invalid("INVALID_INTERVAL", "请选择时间粒度", "/interval")
	}
	return s.store.EnqueueFormulaMetricVersion(ctx, actor, project, id, version, key, in)
}
func (s *FormulaMetrics) ArchiveFormulaMetric(ctx context.Context, actor, project, id string) (FormulaMetricRecord, error) {
	return s.store.ArchiveFormulaMetric(ctx, actor, project, id)
}
func (s *FormulaMetrics) UnarchiveFormulaMetric(ctx context.Context, actor, project, id string) (FormulaMetricRecord, error) {
	return s.store.UnarchiveFormulaMetric(ctx, actor, project, id)
}
