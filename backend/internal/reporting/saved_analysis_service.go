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

type SavedAnalyses struct {
	store              SavedAnalysisStore
	validator          analysis.Validator
	executionAvailable bool
}

var _ SavedAnalysisService = (*SavedAnalyses)(nil)

func NewSavedAnalyses(store SavedAnalysisStore, validator analysis.Validator, executionAvailable bool) *SavedAnalyses {
	return &SavedAnalyses{store: store, validator: validator, executionAvailable: executionAvailable}
}
func (s *SavedAnalyses) checkInput(name string, in any) error {
	if s.validator == nil {
		return fault.New(fault.Unavailable, "REPORTING_VALIDATOR_UNAVAILABLE", "保存分析验证器不可用")
	}
	raw, e := json.Marshal(in)
	if e != nil {
		return analysis.Invalid("INVALID_SAVED_ANALYSIS", "保存分析输入不是有效 JSON")
	}
	value, e := contract.Decode(raw)
	if e != nil {
		return analysis.Invalid("INVALID_SAVED_ANALYSIS", "保存分析输入不是有效 JSON")
	}
	if e = s.validator.Validate(name, value); e != nil {
		return analysis.Invalid("INVALID_SAVED_ANALYSIS", "保存分析输入不符合接口约定")
	}
	return nil
}
func savedIdempotencyKey(key string) error {
	if len(key) < 1 || len(key) > 128 || strings.IndexFunc(key, func(r rune) bool { return r < 33 || r > 126 }) >= 0 {
		return analysis.Invalid("INVALID_IDEMPOTENCY_KEY", "请提供 1 到 128 个可见 ASCII 字符的幂等键")
	}
	return nil
}
func savedCounters(revision, published int64) error {
	if revision < 1 || revision > analysis.MaxSafeInteger || published < 0 || published > analysis.MaxSafeInteger {
		return analysis.Invalid("INVALID_REVISION", "保存分析修订或发布版本无效")
	}
	return nil
}
func savedVersion(version int64) error {
	if version < 1 || version > analysis.MaxSafeInteger {
		return analysis.Invalid("INVALID_VERSION", "保存分析版本无效")
	}
	return nil
}
func (s *SavedAnalyses) CreateSavedAnalysis(ctx context.Context, actor, project, key string, in SavedAnalysisCreate) (SavedAnalysisDraft, error) {
	if e := savedIdempotencyKey(key); e != nil {
		return SavedAnalysisDraft{}, e
	}
	if e := s.checkInput("SavedAnalysisCreate", in); e != nil {
		return SavedAnalysisDraft{}, e
	}
	if !savedEnvironment(in.Environment) {
		return SavedAnalysisDraft{}, analysis.Invalid("INVALID_ENVIRONMENT", "环境无效", "/environment")
	}
	definition, e := NormalizeSavedAnalysisDefinition(in.Definition)
	if e != nil {
		return SavedAnalysisDraft{}, e
	}
	in.Definition = definition
	return s.store.CreateSavedAnalysis(ctx, actor, project, key, in)
}
func (s *SavedAnalyses) ListSavedAnalyses(ctx context.Context, actor, project, environment, after string, limit int, archivedOnly bool, search string) ([]SavedAnalysis, error) {
	if !savedEnvironment(environment) {
		return nil, analysis.Invalid("INVALID_ENVIRONMENT", "请选择环境", "/environment")
	}
	if limit < 1 || limit > 100 {
		return nil, analysis.Invalid("INVALID_LIMIT", "分页大小超出范围", "/limit")
	}
	search, e := NormalizeCatalogSearch(search)
	if e != nil {
		return nil, e
	}
	return s.store.ListSavedAnalyses(ctx, actor, project, environment, after, limit+1, archivedOnly, EscapeLikePattern(search))
}
func (s *SavedAnalyses) GetSavedAnalysis(ctx context.Context, actor, project, id string) (SavedAnalysis, error) {
	return s.store.GetSavedAnalysis(ctx, actor, project, id)
}
func (s *SavedAnalyses) GetSavedAnalysisDraft(ctx context.Context, actor, project, id string) (SavedAnalysisDraft, error) {
	return s.store.GetSavedAnalysisDraft(ctx, actor, project, id)
}
func (s *SavedAnalyses) SaveSavedAnalysisDraft(ctx context.Context, actor, project, id string, revision int64, definition SavedAnalysisDefinition) (SavedAnalysisDraft, error) {
	if e := savedCounters(revision, 0); e != nil {
		return SavedAnalysisDraft{}, e
	}
	if e := s.checkInput("SavedAnalysisUpdate", SavedAnalysisUpdate{Definition: definition}); e != nil {
		return SavedAnalysisDraft{}, e
	}
	normalized, e := NormalizeSavedAnalysisDefinition(definition)
	if e != nil {
		return SavedAnalysisDraft{}, e
	}
	return s.store.SaveSavedAnalysisDraft(ctx, actor, project, id, revision, normalized)
}
func (s *SavedAnalyses) ValidateSavedAnalysis(ctx context.Context, actor, project, id string, in SavedAnalysisValidate) (SavedAnalysisValidation, error) {
	if e := s.checkInput("SavedAnalysisValidate", in); e != nil {
		return SavedAnalysisValidation{}, e
	}
	if e := savedCounters(in.DraftRevision, in.ExpectedPublishedVersion); e != nil {
		return SavedAnalysisValidation{}, e
	}
	snapshot, e := s.store.SavedAnalysisValidationSnapshot(ctx, actor, project, id, in)
	if e != nil {
		return SavedAnalysisValidation{}, e
	}
	if snapshot.Draft.ID != id || snapshot.Draft.ProjectID != project {
		return SavedAnalysisValidation{}, analysis.Invalid("INVALID_SAVED_ANALYSIS", "保存分析快照范围无效")
	}
	if snapshot.Draft.DraftRevision != in.DraftRevision {
		return SavedAnalysisValidation{}, fault.New(fault.Conflict, "SAVED_ANALYSIS_REVISION_CONFLICT", "草稿已更新，请重新加载")
	}
	if snapshot.Draft.PublishedVersion != in.ExpectedPublishedVersion {
		return SavedAnalysisValidation{}, fault.New(fault.Conflict, "PUBLISHED_VERSION_CONFLICT", "发布版本已更新，请重新校验")
	}
	return BuildSavedAnalysisValidation(snapshot)
}
func (s *SavedAnalyses) PublishSavedAnalysis(ctx context.Context, actor, project, id, key string, in SavedAnalysisPublish) (SavedAnalysisVersion, error) {
	if e := savedIdempotencyKey(key); e != nil {
		return SavedAnalysisVersion{}, e
	}
	if e := s.checkInput("SavedAnalysisPublish", in); e != nil {
		return SavedAnalysisVersion{}, e
	}
	if e := savedCounters(in.DraftRevision, in.ExpectedPublishedVersion); e != nil {
		return SavedAnalysisVersion{}, e
	}
	if !savedDigest(in.ValidationHash) {
		return SavedAnalysisVersion{}, analysis.Invalid("INVALID_VALIDATION_HASH", "请提供有效的校验摘要", "/validation_hash")
	}
	return s.store.PublishSavedAnalysis(ctx, actor, project, id, key, in)
}
func (s *SavedAnalyses) ListSavedAnalysisVersions(ctx context.Context, actor, project, id string, after int64, limit int) ([]SavedAnalysisVersion, error) {
	if after < 0 || after > analysis.MaxSafeInteger {
		return nil, analysis.Invalid("INVALID_CURSOR", "分页游标已失效，请重新加载", "/cursor")
	}
	if limit < 1 || limit > 50 {
		return nil, analysis.Invalid("INVALID_LIMIT", "分页大小超出范围", "/limit")
	}
	return s.store.ListSavedAnalysisVersions(ctx, actor, project, id, after, limit+1)
}
func (s *SavedAnalyses) GetSavedAnalysisVersion(ctx context.Context, actor, project, id string, version int64) (SavedAnalysisVersion, error) {
	if e := savedVersion(version); e != nil {
		return SavedAnalysisVersion{}, e
	}
	return s.store.GetSavedAnalysisVersion(ctx, actor, project, id, version)
}
func (s *SavedAnalyses) EnqueueSavedAnalysisVersion(ctx context.Context, actor, project, id string, version int64, key string, in SavedAnalysisExecute) (analysis.Job, error) {
	if e := savedVersion(version); e != nil {
		return analysis.Job{}, e
	}
	if e := savedIdempotencyKey(key); e != nil {
		return analysis.Job{}, e
	}
	if e := s.checkInput("SavedAnalysisExecute", in); e != nil {
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
	if !s.executionAvailable {
		return analysis.Job{}, fault.New(fault.Unavailable, "QUERY_STORAGE_UNAVAILABLE", "分析执行尚未配置")
	}
	return s.store.EnqueueSavedAnalysisVersion(ctx, actor, project, id, version, key, in)
}
func (s *SavedAnalyses) ArchiveSavedAnalysis(ctx context.Context, actor, project, id string) (SavedAnalysis, error) {
	return s.store.ArchiveSavedAnalysis(ctx, actor, project, id)
}
func (s *SavedAnalyses) UnarchiveSavedAnalysis(ctx context.Context, actor, project, id string) (SavedAnalysis, error) {
	return s.store.UnarchiveSavedAnalysis(ctx, actor, project, id)
}
