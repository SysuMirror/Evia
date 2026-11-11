package control

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/howtomakeaname/Evia/backend/internal/gen/wire"
	"github.com/howtomakeaname/Evia/backend/internal/platform/contract"
	"github.com/howtomakeaname/Evia/backend/internal/platform/cursor"
	"github.com/howtomakeaname/Evia/backend/internal/platform/httpapi"
	"github.com/howtomakeaname/Evia/backend/internal/retention"
)

type fakeSweepService struct {
	views     []retention.SweepView
	gotFilter retention.SweepFilter
	gotLimit  int
	err       error
}

func (f *fakeSweepService) ListSweeps(_ context.Context, _, _ string, filter retention.SweepFilter, limit int) ([]retention.SweepView, error) {
	f.gotFilter = filter
	f.gotLimit = limit
	return f.views, f.err
}

func retentionAPIWithSweeps(t *testing.T, svc RetentionService, sweeps RetentionSweepService) http.Handler {
	t.Helper()
	validator, err := contract.New()
	if err != nil {
		t.Fatal(err)
	}
	codec, err := cursor.New([]byte(strings.Repeat("r", 32)))
	if err != nil {
		t.Fatal(err)
	}
	a := &API{
		Boundary: &httpapi.Boundary{Validator: validator, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))},
		Access:   &fakeAccess{}, Machines: &fakeMachines{}, Cursor: codec, Origin: "https://evia.example",
		Retention:       svc,
		RetentionSweeps: sweeps,
	}
	return a.Handler()
}

func sampleSweepViews() []retention.SweepView {
	cutoff := time.Date(2026, 8, 13, 10, 0, 0, 0, time.UTC)
	created := time.Date(2026, 11, 11, 10, 0, 0, 0, time.UTC)
	completed := created
	return []retention.SweepView{
		{
			ID: "33333333-3333-4333-8333-333333333333", ProjectID: projectID, Environment: "production",
			RetentionDays: 90, Cutoff: cutoff, DryRun: true, RowsCandidates: 7,
			Status: retention.SweepSucceeded, Detail: "", CreatedAt: created, CompletedAt: &completed,
		},
		{
			ID: "44444444-4444-4444-8444-444444444444", ProjectID: projectID, Environment: "staging",
			RetentionDays: 60, Cutoff: cutoff, DryRun: false, RowsCandidates: 0,
			Status: retention.SweepFailed, Detail: "delete expired facts failed", CreatedAt: created, CompletedAt: nil,
		},
	}
}

func TestRetentionSweepsListMapsViewsAndPassesQuery(t *testing.T) {
	sweeps := &fakeSweepService{views: sampleSweepViews()}
	handler := retentionAPIWithSweeps(t, &fakeRetentionService{}, sweeps)

	w := retentionRequest(handler, http.MethodGet,
		"/api/v1/projects/"+projectID+"/retention-sweeps?environment=production&limit=50", "", "")
	governanceStatus(t, w, http.StatusOK)

	if sweeps.gotFilter.Environment != "production" || sweeps.gotLimit != 50 {
		t.Fatalf("query mapping wrong: filter=%+v limit=%d", sweeps.gotFilter, sweeps.gotLimit)
	}
	var out wire.RetentionSweepList
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil || len(out.Items) != 2 {
		t.Fatalf("bad sweep list: %s", w.Body.String())
	}
	first := out.Items[0]
	if first.SweepID == "" || first.Environment != "production" || !first.DryRun ||
		first.RowsCandidates != 7 || first.CompletedAt == nil || first.CutoffAt == "" {
		t.Fatalf("succeeded sweep mapping wrong: %+v", first)
	}
	failed := out.Items[1]
	if failed.Status != "failed" || failed.CompletedAt != nil || failed.Detail == "" || failed.DryRun {
		t.Fatalf("failed sweep mapping wrong: %+v", failed)
	}
}

func TestRetentionSweepsDefaultLimitAndOptionalEnvironment(t *testing.T) {
	sweeps := &fakeSweepService{views: sampleSweepViews()}
	handler := retentionAPIWithSweeps(t, &fakeRetentionService{}, sweeps)

	w := retentionRequest(handler, http.MethodGet,
		"/api/v1/projects/"+projectID+"/retention-sweeps", "", "")
	governanceStatus(t, w, http.StatusOK)
	if sweeps.gotLimit != retention.DefaultSweepLimit || sweeps.gotFilter.Environment != "" {
		t.Fatalf("default query wrong: filter=%+v limit=%d", sweeps.gotFilter, sweeps.gotLimit)
	}
}

func TestRetentionSweepsRejectsInvalidParams(t *testing.T) {
	handler := retentionAPIWithSweeps(t, &fakeRetentionService{}, &fakeSweepService{})
	// Transport-enforced rejections: limit bounds and unknown query parameters.
	// Environment enum validation belongs to the domain service and is covered by
	// its unit tests and the PostgreSQL integration test (the fake service here
	// intentionally bypasses domain rules).
	for _, path := range []string{
		"/api/v1/projects/" + projectID + "/retention-sweeps?limit=0",
		"/api/v1/projects/" + projectID + "/retention-sweeps?limit=101",
		"/api/v1/projects/" + projectID + "/retention-sweeps?unknown=1",
	} {
		w := retentionRequest(handler, http.MethodGet, path, "", "")
		governanceStatus(t, w, http.StatusBadRequest)
	}
}

func TestRetentionSweepsRouteAbsentWhenReaderNotWired(t *testing.T) {
	// A control API without a sweep reader must not expose the route at all.
	_, handler := retentionAPI(t, &fakeRetentionService{})
	w := retentionRequest(handler, http.MethodGet,
		"/api/v1/projects/"+projectID+"/retention-sweeps", "", "")
	governanceStatus(t, w, http.StatusNotFound)
}
