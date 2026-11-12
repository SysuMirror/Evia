package control

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/howtomakeaname/Evia/backend/internal/diagnostics"
	"github.com/howtomakeaname/Evia/backend/internal/gen/wire"
	"github.com/howtomakeaname/Evia/backend/internal/platform/admissionstats"
	"github.com/howtomakeaname/Evia/backend/internal/platform/contract"
	"github.com/howtomakeaname/Evia/backend/internal/platform/cursor"
	"github.com/howtomakeaname/Evia/backend/internal/platform/fault"
	"github.com/howtomakeaname/Evia/backend/internal/platform/httpapi"
)

type fakeDiagnosticsService struct {
	report diagnostics.Report
	err    error
	got   struct{ actor, project string }
}

func (f *fakeDiagnosticsService) Build(_ context.Context, actor, project string) (diagnostics.Report, error) {
	f.got.actor, f.got.project = actor, project
	return f.report, f.err
}

func diagnosticsAPIWithService(t *testing.T, svc DiagnosticsService) http.Handler {
	t.Helper()
	validator, err := contract.New()
	if err != nil {
		t.Fatal(err)
	}
	codec, err := cursor.New([]byte(strings.Repeat("a", 32)))
	if err != nil {
		t.Fatal(err)
	}
	a := &API{
		Boundary:    &httpapi.Boundary{Validator: validator, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))},
		Access:      &fakeAccess{},
		Machines:    &fakeMachines{},
		Cursor:      codec,
		Origin:      "https://evia.example",
		Diagnostics: svc,
	}
	return a.Handler()
}

func diagnosticsRequest(handler http.Handler, path string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodGet, path, nil)
	r.AddCookie(&http.Cookie{Name: "evia_session", Value: "valid"})
	r.Header.Set("X-CSRF-Token", "csrf")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	return w
}

func sampleDiagnosticsReport() diagnostics.Report {
	now := time.Date(2026, 11, 12, 9, 0, 0, 0, time.UTC)
	return diagnostics.Report{
		ProjectID:   projectID,
		GeneratedAt: now,
		Features:    diagnostics.Features{CollectionEnabled: true, AnalysisEnabled: true, DeliveryEnabled: true},
		Sources: diagnostics.SourceSection{
			Status:        diagnostics.StatusOK,
			Total:         2,
			ByStatus:      []diagnostics.NameCount{{Name: "active", Count: 1}, {Name: "disabled", Count: 1}},
			ByEnvironment: []diagnostics.NameCount{{Name: "production", Count: 2}},
			DisabledSources: []diagnostics.DisabledSource{
				{ID: "44444444-4444-4444-8444-444444444444", Name: "legacy-web", Environment: "production", Kind: "browser"},
			},
		},
		Budget: &diagnostics.BudgetSection{
			Status:        diagnostics.StatusOK,
			RatePerSecond: 1000,
			Burst:         2000,
			Environments: []diagnostics.EnvPolicy{
				{Environment: "production", RatePerSecond: 5, Burst: 60, Override: true},
			},
		},
		Pipeline: diagnostics.PipelineSection{
			Status: diagnostics.StatusOK,
			Proofs: []diagnostics.Proof{
				{Environment: "production", ReleaseID: "55555555-5555-4555-8555-555555555555", Revision: 3, ObservedAt: now.Add(-time.Minute), AgeSeconds: 60, ProofCount: 2},
			},
		},
		Rejections: &diagnostics.RejectionSection{
			Status:      diagnostics.StatusOK,
			WindowHours: diagnostics.RejectionWindows,
			Totals:      []admissionstats.ReasonCount{{Reason: "INVALID_EVENT", Count: 7}},
			Windows: []diagnostics.RejectionWindow{
				{BucketAt: now.Add(-time.Hour), Environment: "production", Reasons: []admissionstats.ReasonCount{{Reason: "INVALID_EVENT", Count: 7}}},
			},
		},
	}
}

