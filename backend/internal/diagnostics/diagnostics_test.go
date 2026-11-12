package diagnostics

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/howtomakeaname/Evia/backend/internal/ingestion"
	"github.com/howtomakeaname/Evia/backend/internal/pipeline"
	"github.com/howtomakeaname/Evia/backend/internal/platform/admissionstats"
	"github.com/howtomakeaname/Evia/backend/internal/platform/budget"
)

const projectID = "11111111-1111-4111-8111-111111111111"

type sourceListerStub struct {
	rows []ingestion.Source
	err  error
}

func (s *sourceListerStub) ListSources(_ context.Context, _, _, _, _ string, _ int) ([]ingestion.Source, error) {
	return s.rows, s.err
}

type budgetStub struct {
	pingErr  error
	policies []budget.ScopePolicy
}

func (b *budgetStub) Ping(context.Context) error { return b.pingErr }
func (b *budgetStub) SnapshotPolicies(string) []budget.ScopePolicy {
	return b.policies
}

type readinessStub struct {
	receipts []pipeline.ProofReceipt
	err      error
}

func (r *readinessStub) LatestProofs(context.Context, string, int) ([]pipeline.ProofReceipt, error) {
	return r.receipts, r.err
}

type rejectionStub struct {
	read admissionstats.ProjectRead
	err  error
}

func (r *rejectionStub) Read(context.Context, string, int) (admissionstats.ProjectRead, error) {
	return r.read, r.err
}

func TestDisabledSectionsWhenPortsAreNil(t *testing.T) {
	service := NewService(Features{}, nil, nil, nil, nil)
	report, err := service.Build(context.Background(), "actor-1", projectID)
	if err != nil {
		t.Fatal(err)
	}
	if report.Sources.Status != StatusDisabled || report.Pipeline.Status != StatusDisabled {
		t.Fatalf("optional ports must render disabled: %+v", report)
	}
	if report.Budget != nil || report.Rejections != nil {
		t.Fatalf("nil ports must omit sections: %+v", report)
	}
}

func TestSourceInventoryAggregation(t *testing.T) {
	rows := []ingestion.Source{
		{ID: "1", Name: "web", Environment: "production", Kind: "browser", Status: "active"},
		{ID: "2", Name: "api", Environment: "production", Kind: "server", Status: "disabled"},
		{ID: "3", Name: "qa", Environment: "staging", Kind: "server", Status: "active"},
	}
	service := NewService(Features{CollectionEnabled: true}, &sourceListerStub{rows: rows}, nil, nil, nil)
	report, err := service.Build(context.Background(), "actor-1", projectID)
	if err != nil {
		t.Fatal(err)
	}
	section := report.Sources
	if section.Status != StatusOK || section.Total != 3 || section.Truncated {
		t.Fatalf("unexpected source section: %+v", section)
	}
	counts := map[string]int{}
	for _, item := range section.ByStatus {
		counts[item.Name] = item.Count
	}
	if counts["active"] != 2 || counts["disabled"] != 1 {
		t.Fatalf("unexpected status counts: %+v", section.ByStatus)
	}
	if len(section.DisabledSources) != 1 || section.DisabledSources[0].ID != "2" {
		t.Fatalf("disabled source list wrong: %+v", section.DisabledSources)
	}
}

func TestSourceInventoryErrorPropagates(t *testing.T) {
	service := NewService(Features{}, &sourceListerStub{err: errors.New("pg down")}, nil, nil, nil)
	if _, err := service.Build(context.Background(), "actor-1", projectID); err == nil {
		t.Fatal("source listing errors must fail the request")
	}
}

