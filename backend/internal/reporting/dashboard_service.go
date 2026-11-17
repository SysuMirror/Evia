package reporting

import (
	"context"
	"encoding/json"
	"time"

	"github.com/howtomakeaname/Evia/backend/internal/analysis"
	"github.com/howtomakeaname/Evia/backend/internal/platform/contract"
	"github.com/howtomakeaname/Evia/backend/internal/platform/fault"
)

type Dashboards struct {
	store              DashboardStore
	validator          analysis.Validator
	executionAvailable bool
}

var _ DashboardService = (*Dashboards)(nil)

func NewDashboards(store DashboardStore, validator analysis.Validator, executionAvailable bool) *Dashboards {
	return &Dashboards{store: store, validator: validator, executionAvailable: executionAvailable}
}
func (s *Dashboards) checkInput(name string, in any) error {
	if s.validator == nil {
		return fault.New(fault.Unavailable, "REPORTING_VALIDATOR_UNAVAILABLE", "看板验证器不可用")
	}
	raw, e := json.Marshal(in)
	if e != nil {
		return analysis.Invalid("INVALID_DASHBOARD", "看板输入不是有效 JSON")
	}
	value, e := contract.Decode(raw)
	if e != nil {
		return analysis.Invalid("INVALID_DASHBOARD", "看板输入不是有效 JSON")
	}
	if e = s.validator.Validate(name, value); e != nil {
		return analysis.Invalid("INVALID_DASHBOARD", "看板输入不符合接口约定")
	}
	return nil
}
func (s *Dashboards) CreateDashboard(ctx context.Context, actor, project, key string, in DashboardCreate) (DashboardDraft, error) {
	if e := savedIdempotencyKey(key); e != nil {
		return DashboardDraft{}, e
	}
	if e := s.checkInput("DashboardCreate", in); e != nil {
		return DashboardDraft{}, e
	}
	if !savedEnvironment(in.Environment) {
		return DashboardDraft{}, analysis.Invalid("INVALID_ENVIRONMENT", "环境无效", "/environment")
	}
	definition, e := NormalizeDashboardDefinition(in.Definition)
	if e != nil {
		return DashboardDraft{}, e
	}
	if in.FolderID != "" && !dashboardUUID.MatchString(in.FolderID) {
		return DashboardDraft{}, analysis.Invalid("INVALID_DASHBOARD_FOLDER", "文件夹标识无效", "/folder_id")
	}
	in.Definition = definition
	return s.store.CreateDashboard(ctx, actor, project, key, in)
}
func (s *Dashboards) ListDashboards(ctx context.Context, actor, project, environment, after string, limit int, archivedOnly bool, folderScope string) ([]Dashboard, error) {
	if !savedEnvironment(environment) {
		return nil, analysis.Invalid("INVALID_ENVIRONMENT", "请选择环境", "/environment")
	}
	if limit < 1 || limit > 100 {
		return nil, analysis.Invalid("INVALID_LIMIT", "分页大小超出范围", "/limit")
	}
	folderScope, e := NormalizeDashboardFolderScope(folderScope)
	if e != nil {
		return nil, e
	}
	return s.store.ListDashboards(ctx, actor, project, environment, after, limit+1, archivedOnly, folderScope)
}
func (s *Dashboards) GetDashboard(ctx context.Context, actor, project, id string) (Dashboard, error) {
	return s.store.GetDashboard(ctx, actor, project, id)
}
func (s *Dashboards) GetDashboardDraft(ctx context.Context, actor, project, id string) (DashboardDraft, error) {
	return s.store.GetDashboardDraft(ctx, actor, project, id)
}
func (s *Dashboards) SaveDashboardDraft(ctx context.Context, actor, project, id string, revision int64, definition DashboardDefinition, folderID string) (DashboardDraft, error) {
	if e := savedCounters(revision, 0); e != nil {
		return DashboardDraft{}, e
	}
	if e := s.checkInput("DashboardUpdate", DashboardUpdate{Definition: definition, FolderID: folderID}); e != nil {
		return DashboardDraft{}, e
	}
	normalized, e := NormalizeDashboardDefinition(definition)
	if e != nil {
		return DashboardDraft{}, e
	}
	if folderID != "" && !dashboardUUID.MatchString(folderID) {
		return DashboardDraft{}, analysis.Invalid("INVALID_DASHBOARD_FOLDER", "文件夹标识无效", "/folder_id")
	}
	return s.store.SaveDashboardDraft(ctx, actor, project, id, revision, normalized, folderID)
}
func (s *Dashboards) ValidateDashboard(ctx context.Context, actor, project, id string, in DashboardValidate) (DashboardValidation, error) {
	if e := s.checkInput("DashboardValidate", in); e != nil {
		return DashboardValidation{}, e
	}
	if e := savedCounters(in.DraftRevision, in.ExpectedPublishedVersion); e != nil {
		return DashboardValidation{}, e
	}
	snapshot, e := s.store.DashboardValidationSnapshot(ctx, actor, project, id, in)
	if e != nil {
		return DashboardValidation{}, e
	}
	if snapshot.Draft.ID != id || snapshot.Draft.ProjectID != project {
		return DashboardValidation{}, analysis.Invalid("INVALID_DASHBOARD", "看板快照范围无效")
	}
	if snapshot.Draft.DraftRevision != in.DraftRevision {
		return DashboardValidation{}, fault.New(fault.Conflict, "DASHBOARD_REVISION_CONFLICT", "草稿已更新，请重新加载")
	}
	if snapshot.Draft.PublishedVersion != in.ExpectedPublishedVersion {
		return DashboardValidation{}, fault.New(fault.Conflict, "PUBLISHED_VERSION_CONFLICT", "发布版本已更新，请重新校验")
	}
	return BuildDashboardValidation(snapshot)
}
func (s *Dashboards) PublishDashboard(ctx context.Context, actor, project, id, key string, in DashboardPublish) (DashboardVersion, error) {
	if e := savedIdempotencyKey(key); e != nil {
		return DashboardVersion{}, e
	}
	if e := s.checkInput("DashboardPublish", in); e != nil {
		return DashboardVersion{}, e
	}
	if e := savedCounters(in.DraftRevision, in.ExpectedPublishedVersion); e != nil {
		return DashboardVersion{}, e
	}
	if !savedDigest(in.ValidationHash) {
		return DashboardVersion{}, analysis.Invalid("INVALID_VALIDATION_HASH", "请提供有效的校验摘要", "/validation_hash")
	}
	return s.store.PublishDashboard(ctx, actor, project, id, key, in)
}
func (s *Dashboards) ListDashboardVersions(ctx context.Context, actor, project, id string, after int64, limit int) ([]DashboardVersion, error) {
	if after < 0 || after > analysis.MaxSafeInteger {
		return nil, analysis.Invalid("INVALID_CURSOR", "分页游标已失效，请重新加载", "/cursor")
	}
	if limit < 1 || limit > 50 {
		return nil, analysis.Invalid("INVALID_LIMIT", "分页大小超出范围", "/limit")
	}
	return s.store.ListDashboardVersions(ctx, actor, project, id, after, limit+1)
}
func (s *Dashboards) GetDashboardVersion(ctx context.Context, actor, project, id string, version int64) (DashboardVersion, error) {
	if e := savedVersion(version); e != nil {
		return DashboardVersion{}, e
	}
	return s.store.GetDashboardVersion(ctx, actor, project, id, version)
}
func (s *Dashboards) EnqueueDashboardRun(ctx context.Context, actor, project, id string, version int64, key string, in DashboardExecute) (DashboardRun, error) {
	if e := savedVersion(version); e != nil {
		return DashboardRun{}, e
	}
	if e := savedIdempotencyKey(key); e != nil {
		return DashboardRun{}, e
	}
	if e := s.checkInput("DashboardExecute", in); e != nil {
		return DashboardRun{}, e
	}
	// The exact version's Bind applies its kind-specific window budget.
	if in.TimeRange.From.IsZero() || in.TimeRange.To.IsZero() || !in.TimeRange.From.Before(in.TimeRange.To) || in.TimeRange.From.Year() < 1970 || in.TimeRange.To.Year() > 2100 {
		return DashboardRun{}, analysis.Invalid("INVALID_TIME_RANGE", "时间范围必须为有效的非空半开区间", "/time_range")
	}
	if in.TimeRange.To.Sub(in.TimeRange.From) > 31*24*time.Hour {
		return DashboardRun{}, analysis.Invalid("QUERY_COST_EXCEEDED", "查询时间范围超过预算", "/time_range")
	}
	if in.Timezone == "" || in.Timezone == "Local" || len(in.Timezone) > 64 {
		return DashboardRun{}, analysis.Invalid("INVALID_TIMEZONE", "请选择 IANA 时区", "/timezone")
	}
	if _, e := time.LoadLocation(in.Timezone); e != nil {
		return DashboardRun{}, analysis.Invalid("INVALID_TIMEZONE", "时区无效", "/timezone")
	}
	if !s.executionAvailable {
		return DashboardRun{}, fault.New(fault.Unavailable, "QUERY_STORAGE_UNAVAILABLE", "分析执行尚未配置")
	}
	return s.store.EnqueueDashboardRun(ctx, actor, project, id, version, key, in)
}

