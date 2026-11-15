package reporting

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/howtomakeaname/Evia/backend/internal/analysis"
	"github.com/howtomakeaname/Evia/backend/internal/platform/contract"
	"github.com/howtomakeaname/Evia/backend/internal/platform/fault"
)

type Metrics struct {
	store              MetricStore
	validator          analysis.Validator
	executionAvailable bool
}

var _ MetricService = (*Metrics)(nil)

func NewMetrics(store MetricStore, validator analysis.Validator, executionAvailable bool) *Metrics {
	return &Metrics{store: store, validator: validator, executionAvailable: executionAvailable}
}
func (s *Metrics) checkInput(name string, in any) error {
	if s.validator == nil {
		return fault.New(fault.Unavailable, "REPORTING_VALIDATOR_UNAVAILABLE", "指标验证器不可用")
	}
	raw, e := json.Marshal(in)
	if e != nil {
		return analysis.Invalid("INVALID_METRIC", "指标输入不是有效 JSON")
	}
	value, e := contract.Decode(raw)
	if e != nil {
		return analysis.Invalid("INVALID_METRIC", "指标输入不是有效 JSON")
	}
	if e = s.validator.Validate(name, value); e != nil {
		return analysis.Invalid("INVALID_METRIC", "指标输入不符合接口约定")
	}
	return nil
}
func metricIdempotencyKey(key string) error {
	if len(key) < 1 || len(key) > 128 || strings.IndexFunc(key, func(r rune) bool { return r < 33 || r > 126 }) >= 0 {
		return analysis.Invalid("INVALID_IDEMPOTENCY_KEY", "请提供 1 到 128 个可见 ASCII 字符的幂等键")
	}
	return nil
}
func metricCounters(revision, published int64) error {
	if revision < 1 || revision > analysis.MaxSafeInteger || published < 0 || published > analysis.MaxSafeInteger {
		return analysis.Invalid("INVALID_REVISION", "指标修订或发布版本无效")
	}
	return nil
}
func metricVersion(version int64) error {
	if version < 1 || version > analysis.MaxSafeInteger {
		return analysis.Invalid("INVALID_VERSION", "指标版本无效")
	}
	return nil
}
func (s *Metrics) CreateMetric(ctx context.Context, actor, project, key string, in MetricCreate) (MetricDraft, error) {
	if e := metricIdempotencyKey(key); e != nil {
		return MetricDraft{}, e
	}
	if e := s.checkInput("MetricCreate", in); e != nil {
		return MetricDraft{}, e
	}
	if !metricEnvironment(in.Environment) {
		return MetricDraft{}, analysis.Invalid("INVALID_ENVIRONMENT", "环境无效", "/environment")
	}
	definition, e := NormalizeMetricDefinition(in.Definition)
	if e != nil {
		return MetricDraft{}, e
	}
	in.Definition = definition
	return s.store.CreateMetric(ctx, actor, project, key, in)
}
func (s *Metrics) ListMetrics(ctx context.Context, actor, project, environment, after string, limit int, archivedOnly bool) ([]MetricRecord, error) {
	if !metricEnvironment(environment) {
		return nil, analysis.Invalid("INVALID_ENVIRONMENT", "请选择环境", "/environment")
	}
	if limit < 1 || limit > 100 {
		return nil, analysis.Invalid("INVALID_LIMIT", "分页大小超出范围", "/limit")
	}
	return s.store.ListMetrics(ctx, actor, project, environment, after, limit+1, archivedOnly)
}
func (s *Metrics) GetMetric(ctx context.Context, actor, project, id string) (MetricRecord, error) {
	return s.store.GetMetric(ctx, actor, project, id)
}
func (s *Metrics) GetMetricDraft(ctx context.Context, actor, project, id string) (MetricDraft, error) {
	return s.store.GetMetricDraft(ctx, actor, project, id)
}
func (s *Metrics) SaveMetricDraft(ctx context.Context, actor, project, id string, revision int64, definition MetricDefinition) (MetricDraft, error) {
	if e := metricCounters(revision, 0); e != nil {
		return MetricDraft{}, e
	}
	if e := s.checkInput("MetricUpdate", MetricUpdate{Definition: definition}); e != nil {
		return MetricDraft{}, e
	}
	normalized, e := NormalizeMetricDefinition(definition)
	if e != nil {
		return MetricDraft{}, e
	}
	return s.store.SaveMetricDraft(ctx, actor, project, id, revision, normalized)
}
func (s *Metrics) ValidateMetric(ctx context.Context, actor, project, id string, in MetricValidate) (MetricValidation, error) {
	if e := s.checkInput("MetricValidate", in); e != nil {
		return MetricValidation{}, e
	}
	if e := metricCounters(in.DraftRevision, in.ExpectedPublishedVersion); e != nil {
		return MetricValidation{}, e
	}
	snapshot, e := s.store.MetricValidationSnapshot(ctx, actor, project, id, in)
	if e != nil {
		return MetricValidation{}, e
	}
	if snapshot.Draft.ID != id || snapshot.Draft.ProjectID != project {
		return MetricValidation{}, analysis.Invalid("INVALID_METRIC", "指标快照范围无效")
	}
	if snapshot.Draft.DraftRevision != in.DraftRevision {
		return MetricValidation{}, fault.New(fault.Conflict, "METRIC_REVISION_CONFLICT", "草稿已更新，请重新加载")
	}
	if snapshot.Draft.PublishedVersion != in.ExpectedPublishedVersion {
		return MetricValidation{}, fault.New(fault.Conflict, "PUBLISHED_VERSION_CONFLICT", "发布版本已更新，请重新校验")
	}
	return BuildMetricValidation(snapshot)
}
func (s *Metrics) PublishMetric(ctx context.Context, actor, project, id, key string, in MetricPublish) (MetricVersion, error) {
	if e := metricIdempotencyKey(key); e != nil {
		return MetricVersion{}, e
	}
	if e := s.checkInput("MetricPublish", in); e != nil {
		return MetricVersion{}, e
	}
	if e := metricCounters(in.DraftRevision, in.ExpectedPublishedVersion); e != nil {
		return MetricVersion{}, e
	}
	if !metricDigest(in.ValidationHash) {
		return MetricVersion{}, analysis.Invalid("INVALID_VALIDATION_HASH", "请提供有效的校验摘要", "/validation_hash")
	}
	return s.store.PublishMetric(ctx, actor, project, id, key, in)
}
func (s *Metrics) ListMetricVersions(ctx context.Context, actor, project, id string, after int64, limit int) ([]MetricVersion, error) {
	if after < 0 || after > analysis.MaxSafeInteger {
		return nil, analysis.Invalid("INVALID_CURSOR", "分页游标已失效，请重新加载", "/cursor")
	}
	if limit < 1 || limit > 50 {
		return nil, analysis.Invalid("INVALID_LIMIT", "分页大小超出范围", "/limit")
	}
	return s.store.ListMetricVersions(ctx, actor, project, id, after, limit+1)
}
func (s *Metrics) GetMetricVersion(ctx context.Context, actor, project, id string, version int64) (MetricVersion, error) {
	if e := metricVersion(version); e != nil {
		return MetricVersion{}, e
	}
	return s.store.GetMetricVersion(ctx, actor, project, id, version)
}
func (s *Metrics) EnqueueMetricVersion(ctx context.Context, actor, project, id string, version int64, key string, in MetricExecute) (analysis.Job, error) {
	if e := metricVersion(version); e != nil {
		return analysis.Job{}, e
	}
	if e := metricIdempotencyKey(key); e != nil {
		return analysis.Job{}, e
	}
	if e := s.checkInput("MetricExecute", in); e != nil {
		return analysis.Job{}, e
	}
	// The exact version's Bind applies its kind-specific window budget.
	if in.TimeRange.From.IsZero() || in.TimeRange.To.IsZero() || !in.TimeRange.From.Before(in.TimeRange.To) || in.TimeRange.From.Year() < 1970 || in.TimeRange.To.Year() > 2100 {
		return analysis.Job{}, analysis.Invalid("INVALID_TIME_RANGE", "时间范围必须为有效的非空半开区间", "/time_range")
	}
	if in.TimeRange.To.Sub(in.TimeRange.From) > 31*24*time.Hour {
		return analysis.Job{}, analysis.Invalid("QUERY_COST_EXCEEDED", "查询时间范围超过预算", "/time_range")
	}
	if in.Timezone == "" || in.Timezone == "Local" || len(in.Timezone) > 64 {
		return analysis.Job{}, analysis.Invalid("INVALID_TIMEZONE", "请选择 IANA 时区", "/timezone")
	}
	if _, e := time.LoadLocation(in.Timezone); e != nil {
		return analysis.Job{}, analysis.Invalid("INVALID_TIMEZONE", "时区无效", "/timezone")
	}
	if in.Interval != "" && in.Interval != "minute" && in.Interval != "hour" && in.Interval != "day" {
		return analysis.Job{}, analysis.Invalid("INVALID_INTERVAL", "请选择时间粒度", "/interval")
	}

	if !s.executionAvailable {
		return analysis.Job{}, fault.New(fault.Unavailable, "QUERY_STORAGE_UNAVAILABLE", "分析执行尚未配置")
	}
	return s.store.EnqueueMetricVersion(ctx, actor, project, id, version, key, in)
}
func (s *Metrics) ArchiveMetric(ctx context.Context, actor, project, id string) (MetricRecord, error) {
	return s.store.ArchiveMetric(ctx, actor, project, id)
}
func (s *Metrics) UnarchiveMetric(ctx context.Context, actor, project, id string) (MetricRecord, error) {
	return s.store.UnarchiveMetric(ctx, actor, project, id)
}
