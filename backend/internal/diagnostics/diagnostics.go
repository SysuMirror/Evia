// Package diagnostics composes authorized collection health signals from
// bounded ports. Every section degrades independently when its dependency is
// disabled or unavailable; no section ever invents measurements.
package diagnostics

import (
	"context"
	"sort"
	"time"

	"github.com/howtomakeaname/Evia/backend/internal/ingestion"
	"github.com/howtomakeaname/Evia/backend/internal/pipeline"
	"github.com/howtomakeaname/Evia/backend/internal/platform/admissionstats"
	"github.com/howtomakeaname/Evia/backend/internal/platform/budget"
)

const (
	StatusOK          = "ok"
	StatusNoData      = "no_data"
	StatusUnavailable = "unavailable"
	StatusDisabled    = "disabled"

	inventoryLimit    = 200
	disabledListLimit = 20
	// RejectionWindows is the fixed 24-hour lookback of hourly counters.
	RejectionWindows = 24
	// ProofLimit bounds the pipeline section per request.
	ProofLimit = pipeline.MaxReadinessProofs
)

// Features reports the serve-process switches that decide which ports exist.
type Features struct {
	CollectionEnabled bool `json:"collection_enabled"`
	AnalysisEnabled   bool `json:"analysis_enabled"`
	DeliveryEnabled   bool `json:"delivery_enabled"`
}

// NameCount is one deterministic aggregation bucket.
type NameCount struct {
	Name  string `json:"name"`
	Count int    `json:"count"`
}

// DisabledSource identifies a source that currently rejects all writes.
type DisabledSource struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Environment string `json:"environment"`
	Kind        string `json:"kind"`
}

// SourceSection summarizes the durable source inventory from PostgreSQL.
type SourceSection struct {
	Status          string           `json:"status"`
	Total           int              `json:"total"`
	Truncated       bool             `json:"truncated"`
	ByStatus        []NameCount      `json:"by_status"`
	ByEnvironment   []NameCount      `json:"by_environment"`
	DisabledSources []DisabledSource `json:"disabled_sources"`
}

// EnvPolicy is the effective rate policy for one environment.
type EnvPolicy struct {
	Environment   string  `json:"environment"`
	RatePerSecond float64 `json:"rate_per_second"`
	Burst         int     `json:"burst"`
	Override      bool    `json:"override"`
}

// BudgetSection reports Redis health plus the configured admission policy.
type BudgetSection struct {
	Status        string      `json:"status"`
	ErrorCode     string      `json:"error_code,omitempty"`
	RatePerSecond float64     `json:"rate_per_second"`
	Burst         int         `json:"burst"`
	Environments  []EnvPolicy `json:"environments"`
}

// Proof is one grouped pipeline readiness observation.
type Proof struct {
	Environment string    `json:"environment"`
	ReleaseID   string    `json:"release_id"`
	Revision    int64     `json:"revision"`
	ObservedAt  time.Time `json:"observed_at"`
	AgeSeconds  int64     `json:"age_seconds"`
	ProofCount  int64     `json:"proof_count"`
}

// PipelineSection reports the latest durable probe evidence in ClickHouse.
type PipelineSection struct {
	Status    string  `json:"status"`
	ErrorCode string  `json:"error_code,omitempty"`
	Proofs    []Proof `json:"proofs"`
}

// RejectionWindow holds one hourly bucket for one environment.
type RejectionWindow struct {
	BucketAt    time.Time                    `json:"bucket_at"`
	Environment string                       `json:"environment"`
	Reasons     []admissionstats.ReasonCount `json:"reasons"`
}

// RejectionSection exposes durable Redis rejection counters.
type RejectionSection struct {
	Status      string                       `json:"status"`
	ErrorCode   string                       `json:"error_code,omitempty"`
	WindowHours int                          `json:"window_hours"`
	Totals      []admissionstats.ReasonCount `json:"totals"`
	Windows     []RejectionWindow            `json:"windows"`
}

// Report is the full authorized diagnostic view.
type Report struct {
	ProjectID   string            `json:"project_id"`
	GeneratedAt time.Time         `json:"generated_at"`
	Features    Features          `json:"features"`
	Sources     SourceSection     `json:"sources"`
	Budget      *BudgetSection    `json:"budget,omitempty"`
	Pipeline    PipelineSection   `json:"pipeline"`
	Rejections  *RejectionSection `json:"rejections,omitempty"`
}

// SourceLister is satisfied by the ingestion source service.
type SourceLister interface {
	ListSources(ctx context.Context, actor, project, env, after string, limit int) ([]ingestion.Source, error)
}

// BudgetInspector is satisfied by *budget.Limiter.
type BudgetInspector interface {
	Ping(ctx context.Context) error
	SnapshotPolicies(projectID string) []budget.ScopePolicy
}

// ReadinessPort is satisfied by *pipeline.ReadinessReader.
type ReadinessPort interface {
	LatestProofs(ctx context.Context, projectID string, limit int) ([]pipeline.ProofReceipt, error)
}

// RejectionPort is satisfied by *admissionstats.Recorder.
type RejectionPort interface {
	Read(ctx context.Context, projectID string, windows int) (admissionstats.ProjectRead, error)
}

// Service assembles the four bounded ports. Nil ports render as disabled.
type Service struct {
	features   Features
	sources    SourceLister
	budget     BudgetInspector
	readiness  ReadinessPort
	rejections RejectionPort
	now        func() time.Time
}

