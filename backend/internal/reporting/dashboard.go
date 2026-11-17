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
	ID string `json:"widget_id"`
	// A widget pins exactly one immutable source: either a saved-analysis
	// version (AnalysisID/AnalysisVersion) or a metric version (MetricRef).
	AnalysisID      string           `json:"analysis_id,omitempty"`
	AnalysisVersion int64            `json:"analysis_version,omitempty"`
	MetricRef       *MetricReference `json:"metric_ref,omitempty"`
	// TextCard is the third, presentation-only option: mutually exclusive with
	// both executable sources and excluded from run fanout.
	TextCard     *TextCard       `json:"text_card,omitempty"`
	Title        string          `json:"title"`
	ReadingOrder int             `json:"reading_order"`
	Layout       DashboardLayout `json:"layout"`
	// ParameterBindings maps dashboard-level parameters to this widget's own
	// pinned event fields. It never stores a pseudo-global field name: each
	// binding carries the concrete field the widget's source owns. It is
	// forbidden on presentation-only text cards.
	ParameterBindings []ParameterBinding `json:"parameter_bindings,omitempty"`
}
type DashboardDefinition struct {
	Title       string                 `json:"title"`
	Description string                 `json:"description"`
	Parameters  []ParameterDeclaration `json:"parameters,omitempty"`
	Widgets     []DashboardWidget      `json:"widgets"`
}
type Dashboard struct {
	ID          string `json:"dashboard_id"`
	ProjectID   string `json:"project_id"`
	Environment string `json:"environment"`
	// FolderID empty means the dashboard stays at the project/environment root.
	FolderID         string     `json:"folder_id,omitempty"`
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
	WidgetID string `json:"widget_id"`
	// AnalysisID/AnalysisVersion are empty for a metric-sourced widget.
	AnalysisID      string `json:"analysis_id,omitempty"`
	AnalysisVersion int64  `json:"analysis_version,omitempty"`
	// MetricID/MetricVersion are empty for an analysis-sourced widget.
	MetricID       string `json:"metric_id,omitempty"`
	MetricVersion  int64  `json:"metric_version,omitempty"`
	DefinitionHash string `json:"definition_hash"`
	DependencyHash string `json:"dependency_hash"`
	ValidationHash string `json:"validation_hash"`
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
	Environment string `json:"environment"`
	// FolderID empty creates the dashboard at the root; a non-empty value must
	// reference an existing folder in the same project/environment.
	FolderID   string              `json:"folder_id,omitempty"`
	Definition DashboardDefinition `json:"definition"`
}
type DashboardUpdate struct {
	// FolderID is a full-replacement field: omitted or empty moves the
	// dashboard back to the root, a UUID moves it into that folder.
	FolderID   string              `json:"folder_id,omitempty"`
	Definition DashboardDefinition `json:"definition"`
}

// DashboardRebinding maps one executable widget to an immutable source that
// exists in the copy's target environment. Exactly one of the analysis pair or
// metric pair must be present, and it must match the source widget's kind.
// Presentation-only text cards need no entry.
type DashboardRebinding struct {
	WidgetID string `json:"widget_id"`
	// AnalysisID/AnalysisVersion identify a published saved-analysis version in
	// the target environment; both stay zero for a metric-sourced widget.
	AnalysisID      string `json:"analysis_id,omitempty"`
	AnalysisVersion int64  `json:"analysis_version,omitempty"`
	// MetricID/MetricVersion identify a published metric version in the target
	// environment; both stay zero for an analysis-sourced widget.
	MetricID      string `json:"metric_id,omitempty"`
	MetricVersion int64  `json:"metric_version,omitempty"`
}

