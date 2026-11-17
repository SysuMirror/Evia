package reporting

import (
	"context"
	"encoding/json"
	"time"

	"github.com/howtomakeaname/Evia/backend/internal/analysis"
)

const MaxSavedAnalysisDefinitionBytes = 128 * 1024

type SavedAnalysisVisualization struct {
	Kind string `json:"kind"`
}

// MetricReference pins an immutable metric version as the data source of a
// saved analysis. The exact version is fixed at publication and never resolves
// implicitly to the latest metric. It is mutually exclusive with an inline
// query on input; the storage adapter derives the executable query from the
// referenced metric version inside the same authorized transaction.
type MetricReference struct {
	MetricID string `json:"metric_id"`
	Version  int64  `json:"version"`
}

type SavedAnalysisDefinition struct {
	Title         string                     `json:"title"`
	Description   string                     `json:"description"`
	Query         analysis.Template          `json:"query"`
	MetricRef     *MetricReference           `json:"metric_ref,omitempty"`
	Visualization SavedAnalysisVisualization `json:"visualization"`
}

// MarshalJSON keeps the stored/read representation byte-identical while
// omitting the not-yet-derived query for a metric-reference write input. The
// domain Query is a value type, so without this an omitted write query would
// rematerialize as an empty object during service re-validation and fail the
// one-source contract. Stored definitions always carry a non-empty derived or
// inline query and are therefore unaffected.
func (d SavedAnalysisDefinition) MarshalJSON() ([]byte, error) {
	type shape struct {
		Title         string                     `json:"title"`
		Description   string                     `json:"description"`
		Query         *analysis.Template         `json:"query,omitempty"`
		MetricRef     *MetricReference           `json:"metric_ref,omitempty"`
		Visualization SavedAnalysisVisualization `json:"visualization"`
	}
	out := shape{
		Title:         d.Title,
		Description:   d.Description,
		MetricRef:     d.MetricRef,
		Visualization: d.Visualization,
	}
	if d.Query.Kind != "" {
		out.Query = &d.Query
	}
	return json.Marshal(out)
}

type SavedAnalysis struct {
	ID               string     `json:"analysis_id"`
	ProjectID        string     `json:"project_id"`
	Environment      string     `json:"environment"`
	Title            string     `json:"title"`
	Kind             string     `json:"kind"`
	DraftRevision    int64      `json:"draft_revision"`
	PublishedVersion int64      `json:"published_version"`
	ArchivedAt       *time.Time `json:"archived_at,omitempty"`
	ArchivedBy       string     `json:"archived_by,omitempty"`
	CreatedAt        time.Time  `json:"created_at"`
	UpdatedAt        time.Time  `json:"updated_at"`
}
type SavedAnalysisDraft struct {
	SavedAnalysis
	Definition SavedAnalysisDefinition `json:"definition"`
}
type SavedAnalysisVersion struct {
	AnalysisID     string                  `json:"analysis_id"`
	ProjectID      string                  `json:"project_id"`
	Environment    string                  `json:"environment"`
	Version        int64                   `json:"version"`
	DraftRevision  int64                   `json:"draft_revision"`
	Definition     SavedAnalysisDefinition `json:"definition"`
	Dependencies   DependencyManifest      `json:"dependencies"`
	DefinitionHash string                  `json:"definition_hash"`
	DependencyHash string                  `json:"dependency_hash"`
	ValidationHash string                  `json:"validation_hash"`
	CreatedAt      time.Time               `json:"created_at"`
	CreatedBy      string                  `json:"created_by"`
}
type SavedAnalysisChange struct {
	Category string `json:"category"`
	Path     string `json:"path"`
	Before   any    `json:"before,omitempty"`
	After    any    `json:"after,omitempty"`
}
type SavedAnalysisValidation struct {
	Valid                bool                    `json:"valid"`
	ExecutionPerformed   bool                    `json:"execution_performed"`
	DraftRevision        int64                   `json:"draft_revision"`
	PublishedVersion     int64                   `json:"published_version"`
	NormalizedDefinition SavedAnalysisDefinition `json:"normalized_definition"`
	Dependencies         DependencyManifest      `json:"dependencies"`
	DefinitionHash       string                  `json:"definition_hash"`
	DependencyHash       string                  `json:"dependency_hash"`
	Diff                 []SavedAnalysisChange   `json:"diff"`
	ValidationHash       string                  `json:"validation_hash"`
}
type SavedAnalysisCreate struct {
	Environment string                  `json:"environment"`
	Definition  SavedAnalysisDefinition `json:"definition"`
}
type SavedAnalysisUpdate struct {
	Definition SavedAnalysisDefinition `json:"definition"`
}
type SavedAnalysisValidate struct {
	DraftRevision            int64 `json:"draft_revision"`
	ExpectedPublishedVersion int64 `json:"expected_published_version"`
}
type SavedAnalysisPublish struct {
	DraftRevision            int64  `json:"draft_revision"`
	ExpectedPublishedVersion int64  `json:"expected_published_version"`
	ValidationHash           string `json:"validation_hash"`
}
type SavedAnalysisExecute struct {
	TimeRange analysis.TimeRange `json:"time_range"`
	Timezone  string             `json:"timezone"`
}
type SavedAnalysisSnapshot struct {
	Draft        SavedAnalysisDraft
	Baseline     *SavedAnalysisVersion
	Dependencies DependencyManifest
}

