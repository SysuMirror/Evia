package reporting

import (
	"context"
	"encoding/json"

	"github.com/howtomakeaname/Evia/backend/internal/analysis"
	"github.com/howtomakeaname/Evia/backend/internal/platform/contract"
	"github.com/howtomakeaname/Evia/backend/internal/platform/fault"
)

type DashboardFolders struct {
	store     DashboardFolderStore
	validator analysis.Validator
}

var _ DashboardFolderService = (*DashboardFolders)(nil)

func NewDashboardFolders(store DashboardFolderStore, validator analysis.Validator) *DashboardFolders {
	return &DashboardFolders{store: store, validator: validator}
}

func (s *DashboardFolders) checkInput(name string, in any) error {
	if s.validator == nil {
		return fault.New(fault.Unavailable, "REPORTING_VALIDATOR_UNAVAILABLE", "看板验证器不可用")
	}
	raw, e := json.Marshal(in)
	if e != nil {
		return analysis.Invalid("INVALID_DASHBOARD_FOLDER", "文件夹输入不是有效 JSON")
	}
	value, e := contract.Decode(raw)
	if e != nil {
		return analysis.Invalid("INVALID_DASHBOARD_FOLDER", "文件夹输入不是有效 JSON")
	}
	if e = s.validator.Validate(name, value); e != nil {
		return analysis.Invalid("INVALID_DASHBOARD_FOLDER", "文件夹输入不符合接口约定")
	}
	return nil
}

func (s *DashboardFolders) CreateDashboardFolder(ctx context.Context, actor, project, key string, in DashboardFolderCreate) (DashboardFolder, error) {
	if e := savedIdempotencyKey(key); e != nil {
		return DashboardFolder{}, e
	}
	if e := s.checkInput("DashboardFolderCreate", in); e != nil {
		return DashboardFolder{}, e
	}
	normalized, e := NormalizeDashboardFolderCreate(in)
	if e != nil {
		return DashboardFolder{}, e
	}
	return s.store.CreateDashboardFolder(ctx, actor, project, key, normalized)
}

func (s *DashboardFolders) ListDashboardFolders(ctx context.Context, actor, project, environment, after string, limit int) ([]DashboardFolder, error) {
	if !savedEnvironment(environment) {
		return nil, analysis.Invalid("INVALID_ENVIRONMENT", "请选择环境", "/environment")
	}
	if limit < 1 || limit > 100 {
		return nil, analysis.Invalid("INVALID_LIMIT", "分页大小超出范围", "/limit")
	}
	return s.store.ListDashboardFolders(ctx, actor, project, environment, after, limit+1)
}

func (s *DashboardFolders) GetDashboardFolder(ctx context.Context, actor, project, id string) (DashboardFolder, error) {
	return s.store.GetDashboardFolder(ctx, actor, project, id)
}

func (s *DashboardFolders) UpdateDashboardFolder(ctx context.Context, actor, project, id string, revision int64, in DashboardFolderUpdate) (DashboardFolder, error) {
	if e := savedCounters(revision, 0); e != nil {
		return DashboardFolder{}, e
	}
	if e := s.checkInput("DashboardFolderUpdate", in); e != nil {
		return DashboardFolder{}, e
	}
	normalized, e := NormalizeDashboardFolderUpdate(in)
	if e != nil {
		return DashboardFolder{}, e
	}
	return s.store.UpdateDashboardFolder(ctx, actor, project, id, revision, normalized)
}

func (s *DashboardFolders) DeleteDashboardFolder(ctx context.Context, actor, project, id string) error {
	return s.store.DeleteDashboardFolder(ctx, actor, project, id)
}
