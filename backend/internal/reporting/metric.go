package reporting

import (
	"context"
	"time"

	"github.com/howtomakeaname/Evia/backend/internal/analysis"
)

const MaxMetricDefinitionBytes = 128 * 1024

type MetricDefinition struct {
	Title         string                  `json:"title"`
	Description   string                  `json:"description"`
	EventVersions []analysis.EventVersion `json:"event_versions"`
	Filter        *analysis.Filter        `json:"filter,omitempty"`
	Subject       string                  `json:"subject"`
	Aggregation   analysis.Metric         `json:"aggregation"`
	Unit          string                  `json:"unit"`
}
type MetricRecord struct {
	ID               string     `json:"metric_id"`
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
type MetricDraft struct {
	MetricRecord
	Definition MetricDefinition `json:"definition"`
}
type MetricVersion struct {
	MetricID       string             `json:"metric_id"`
	ProjectID      string             `json:"project_id"`
	Environment    string             `json:"environment"`
	Version        int64              `json:"version"`
	DraftRevision  int64              `json:"draft_revision"`
	Definition     MetricDefinition   `json:"definition"`
	Dependencies   DependencyManifest `json:"dependencies"`
	DefinitionHash string             `json:"definition_hash"`
	DependencyHash string             `json:"dependency_hash"`
	ValidationHash string             `json:"validation_hash"`
	CreatedAt      time.Time          `json:"created_at"`
	CreatedBy      string             `json:"created_by"`
}
type MetricChange struct {
	Category string `json:"category"`
	Path     string `json:"path"`
	Before   any    `json:"before,omitempty"`
	After    any    `json:"after,omitempty"`
}
type MetricValidation struct {
	Valid                bool               `json:"valid"`
	ExecutionPerformed   bool               `json:"execution_performed"`
	DraftRevision        int64              `json:"draft_revision"`
	PublishedVersion     int64              `json:"published_version"`
	NormalizedDefinition MetricDefinition   `json:"normalized_definition"`
	Dependencies         DependencyManifest `json:"dependencies"`
	DefinitionHash       string             `json:"definition_hash"`
	DependencyHash       string             `json:"dependency_hash"`
	Diff                 []MetricChange     `json:"diff"`
	ValidationHash       string             `json:"validation_hash"`
}
type MetricCreate struct {
	Environment string           `json:"environment"`
	Definition  MetricDefinition `json:"definition"`
}
type MetricUpdate struct {
	Definition MetricDefinition `json:"definition"`
}
type MetricValidate struct {
	DraftRevision            int64 `json:"draft_revision"`
	ExpectedPublishedVersion int64 `json:"expected_published_version"`
}
type MetricPublish struct {
	DraftRevision            int64  `json:"draft_revision"`
	ExpectedPublishedVersion int64  `json:"expected_published_version"`
	ValidationHash           string `json:"validation_hash"`
}
type MetricExecute struct {
	TimeRange analysis.TimeRange `json:"time_range"`
	Timezone  string             `json:"timezone"`
	Interval  string             `json:"interval,omitempty"`
	GroupBy   []analysis.Field   `json:"group_by,omitempty"`
	Filter    *analysis.Filter   `json:"filter,omitempty"`
	Limit     int                `json:"limit,omitempty"`
}
type MetricSnapshot struct {
	Draft        MetricDraft
	Baseline     *MetricVersion
	Dependencies DependencyManifest
}

// MetricStore rechecks current actor membership in every transaction.
// Validation snapshots never write; publication recomputes BuildMetricValidation
// under the same transaction that writes immutable content, audit and idempotency.
type MetricStore interface {
	CreateMetric(context.Context, string, string, string, MetricCreate) (MetricDraft, error)
	ListMetrics(context.Context, string, string, string, string, int, bool) ([]MetricRecord, error)
	GetMetric(context.Context, string, string, string) (MetricRecord, error)
	GetMetricDraft(context.Context, string, string, string) (MetricDraft, error)
	SaveMetricDraft(context.Context, string, string, string, int64, MetricDefinition) (MetricDraft, error)
	MetricValidationSnapshot(context.Context, string, string, string, MetricValidate) (MetricSnapshot, error)
	PublishMetric(context.Context, string, string, string, string, MetricPublish) (MetricVersion, error)
	ListMetricVersions(context.Context, string, string, string, int64, int) ([]MetricVersion, error)
	GetMetricVersion(context.Context, string, string, string, int64) (MetricVersion, error)
	EnqueueMetricVersion(context.Context, string, string, string, int64, string, MetricExecute) (analysis.Job, error)
	ArchiveMetric(context.Context, string, string, string) (MetricRecord, error)
	UnarchiveMetric(context.Context, string, string, string) (MetricRecord, error)
}
type MetricService interface {
	CreateMetric(context.Context, string, string, string, MetricCreate) (MetricDraft, error)
	ListMetrics(context.Context, string, string, string, string, int, bool) ([]MetricRecord, error)
	GetMetric(context.Context, string, string, string) (MetricRecord, error)
	GetMetricDraft(context.Context, string, string, string) (MetricDraft, error)
	SaveMetricDraft(context.Context, string, string, string, int64, MetricDefinition) (MetricDraft, error)
	ValidateMetric(context.Context, string, string, string, MetricValidate) (MetricValidation, error)
	PublishMetric(context.Context, string, string, string, string, MetricPublish) (MetricVersion, error)
	ListMetricVersions(context.Context, string, string, string, int64, int) ([]MetricVersion, error)
	GetMetricVersion(context.Context, string, string, string, int64) (MetricVersion, error)
	EnqueueMetricVersion(context.Context, string, string, string, int64, string, MetricExecute) (analysis.Job, error)
	ArchiveMetric(context.Context, string, string, string) (MetricRecord, error)
	UnarchiveMetric(context.Context, string, string, string) (MetricRecord, error)
}