// SavedAnalysisStore rechecks current actor membership in every transaction.
// Validation snapshots never write; publication recomputes BuildSavedAnalysisValidation
// under the same transaction that writes immutable content, audit and idempotency.
type SavedAnalysisStore interface {
	CreateSavedAnalysis(context.Context, string, string, string, SavedAnalysisCreate) (SavedAnalysisDraft, error)
	// search is a normalized, LIKE-escaped literal title substring or "" for
	// no filter.
	ListSavedAnalyses(context.Context, string, string, string, string, int, bool, string) ([]SavedAnalysis, error)
	GetSavedAnalysis(context.Context, string, string, string) (SavedAnalysis, error)
	GetSavedAnalysisDraft(context.Context, string, string, string) (SavedAnalysisDraft, error)
	SaveSavedAnalysisDraft(context.Context, string, string, string, int64, SavedAnalysisDefinition) (SavedAnalysisDraft, error)
	SavedAnalysisValidationSnapshot(context.Context, string, string, string, SavedAnalysisValidate) (SavedAnalysisSnapshot, error)
	PublishSavedAnalysis(context.Context, string, string, string, string, SavedAnalysisPublish) (SavedAnalysisVersion, error)
	ListSavedAnalysisVersions(context.Context, string, string, string, int64, int) ([]SavedAnalysisVersion, error)
	GetSavedAnalysisVersion(context.Context, string, string, string, int64) (SavedAnalysisVersion, error)
	EnqueueSavedAnalysisVersion(context.Context, string, string, string, int64, string, SavedAnalysisExecute) (analysis.Job, error)
	ArchiveSavedAnalysis(context.Context, string, string, string) (SavedAnalysis, error)
	UnarchiveSavedAnalysis(context.Context, string, string, string) (SavedAnalysis, error)
}
type SavedAnalysisService interface {
	CreateSavedAnalysis(context.Context, string, string, string, SavedAnalysisCreate) (SavedAnalysisDraft, error)
	ListSavedAnalyses(context.Context, string, string, string, string, int, bool, string) ([]SavedAnalysis, error)
	GetSavedAnalysis(context.Context, string, string, string) (SavedAnalysis, error)
	GetSavedAnalysisDraft(context.Context, string, string, string) (SavedAnalysisDraft, error)
	SaveSavedAnalysisDraft(context.Context, string, string, string, int64, SavedAnalysisDefinition) (SavedAnalysisDraft, error)
	ValidateSavedAnalysis(context.Context, string, string, string, SavedAnalysisValidate) (SavedAnalysisValidation, error)
	PublishSavedAnalysis(context.Context, string, string, string, string, SavedAnalysisPublish) (SavedAnalysisVersion, error)
	ListSavedAnalysisVersions(context.Context, string, string, string, int64, int) ([]SavedAnalysisVersion, error)
	GetSavedAnalysisVersion(context.Context, string, string, string, int64) (SavedAnalysisVersion, error)
	EnqueueSavedAnalysisVersion(context.Context, string, string, string, int64, string, SavedAnalysisExecute) (analysis.Job, error)
	ArchiveSavedAnalysis(context.Context, string, string, string) (SavedAnalysis, error)
	UnarchiveSavedAnalysis(context.Context, string, string, string) (SavedAnalysis, error)
}
