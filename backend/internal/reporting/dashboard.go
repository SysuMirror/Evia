package reporting

import (
	"context"
	"github.com/howtomakeaname/Evia/backend/internal/analysis"
	"time"
)

const MaxDashboardWidgets = 12
const MaxDashboardDefinitionBytes = 64 * 1024

type DashboardLayout struct {
	X      int `json:"x"`
	Y      int `json:"y"`
	Width  int `json:"width"`
	Height int `json:"height"`
}
type DashboardWidget struct {
	ID              string          `json:"widget_id"`
	AnalysisID      string          `json:"analysis_id"`
	AnalysisVersion int64           `json:"analysis_version"`
	Title           string          `json:"title"`
	ReadingOrder    int             `json:"reading_order"`
	Layout          DashboardLayout `json:"layout"`
}
type DashboardDefinition struct {
	Title       string            `json:"title"`
	Description string            `json:"description"`
	Widgets     []DashboardWidget `json:"widgets"`
}
type Dashboard struct {
	ID               string     `json:"dashboard_id"`
	ProjectID        string     `json:"project_id"`
	Environment      string     `json:"environment"`
	Title            string     `json:"title"`
	DraftRevision    int64      `json:"draft_revision"`
	PublishedVersion int64      `json:"published_version"`
	ArchivedAt       *time.Time `json:"archived_at,omitempty"`
	ArchivedBy       string     `json:"archived_by,omitempty"`
	CreatedAt        time.Time  `json:"created_at"`
	UpdatedAt        time.Time  `json:"updated_at"`
}
type DashboardDraft struct {
	Dashboard
	Definition DashboardDefinition `json:"definition"`
}
type DashboardDependency struct {
	WidgetID        string `json:"widget_id"`
	AnalysisID      string `json:"analysis_id"`
	AnalysisVersion int64  `json:"analysis_version"`
	DefinitionHash  string `json:"definition_hash"`
	DependencyHash  string `json:"dependency_hash"`
	ValidationHash  string `json:"validation_hash"`
}
type DashboardDependencies struct {
	ProjectID   string                `json:"project_id"`
	Environment string                `json:"environment"`
	Analyses    []DashboardDependency `json:"analyses"`
}
type DashboardVersion struct {
	DashboardID    string                `json:"dashboard_id"`
	ProjectID      string                `json:"project_id"`
	Environment    string                `json:"environment"`
	Version        int64                 `json:"version"`
	DraftRevision  int64                 `json:"draft_revision"`
	Definition     DashboardDefinition   `json:"definition"`
	Dependencies   DashboardDependencies `json:"dependencies"`
	DefinitionHash string                `json:"definition_hash"`
	DependencyHash string                `json:"dependency_hash"`
	ValidationHash string                `json:"validation_hash"`
	CreatedAt      time.Time             `json:"created_at"`
	CreatedBy      string                `json:"created_by"`
}
type DashboardChange = SavedAnalysisChange