func TestDiagnosticsMapsFullReport(t *testing.T) {
	svc := &fakeDiagnosticsService{report: sampleDiagnosticsReport()}
	handler := diagnosticsAPIWithService(t, svc)

	w := diagnosticsRequest(handler, "/api/v1/projects/"+projectID+"/diagnostics")
	governanceStatus(t, w, http.StatusOK)
	if svc.got.project != projectID || svc.got.actor == "" {
		t.Fatalf("service scope wrong: %+v", svc.got)
	}
	var out wire.Diagnostics
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.ProjectID != projectID || !out.Features.CollectionEnabled || out.Sources.Total != 2 || len(out.Sources.DisabledSources) != 1 {
		t.Fatalf("source mapping wrong: %+v", out)
	}
	if out.Budget.Status != "ok" || out.Budget.RatePerSecond != 1000 || out.Budget.Environments[0].Override != true {
		t.Fatalf("budget mapping wrong: %+v", out.Budget)
	}
	if out.Pipeline.Status != "ok" || len(out.Pipeline.Proofs) != 1 || out.Pipeline.Proofs[0].AgeSeconds != 60 {
		t.Fatalf("pipeline mapping wrong: %+v", out.Pipeline)
	}
	if out.Rejections.Status != "ok" || out.Rejections.WindowHours != 24 || out.Rejections.Totals[0].Count != 7 {
		t.Fatalf("rejection mapping wrong: %+v", out.Rejections)
	}
}

func TestDiagnosticsRendersDisabledSections(t *testing.T) {
	report := sampleDiagnosticsReport()
	report.Budget = nil
	report.Rejections = nil
	report.Pipeline.Status = diagnostics.StatusDisabled
	report.Pipeline.Proofs = nil
	svc := &fakeDiagnosticsService{report: report}
	handler := diagnosticsAPIWithService(t, svc)

	w := diagnosticsRequest(handler, "/api/v1/projects/"+projectID+"/diagnostics")
	governanceStatus(t, w, http.StatusOK)
	var out wire.Diagnostics
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.Budget.Status != "disabled" || out.Rejections.Status != "disabled" || out.Pipeline.Status != "disabled" {
		t.Fatalf("nil ports must render disabled sections: %+v %+v %+v", out.Budget, out.Rejections, out.Pipeline)
	}
}

func TestDiagnosticsRejectsBadInputAndUnauthenticated(t *testing.T) {
	handler := diagnosticsAPIWithService(t, &fakeDiagnosticsService{report: sampleDiagnosticsReport()})
	for _, path := range []string{
		"/api/v1/projects/not-a-uuid/diagnostics",
		"/api/v1/projects/" + projectID + "/diagnostics?unknown=1",
	} {
		if got := diagnosticsRequest(handler, path); got.Code != http.StatusBadRequest {
			t.Fatalf("path %s expected 400 got %d", path, got.Code)
		}
	}
	r := httptest.NewRequest(http.MethodGet, "/api/v1/projects/"+projectID+"/diagnostics", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 without session, got %d", w.Code)
	}
}

func TestDiagnosticsPropagatesServiceUnavailable(t *testing.T) {
	svc := &fakeDiagnosticsService{err: fault.New(fault.Unavailable, "DIAGNOSTICS_SOURCES_UNAVAILABLE", "来源存储不可用")}
	handler := diagnosticsAPIWithService(t, svc)
	w := diagnosticsRequest(handler, "/api/v1/projects/"+projectID+"/diagnostics")
	governanceStatus(t, w, http.StatusServiceUnavailable)
}

func TestDiagnosticsRouteAbsentWhenNotWired(t *testing.T) {
	validator, err := contract.New()
	if err != nil {
		t.Fatal(err)
	}
	codec, err := cursor.New([]byte(strings.Repeat("a", 32)))
	if err != nil {
		t.Fatal(err)
	}
	a := &API{
		Boundary: &httpapi.Boundary{Validator: validator, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))},
		Access:   &fakeAccess{}, Machines: &fakeMachines{}, Cursor: codec, Origin: "https://evia.example",
	}
	w := diagnosticsRequest(a.Handler(), "/api/v1/projects/"+projectID+"/diagnostics")
	if w.Code != http.StatusNotFound {
		t.Fatalf("unwired service must not mount the route, got %d", w.Code)
	}
}
