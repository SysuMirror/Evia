package control

import (
	"context"
	"net/http"
	"time"

	"github.com/howtomakeaname/Evia/backend/internal/diagnostics"
	"github.com/howtomakeaname/Evia/backend/internal/gen/wire"
)

// DiagnosticsService is the bounded read model the HTTP boundary requires.
type DiagnosticsService interface {
	Build(ctx context.Context, actor, projectID string) (diagnostics.Report, error)
}

// DiagnosticsAPI exposes the authorized collection diagnostics surface.
type DiagnosticsAPI struct {
	API     *API
	Service DiagnosticsService
}

// Register mounts nothing when the service is absent, matching other APIs.
func (a *DiagnosticsAPI) Register(mux *http.ServeMux) {
	if a.Service == nil {
		return
	}
	mux.HandleFunc("GET /api/v1/projects/{project_id}/diagnostics", a.API.projectAuth(false, a.get))
}

func diagnosticsTime(value time.Time) string {
	return value.UTC().Format(time.RFC3339Nano)
}

func nameCountsWire(items []diagnostics.NameCount) []wire.DiagnosticsNameCount {
	out := make([]wire.DiagnosticsNameCount, 0, len(items))
	for _, item := range items {
		out = append(out, wire.DiagnosticsNameCount{Name: item.Name, Count: int64(item.Count)})
	}
	return out
}

func (a *DiagnosticsAPI) get(w http.ResponseWriter, r *http.Request, actor authorizedActor) error {
	project, err := governanceProject(r)
	if err != nil {
		return err
	}
	if _, err = governanceQuery(r); err != nil {
		return err
	}
	report, err := a.Service.Build(r.Context(), actor.ID, project)
	if err != nil {
		return err
	}
	return a.API.Boundary.JSON(w, http.StatusOK, "Diagnostics", diagnosticsWire(report))
}

func diagnosticsWire(report diagnostics.Report) wire.Diagnostics {
	out := wire.Diagnostics{
		ProjectID:   report.ProjectID,
		GeneratedAt: diagnosticsTime(report.GeneratedAt),
		Features: wire.DiagnosticsFeatures{
			CollectionEnabled: report.Features.CollectionEnabled,
			AnalysisEnabled:   report.Features.AnalysisEnabled,
			DeliveryEnabled:   report.Features.DeliveryEnabled,
		},
		Sources: wire.DiagnosticsSourceSummary{
			Status:          report.Sources.Status,
			Total:           int64(report.Sources.Total),
			Truncated:       report.Sources.Truncated,
			ByStatus:        nameCountsWire(report.Sources.ByStatus),
			ByEnvironment:   nameCountsWire(report.Sources.ByEnvironment),
			DisabledSources: []wire.DiagnosticsDisabledSource{},
		},
		Budget:     disabledBudget(),
		Pipeline:   wire.DiagnosticsPipeline{Status: report.Pipeline.Status, Proofs: []wire.DiagnosticsProof{}},
		Rejections: disabledRejections(),
	}
	for _, source := range report.Sources.DisabledSources {
		out.Sources.DisabledSources = append(out.Sources.DisabledSources, wire.DiagnosticsDisabledSource{
			ID:          source.ID,
			Name:        source.Name,
			Environment: source.Environment,
			Kind:        source.Kind,
		})
	}
	if report.Budget != nil {
		out.Budget = wire.DiagnosticsBudget{
			Status:        report.Budget.Status,
			RatePerSecond: report.Budget.RatePerSecond,
			Burst:         int64(report.Budget.Burst),
			Environments:  []wire.DiagnosticsEnvPolicy{},
		}
		if report.Budget.ErrorCode != "" {
			code := report.Budget.ErrorCode
			out.Budget.ErrorCode = &code
		}
		for _, policy := range report.Budget.Environments {
			out.Budget.Environments = append(out.Budget.Environments, wire.DiagnosticsEnvPolicy{
				Environment:   policy.Environment,
				RatePerSecond: policy.RatePerSecond,
				Burst:         int64(policy.Burst),
				Override:      policy.Override,
			})
		}
	}
	if report.Pipeline.ErrorCode != "" {
		code := report.Pipeline.ErrorCode
		out.Pipeline.ErrorCode = &code
	}
	for _, proof := range report.Pipeline.Proofs {
		out.Pipeline.Proofs = append(out.Pipeline.Proofs, wire.DiagnosticsProof{
			Environment: proof.Environment,
			ReleaseID:   proof.ReleaseID,
			Revision:    proof.Revision,
			ObservedAt:  diagnosticsTime(proof.ObservedAt),
			AgeSeconds:  proof.AgeSeconds,
			ProofCount:  proof.ProofCount,
		})
	}
	if report.Rejections != nil {
		out.Rejections = wire.DiagnosticsRejections{
			Status:      report.Rejections.Status,
			WindowHours: int64(report.Rejections.WindowHours),
			Totals:      []wire.DiagnosticsReasonCount{},
			Windows:     []wire.DiagnosticsRejectionWindow{},
		}
		if report.Rejections.ErrorCode != "" {
			code := report.Rejections.ErrorCode
			out.Rejections.ErrorCode = &code
		}
		for _, item := range report.Rejections.Totals {
			out.Rejections.Totals = append(out.Rejections.Totals, wire.DiagnosticsReasonCount{Reason: item.Reason, Count: item.Count})
		}
		for _, window := range report.Rejections.Windows {
			entry := wire.DiagnosticsRejectionWindow{
				BucketAt:    diagnosticsTime(window.BucketAt),
				Environment: window.Environment,
				Reasons:     []wire.DiagnosticsReasonCount{},
			}
			for _, item := range window.Reasons {
				entry.Reasons = append(entry.Reasons, wire.DiagnosticsReasonCount{Reason: item.Reason, Count: item.Count})
			}
			out.Rejections.Windows = append(out.Rejections.Windows, entry)
		}
	}
	return out
}

func disabledBudget() wire.DiagnosticsBudget {
	return wire.DiagnosticsBudget{Status: diagnostics.StatusDisabled, Environments: []wire.DiagnosticsEnvPolicy{}}
}

func disabledRejections() wire.DiagnosticsRejections {
	return wire.DiagnosticsRejections{
		Status:      diagnostics.StatusDisabled,
		WindowHours: diagnostics.RejectionWindows,
		Totals:      []wire.DiagnosticsReasonCount{},
		Windows:     []wire.DiagnosticsRejectionWindow{},
	}
}