func TestBudgetSectionHealthAndDegradation(t *testing.T) {
	policies := []budget.ScopePolicy{
		{ProjectID: projectID, Environment: "development", Rate: 1000, Burst: 2000},
		{ProjectID: projectID, Environment: "staging", Rate: 1000, Burst: 2000},
		{ProjectID: projectID, Environment: "production", Rate: 5, Burst: 60, Override: true},
	}
	service := NewService(Features{CollectionEnabled: true}, nil, &budgetStub{policies: policies}, nil, nil)
	report, err := service.Build(context.Background(), "actor-1", projectID)
	if err != nil {
		t.Fatal(err)
	}
	if report.Budget == nil || report.Budget.Status != StatusOK || report.Budget.RatePerSecond != 1000 || report.Budget.Burst != 2000 {
		t.Fatalf("unexpected budget section: %+v", report.Budget)
	}
	if len(report.Budget.Environments) != 3 || !report.Budget.Environments[2].Override || report.Budget.Environments[2].RatePerSecond != 5 {
		t.Fatalf("environment policies not preserved: %+v", report.Budget.Environments)
	}

	degraded := NewService(Features{}, nil, &budgetStub{policies: policies, pingErr: errors.New("redis down")}, nil, nil)
	report, _ = degraded.Build(context.Background(), "actor-1", projectID)
	if report.Budget.Status != StatusUnavailable || report.Budget.ErrorCode != "DIAGNOSTICS_BUDGET_UNAVAILABLE" {
		t.Fatalf("ping failure must degrade the section: %+v", report.Budget)
	}
}

func TestPipelineSectionStates(t *testing.T) {
	now := time.Now().UTC()
	receipts := []pipeline.ProofReceipt{
		{Environment: "production", ReleaseID: "r1", Revision: 2, LastObserved: now.Add(-time.Minute), ProofCount: 3},
	}
	ok := NewService(Features{DeliveryEnabled: true}, nil, nil, &readinessStub{receipts: receipts}, nil)
	report, err := ok.Build(context.Background(), "actor-1", projectID)
	if err != nil {
		t.Fatal(err)
	}
	if report.Pipeline.Status != StatusOK || len(report.Pipeline.Proofs) != 1 || report.Pipeline.Proofs[0].AgeSeconds < 50 {
		t.Fatalf("unexpected pipeline section: %+v", report.Pipeline)
	}

	noData := NewService(Features{}, nil, nil, &readinessStub{}, nil)
	report, _ = noData.Build(context.Background(), "actor-1", projectID)
	if report.Pipeline.Status != StatusNoData {
		t.Fatalf("empty receipts must be no_data: %+v", report.Pipeline)
	}

	failed := NewService(Features{}, nil, nil, &readinessStub{err: errors.New("ch down")}, nil)
	report, _ = failed.Build(context.Background(), "actor-1", projectID)
	if report.Pipeline.Status != StatusUnavailable || report.Pipeline.ErrorCode != "DIAGNOSTICS_PIPELINE_UNAVAILABLE" {
		t.Fatalf("reader errors must degrade the section: %+v", report.Pipeline)
	}
}

func TestRejectionSectionMapsWindowsAndDegrades(t *testing.T) {
	read := admissionstats.ProjectRead{
		ProjectID: projectID,
		Environments: []admissionstats.EnvironmentWindows{
			{Environment: "production", Windows: []admissionstats.Window{
				{BucketAt: time.Date(2026, 11, 12, 8, 0, 0, 0, time.UTC), Reasons: []admissionstats.ReasonCount{{Reason: "INVALID_EVENT", Count: 2}}},
				{BucketAt: time.Date(2026, 11, 12, 9, 0, 0, 0, time.UTC), Reasons: []admissionstats.ReasonCount{{Reason: "EVENT_TOO_LARGE", Count: 1}}},
			}},
		},
		Totals: []admissionstats.ReasonCount{{Reason: "INVALID_EVENT", Count: 2}, {Reason: "EVENT_TOO_LARGE", Count: 1}},
	}
	service := NewService(Features{CollectionEnabled: true}, nil, nil, nil, &rejectionStub{read: read})
	report, err := service.Build(context.Background(), "actor-1", projectID)
	if err != nil {
		t.Fatal(err)
	}
	if report.Rejections == nil || report.Rejections.Status != StatusOK || report.Rejections.WindowHours != RejectionWindows {
		t.Fatalf("unexpected rejection section: %+v", report.Rejections)
	}
	if len(report.Rejections.Windows) != 2 || report.Rejections.Windows[0].BucketAt.After(report.Rejections.Windows[1].BucketAt) {
		t.Fatalf("windows must be ordered oldest first: %+v", report.Rejections.Windows)
	}

	failed := NewService(Features{}, nil, nil, nil, &rejectionStub{err: errors.New("redis down")})
	report, _ = failed.Build(context.Background(), "actor-1", projectID)
	if report.Rejections.Status != StatusUnavailable || report.Rejections.ErrorCode != "DIAGNOSTICS_REJECTIONS_UNAVAILABLE" {
		t.Fatalf("read errors must degrade the section: %+v", report.Rejections)
	}
}
