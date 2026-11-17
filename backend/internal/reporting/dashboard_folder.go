package reporting

import (
	"context"
	"strings"
	"time"

	"github.com/howtomakeaname/Evia/backend/internal/analysis"
)

// MaxDashboardFolderDepth bounds the hierarchy: a root folder is depth 1 and
// the deepest allowed node is its grandchild at depth 3.
const MaxDashboardFolderDepth = 3

// DashboardFolder is scoped organization metadata. It has no immutable
// witnesses; empty folders are hard-deleted and parent/depth are fixed at
// creation, so renames are the only supported mutation.
type DashboardFolder struct {
	ID          string `json:"folder_id"`
	ProjectID   string `json:"project_id"`
	Environment string `json:"environment"`
	// ParentID is empty for a root folder.
	ParentID  string    `json:"parent_id,omitempty"`
	Depth     int       `json:"depth"`
	Title     string    `json:"title"`
	Revision  int64     `json:"revision"`
	CreatedBy string    `json:"created_by"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}
type DashboardFolderCreate struct {
	Environment string `json:"environment"`
	// ParentID empty creates a root-level folder.
	ParentID string `json:"parent_id,omitempty"`
	Title    string `json:"title"`
}
type DashboardFolderUpdate struct {
	Title string `json:"title"`
}

type DashboardFolderStore interface {
	CreateDashboardFolder(context.Context, string, string, string, DashboardFolderCreate) (DashboardFolder, error)
	ListDashboardFolders(context.Context, string, string, string, string, int) ([]DashboardFolder, error)
	GetDashboardFolder(context.Context, string, string, string) (DashboardFolder, error)
	UpdateDashboardFolder(context.Context, string, string, string, int64, DashboardFolderUpdate) (DashboardFolder, error)
	DeleteDashboardFolder(context.Context, string, string, string) error
}
type DashboardFolderService interface {
	CreateDashboardFolder(context.Context, string, string, string, DashboardFolderCreate) (DashboardFolder, error)
	ListDashboardFolders(context.Context, string, string, string, string, int) ([]DashboardFolder, error)
	GetDashboardFolder(context.Context, string, string, string) (DashboardFolder, error)
	UpdateDashboardFolder(context.Context, string, string, string, int64, DashboardFolderUpdate) (DashboardFolder, error)
	DeleteDashboardFolder(context.Context, string, string, string) error
}

// NormalizeDashboardFolderCreate validates the request shape. Parent existence,
// same-scope membership and the depth bound are resolved transactionally
// against stored rows, never trusted from the client.
func NormalizeDashboardFolderCreate(in DashboardFolderCreate) (DashboardFolderCreate, error) {
	out := in
	out.Title = strings.TrimSpace(in.Title)
	if !savedEnvironment(in.Environment) {
		return out, analysis.Invalid("INVALID_ENVIRONMENT", "环境无效", "/environment")
	}
	if in.ParentID != "" && !dashboardUUID.MatchString(in.ParentID) {
		return out, analysis.Invalid("INVALID_DASHBOARD_FOLDER_PARENT", "父文件夹标识无效", "/parent_id")
	}
	if !dashboardText(out.Title, 128, false) {
		return out, analysis.Invalid("INVALID_DASHBOARD_FOLDER_TITLE", "文件夹标题需为 1 至 128 个字符", "/title")
	}
	return out, nil
}
func NormalizeDashboardFolderUpdate(in DashboardFolderUpdate) (DashboardFolderUpdate, error) {
	out := in
	out.Title = strings.TrimSpace(in.Title)
	if !dashboardText(out.Title, 128, false) {
		return out, analysis.Invalid("INVALID_DASHBOARD_FOLDER_TITLE", "文件夹标题需为 1 至 128 个字符", "/title")
	}
	return out, nil
}

// NormalizeDashboardFolderScope validates the list filter: empty means no
// filtering, "root" matches unfiled dashboards and a UUID matches one folder.
func NormalizeDashboardFolderScope(scope string) (string, error) {
	if scope == "" || scope == "root" || dashboardUUID.MatchString(scope) {
		return scope, nil
	}
	return "", analysis.Invalid("INVALID_QUERY", "folder_id 需为文件夹 UUID 或 root", "/folder_id")
}