// NewService wires the diagnostics service. Every port except features is
// optional and must be nil when the corresponding feature is switched off.
func NewService(features Features, sources SourceLister, budgetInspector BudgetInspector, readiness ReadinessPort, rejections RejectionPort) *Service {
	return &Service{
		features:   features,
		sources:    sources,
		budget:     budgetInspector,
		readiness:  readiness,
		rejections: rejections,
		now:        func() time.Time { return time.Now().UTC() },
	}
}

func sortedCounts(counts map[string]int) []NameCount {
	out := make([]NameCount, 0, len(counts))
	for name, count := range counts {
		out = append(out, NameCount{Name: name, Count: count})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Build collects every section independently. A PostgreSQL source listing
// failure still fails the request; optional dependencies degrade in place.
func (s *Service) Build(ctx context.Context, actor, projectID string) (Report, error) {
	report := Report{ProjectID: projectID, GeneratedAt: s.now().UTC(), Features: s.features}
	sources, err := s.buildSources(ctx, actor, projectID)
	if err != nil {
		return Report{}, err
	}
	report.Sources = sources
	report.Budget = s.buildBudget(ctx, projectID)
	report.Pipeline = s.buildPipeline(ctx, projectID)
	report.Rejections = s.buildRejections(ctx, projectID)
	return report, nil
}

func (s *Service) buildSources(ctx context.Context, actor, projectID string) (SourceSection, error) {
	section := SourceSection{Status: StatusOK, ByStatus: []NameCount{}, ByEnvironment: []NameCount{}, DisabledSources: []DisabledSource{}}
	if s.sources == nil {
		section.Status = StatusDisabled
		return section, nil
	}
	rows, err := s.sources.ListSources(ctx, actor, projectID, "", "", inventoryLimit)
	if err != nil {
		return SourceSection{}, err
	}
	statusCounts := map[string]int{}
	environmentCounts := map[string]int{}
	for _, source := range rows {
		section.Total++
		statusCounts[source.Status]++
		environmentCounts[source.Environment]++
		if source.Status == "disabled" && len(section.DisabledSources) < disabledListLimit {
			section.DisabledSources = append(section.DisabledSources, DisabledSource{
				ID:          source.ID,
				Name:        source.Name,
				Environment: source.Environment,
				Kind:        source.Kind,
			})
		}
	}
	section.Truncated = len(rows) == inventoryLimit
	section.ByStatus = sortedCounts(statusCounts)
	section.ByEnvironment = sortedCounts(environmentCounts)
	return section, nil
}

func (s *Service) buildBudget(ctx context.Context, projectID string) *BudgetSection {
	if s.budget == nil {
		return nil
	}
	section := &BudgetSection{Status: StatusOK, Environments: []EnvPolicy{}}
	policies := s.budget.SnapshotPolicies(projectID)
	if len(policies) == 0 {
		section.Status = StatusUnavailable
		section.ErrorCode = "DIAGNOSTICS_BUDGET_POLICY_UNAVAILABLE"
		return section
	}
	section.RatePerSecond, section.Burst = policies[0].Rate, policies[0].Burst
	for _, policy := range policies {
		section.Environments = append(section.Environments, EnvPolicy{
			Environment:   policy.Environment,
			RatePerSecond: policy.Rate,
			Burst:         policy.Burst,
			Override:      policy.Override,
		})
	}
	if err := s.budget.Ping(ctx); err != nil {
		section.Status = StatusUnavailable
		section.ErrorCode = "DIAGNOSTICS_BUDGET_UNAVAILABLE"
	}
	return section
}

func (s *Service) buildPipeline(ctx context.Context, projectID string) PipelineSection {
	section := PipelineSection{Status: StatusDisabled, Proofs: []Proof{}}
	if s.readiness == nil {
		return section
	}
	receipts, err := s.readiness.LatestProofs(ctx, projectID, ProofLimit)
	if err != nil {
		return PipelineSection{Status: StatusUnavailable, ErrorCode: "DIAGNOSTICS_PIPELINE_UNAVAILABLE", Proofs: []Proof{}}
	}
	if len(receipts) == 0 {
		section.Status = StatusNoData
		return section
	}
	now := s.now()
	section.Status = StatusOK
	for _, receipt := range receipts {
		section.Proofs = append(section.Proofs, Proof{
			Environment: receipt.Environment,
			ReleaseID:   receipt.ReleaseID,
			Revision:    receipt.Revision,
			ObservedAt:  receipt.LastObserved,
			AgeSeconds:  int64(now.Sub(receipt.LastObserved).Seconds()),
			ProofCount:  receipt.ProofCount,
		})
	}
	return section
}

func (s *Service) buildRejections(ctx context.Context, projectID string) *RejectionSection {
	if s.rejections == nil {
		return nil
	}
	section := &RejectionSection{Status: StatusOK, WindowHours: RejectionWindows, Totals: []admissionstats.ReasonCount{}, Windows: []RejectionWindow{}}
	read, err := s.rejections.Read(ctx, projectID, RejectionWindows)
	if err != nil {
		section.Status = StatusUnavailable
		section.ErrorCode = "DIAGNOSTICS_REJECTIONS_UNAVAILABLE"
		return section
	}
	section.Totals = read.Totals
	for _, environment := range read.Environments {
		for _, window := range environment.Windows {
			section.Windows = append(section.Windows, RejectionWindow{
				BucketAt:    window.BucketAt,
				Environment: environment.Environment,
				Reasons:     window.Reasons,
			})
		}
	}
	sort.Slice(section.Windows, func(i, j int) bool {
		if section.Windows[i].BucketAt.Equal(section.Windows[j].BucketAt) {
			return section.Windows[i].Environment < section.Windows[j].Environment
		}
		return section.Windows[i].BucketAt.Before(section.Windows[j].BucketAt)
	})
	return section
}