func (s *Dashboards) GetDashboardRun(ctx context.Context, actor, project, id string) (DashboardRun, error) {
	return s.store.GetDashboardRun(ctx, actor, project, id)
}
func (s *Dashboards) CancelDashboardRun(ctx context.Context, actor, project, id string) (DashboardRun, error) {
	return s.store.CancelDashboardRun(ctx, actor, project, id)
}
func (s *Dashboards) CopyDashboard(ctx context.Context, actor, project, id, key string, in DashboardCopy) (DashboardDraft, error) {
	if e := savedIdempotencyKey(key); e != nil {
		return DashboardDraft{}, e
	}
	if e := s.checkInput("DashboardCopy", in); e != nil {
		return DashboardDraft{}, e
	}
	if in.FolderID != "" && !dashboardUUID.MatchString(in.FolderID) {
		return DashboardDraft{}, analysis.Invalid("INVALID_DASHBOARD_FOLDER", "文件夹标识无效", "/folder_id")
	}
	return s.store.CopyDashboard(ctx, actor, project, id, key, in)
}
func (s *Dashboards) ArchiveDashboard(ctx context.Context, actor, project, id string) (Dashboard, error) {
	return s.store.ArchiveDashboard(ctx, actor, project, id)
}
func (s *Dashboards) UnarchiveDashboard(ctx context.Context, actor, project, id string) (Dashboard, error) {
	return s.store.UnarchiveDashboard(ctx, actor, project, id)
}