type DashboardValidation struct {
	Valid                bool                  `json:"valid"`
	ExecutionPerformed   bool                  `json:"execution_performed"`
	DraftRevision        int64                 `json:"draft_revision"`
	PublishedVersion     int64                 `json:"published_version"`
	NormalizedDefinition DashboardDefinition   `json:"normalized_definition"`
	Dependencies         DashboardDependencies `json:"dependencies"`
	DefinitionHash       string                `json:"definition_hash"`
	DependencyHash       string                `json:"dependency_hash"`
	Diff                 []SavedAnalysisChange `json:"diff"`
	ValidationHash       string                `json:"validation_hash"`
}
type DashboardCreate struct {
	Environment string              `json:"environment"`
	Definition  DashboardDefinition `json:"definition"`
}
type DashboardUpdate struct {
	Definition DashboardDefinition `json:"definition"`
}
type DashboardValidate struct {
	DraftRevision            int64 `json:"draft_revision"`
	ExpectedPublishedVersion int64 `json:"expected_published_version"`
}
type DashboardPublish struct {
	DraftRevision            int64  `json:"draft_revision"`
	ExpectedPublishedVersion int64  `json:"expected_published_version"`
	ValidationHash           string `json:"validation_hash"`
}
type DashboardWidgetFilter struct {
	WidgetID string           `json:"widget_id"`
	Filter   *analysis.Filter `json:"filter"`
}
type DashboardExecute struct {
	TimeRange     analysis.TimeRange      `json:"time_range"`
	Timezone      string                  `json:"timezone"`
	WidgetFilters []DashboardWidgetFilter `json:"widget_filters,omitempty"`
}
type DashboardSnapshot struct {
	Draft        DashboardDraft
	Baseline     *DashboardVersion
	Dependencies DashboardDependencies
}
type DashboardWidgetRun struct {
	WidgetID        string       `json:"widget_id"`
	AnalysisID      string       `json:"analysis_id"`
	AnalysisVersion int64        `json:"analysis_version"`
	Query           analysis.Job `json:"query"`
}
type DashboardRun struct {
	ID            string                  `json:"run_id"`
	DashboardID   string                  `json:"dashboard_id"`
	ProjectID     string                  `json:"project_id"`
	Environment   string                  `json:"environment"`
	Version       int64                   `json:"version"`
	CreatedBy     string                  `json:"created_by"`
	TimeRange     analysis.TimeRange      `json:"time_range"`
	Timezone      string                  `json:"timezone"`
	WidgetFilters []DashboardWidgetFilter `json:"widget_filters,omitempty"`
	State         string                  `json:"state"`
	CreatedAt     time.Time               `json:"created_at"`
	ExpiresAt     time.Time               `json:"expires_at"`
	PollAfterMS   *int                    `json:"poll_after_ms,omitempty"`
	Widgets       []DashboardWidgetRun    `json:"widgets"`
}

type DashboardStore interface {
	CreateDashboard(context.Context, string, string, string, DashboardCreate) (DashboardDraft, error)
	ListDashboards(context.Context, string, string, string, string, int) ([]Dashboard, error)
	GetDashboard(context.Context, string, string, string) (Dashboard, error)
	GetDashboardDraft(context.Context, string, string, string) (DashboardDraft, error)
	SaveDashboardDraft(context.Context, string, string, string, int64, DashboardDefinition) (DashboardDraft, error)
	DashboardValidationSnapshot(context.Context, string, string, string, DashboardValidate) (DashboardSnapshot, error)
	PublishDashboard(context.Context, string, string, string, string, DashboardPublish) (DashboardVersion, error)
	ListDashboardVersions(context.Context, string, string, string, int64, int) ([]DashboardVersion, error)
	GetDashboardVersion(context.Context, string, string, string, int64) (DashboardVersion, error)
	EnqueueDashboardRun(context.Context, string, string, string, int64, string, DashboardExecute) (DashboardRun, error)
	GetDashboardRun(context.Context, string, string, string) (DashboardRun, error)
	CancelDashboardRun(context.Context, string, string, string) (DashboardRun, error)
	ArchiveDashboard(context.Context, string, string, string) (Dashboard, error)
	UnarchiveDashboard(context.Context, string, string, string) (Dashboard, error)
}
type DashboardService interface {
	CreateDashboard(context.Context, string, string, string, DashboardCreate) (DashboardDraft, error)
	ListDashboards(context.Context, string, string, string, string, int) ([]Dashboard, error)
	GetDashboard(context.Context, string, string, string) (Dashboard, error)
	GetDashboardDraft(context.Context, string, string, string) (DashboardDraft, error)
	SaveDashboardDraft(context.Context, string, string, string, int64, DashboardDefinition) (DashboardDraft, error)
	ValidateDashboard(context.Context, string, string, string, DashboardValidate) (DashboardValidation, error)
	PublishDashboard(context.Context, string, string, string, string, DashboardPublish) (DashboardVersion, error)
	ListDashboardVersions(context.Context, string, string, string, int64, int) ([]DashboardVersion, error)
	GetDashboardVersion(context.Context, string, string, string, int64) (DashboardVersion, error)
	EnqueueDashboardRun(context.Context, string, string, string, int64, string, DashboardExecute) (DashboardRun, error)
	GetDashboardRun(context.Context, string, string, string) (DashboardRun, error)
	CancelDashboardRun(context.Context, string, string, string) (DashboardRun, error)
	ArchiveDashboard(context.Context, string, string, string) (Dashboard, error)
	UnarchiveDashboard(context.Context, string, string, string) (Dashboard, error)
}