// DashboardCopy creates a new unpublished dashboard from an existing one. With
// no TargetEnvironment it reproduces the current same-environment behavior
// (fresh dashboard/widget IDs, retained references, layout, parameters and
// bindings). When TargetEnvironment names a different closed-set environment,
// Rebindings must cover every executable widget; each target reference is
// validated against that environment by the store. Publication state, runs,
// idempotency history and archive state are never carried over.
type DashboardCopy struct {
	// Title is required so the new resource is distinguishable in listings.
	Title string `json:"title"`
	// FolderID empty places the copy at the project root; a non-empty value
	// must reference an existing folder in the target environment.
	FolderID string `json:"folder_id,omitempty"`
	// TargetEnvironment empty means the source environment. A different value
	// switches the copy to that environment and requires explicit rebinding.
	TargetEnvironment string `json:"target_environment,omitempty"`
	// Rebindings is required and exhaustive for executable widgets only when
	// crossing environments; it must be empty for a same-environment copy.
	Rebindings []DashboardRebinding `json:"rebindings,omitempty"`
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
	// ParameterValues carries runtime values keyed by the declared
	// parameter_id; unknown keys and type mismatches fail closed.
	ParameterValues ParameterValues `json:"parameter_values,omitempty"`
	// Comparison, when set, adds one shifted-window query per widget using the
	// DST-safe ShiftTimeRange derivation; nil runs the current window only.
	Comparison *ComparisonSpec `json:"comparison,omitempty"`
}
type DashboardSnapshot struct {
	Draft        DashboardDraft
	Baseline     *DashboardVersion
	Dependencies DashboardDependencies
}
type DashboardWidgetRun struct {
	WidgetID        string           `json:"widget_id"`
	AnalysisID      string           `json:"analysis_id,omitempty"`
	AnalysisVersion int64            `json:"analysis_version,omitempty"`
	MetricRef       *MetricReference `json:"metric_ref,omitempty"`
	Query           analysis.Job     `json:"query"`
	// Comparison is present only when the run requested a comparison window;
	// its job uses the same filters and granularity over the shifted range.
	Comparison *analysis.Job `json:"comparison,omitempty"`
}
type DashboardRun struct {
	ID              string                  `json:"run_id"`
	DashboardID     string                  `json:"dashboard_id"`
	ProjectID       string                  `json:"project_id"`
	Environment     string                  `json:"environment"`
	Version         int64                   `json:"version"`
	CreatedBy       string                  `json:"created_by"`
	TimeRange       analysis.TimeRange      `json:"time_range"`
	Timezone        string                  `json:"timezone"`
	WidgetFilters   []DashboardWidgetFilter `json:"widget_filters,omitempty"`
	ParameterValues ParameterValues         `json:"parameter_values,omitempty"`
	Comparison      *ComparisonSpec         `json:"comparison,omitempty"`
	State           string                  `json:"state"`
	CreatedAt       time.Time               `json:"created_at"`
	ExpiresAt       time.Time               `json:"expires_at"`
	PollAfterMS     *int                    `json:"poll_after_ms,omitempty"`
	Widgets         []DashboardWidgetRun    `json:"widgets"`
}

type DashboardStore interface {
	CreateDashboard(context.Context, string, string, string, DashboardCreate) (DashboardDraft, error)
	// folderScope is "" for no folder filter, "root" for unfiled dashboards or
	// a folder UUID; it is validated before reaching the store. search is a
	// normalized, LIKE-escaped literal title substring or "" for no filter.
	ListDashboards(context.Context, string, string, string, string, int, bool, string, string) ([]Dashboard, error)
	GetDashboard(context.Context, string, string, string) (Dashboard, error)
	GetDashboardDraft(context.Context, string, string, string) (DashboardDraft, error)
	SaveDashboardDraft(context.Context, string, string, string, int64, DashboardDefinition, string) (DashboardDraft, error)
	DashboardValidationSnapshot(context.Context, string, string, string, DashboardValidate) (DashboardSnapshot, error)
	PublishDashboard(context.Context, string, string, string, string, DashboardPublish) (DashboardVersion, error)
	ListDashboardVersions(context.Context, string, string, string, int64, int) ([]DashboardVersion, error)
	GetDashboardVersion(context.Context, string, string, string, int64) (DashboardVersion, error)
	EnqueueDashboardRun(context.Context, string, string, string, int64, string, DashboardExecute) (DashboardRun, error)
	GetDashboardRun(context.Context, string, string, string) (DashboardRun, error)
	CancelDashboardRun(context.Context, string, string, string) (DashboardRun, error)
	CopyDashboard(context.Context, string, string, string, string, DashboardCopy) (DashboardDraft, error)
	ArchiveDashboard(context.Context, string, string, string) (Dashboard, error)
	UnarchiveDashboard(context.Context, string, string, string) (Dashboard, error)
}
type DashboardService interface {
	CreateDashboard(context.Context, string, string, string, DashboardCreate) (DashboardDraft, error)
	ListDashboards(context.Context, string, string, string, string, int, bool, string, string) ([]Dashboard, error)
	GetDashboard(context.Context, string, string, string) (Dashboard, error)
	GetDashboardDraft(context.Context, string, string, string) (DashboardDraft, error)
	SaveDashboardDraft(context.Context, string, string, string, int64, DashboardDefinition, string) (DashboardDraft, error)
	ValidateDashboard(context.Context, string, string, string, DashboardValidate) (DashboardValidation, error)
	PublishDashboard(context.Context, string, string, string, string, DashboardPublish) (DashboardVersion, error)
	ListDashboardVersions(context.Context, string, string, string, int64, int) ([]DashboardVersion, error)
	GetDashboardVersion(context.Context, string, string, string, int64) (DashboardVersion, error)
	EnqueueDashboardRun(context.Context, string, string, string, int64, string, DashboardExecute) (DashboardRun, error)
	GetDashboardRun(context.Context, string, string, string) (DashboardRun, error)
	CancelDashboardRun(context.Context, string, string, string) (DashboardRun, error)
	CopyDashboard(context.Context, string, string, string, string, DashboardCopy) (DashboardDraft, error)
	ArchiveDashboard(context.Context, string, string, string) (Dashboard, error)
	UnarchiveDashboard(context.Context, string, string, string) (Dashboard, error)
}
