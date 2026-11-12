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

	"github.com/howtomakeaname/Evia/backend/internal/backfilling"
	"github.com/howtomakeaname/Evia/backend/internal/gen/wire"
	"github.com/howtomakeaname/Evia/backend/internal/platform/contract"
	"github.com/howtomakeaname/Evia/backend/internal/platform/cursor"
	"github.com/howtomakeaname/Evia/backend/internal/platform/httpapi"
)

type fakeBackfillService struct {
	page      backfilling.Page
	job       backfilling.Job
	gotInput  backfilling.Input
	gotFilter backfilling.ListFilter
	createErr error
}

func (f *fakeBackfillService) CreateJob(_ context.Context, in backfilling.Input) (backfilling.Job, bool, error) {
	f.gotInput = in
	if f.createErr != nil {
		return backfilling.Job{}, false, f.createErr
	}
	return f.job, true, nil
}
func (f *fakeBackfillService) GetJob(_ context.Context, _, _, _ string) (backfilling.Job, error) {
	return f.job, nil
}
func (f *fakeBackfillService) ListJobs(_ context.Context, _, _ string, filter backfilling.ListFilter) (backfilling.Page, error) {
	f.gotFilter = filter
	return f.page, nil
}
func (f *fakeBackfillService) CancelJob(_ context.Context, _, _, _ string) (backfilling.Job, error) {
	return f.job, nil
}

func backfillAPIWithService(t *testing.T, svc BackfillService) http.Handler {
	t.Helper()
	validator, err := contract.New()
	if err != nil {
		t.Fatal(err)
	}
	codec, err := cursor.New([]byte(strings.Repeat("b", 32)))
	if err != nil {
		t.Fatal(err)
	}
	a := &API{
		Boundary: &httpapi.Boundary{Validator: validator, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))},
		Access:   &fakeAccess{}, Machines: &fakeMachines{}, Cursor: codec, Origin: "https://evia.example",
		Backfills: svc,
	}
	return a.Handler()
}

func backfillRequest(handler http.Handler, method, path, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.AddCookie(&http.Cookie{Name: "evia_session", Value: "valid"})
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("X-CSRF-Token", "csrf")
	r.Header.Set("Idempotency-Key", "backfill-http-key")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	return w
}

func sampleBackfillJob() backfilling.Job {
	now := time.Date(2026, 11, 12, 10, 0, 0, 0, time.UTC)
	return backfilling.Job{
		JobID: "66666666-6666-4666-8666-666666666666", ProjectID: projectID, Environment: "development",
		ManifestID: "33333333-3333-4333-8333-333333333333", Name: "restore november",
		State: backfilling.StateQueued, RowsExpected: 12, CreatedAt: now, UpdatedAt: now,
	}
}

func TestBackfillCreateMapsInputAndEnvelope(t *testing.T) {
	svc := &fakeBackfillService{job: sampleBackfillJob()}
	handler := backfillAPIWithService(t, svc)
	body := `{"name":"restore november","environment":"development","manifest_id":"33333333-3333-4333-8333-333333333333"}`
	w := backfillRequest(handler, http.MethodPost, "/api/v1/projects/"+projectID+"/backfills", body)
	governanceStatus(t, w, http.StatusCreated)
	if svc.gotInput.ManifestID != "33333333-3333-4333-8333-333333333333" || svc.gotInput.IdempotencyKey != "backfill-http-key" {
		t.Fatalf("input mapping wrong: %+v", svc.gotInput)
	}
	var out wire.BackfillJob
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil || out.JobID == "" || out.RowsExpected != 12 || out.State != "queued" {
		t.Fatalf("response mapping wrong: %s", w.Body.String())
	}
}

func TestBackfillListGetAndCancel(t *testing.T) {
	svc := &fakeBackfillService{page: backfilling.Page{Items: []backfilling.Job{sampleBackfillJob()}}, job: sampleBackfillJob()}
	handler := backfillAPIWithService(t, svc)

	w := backfillRequest(handler, http.MethodGet, "/api/v1/projects/"+projectID+"/backfills?limit=25", "")
	governanceStatus(t, w, http.StatusOK)
	if svc.gotFilter.Limit != 25 {
		t.Fatalf("limit mapping wrong: %d", svc.gotFilter.Limit)
	}
	var list wire.BackfillList
	if err := json.Unmarshal(w.Body.Bytes(), &list); err != nil || len(list.Items) != 1 {
		t.Fatalf("list envelope wrong: %s", w.Body.String())
	}

	w = backfillRequest(handler, http.MethodGet,
		"/api/v1/projects/"+projectID+"/backfills/66666666-6666-4666-8666-666666666666", "")
	governanceStatus(t, w, http.StatusOK)

	w = backfillRequest(handler, http.MethodDelete,
		"/api/v1/projects/"+projectID+"/backfills/66666666-6666-4666-8666-666666666666", "")
	governanceStatus(t, w, http.StatusOK)

	if got := backfillRequest(handler, http.MethodGet,
		"/api/v1/projects/"+projectID+"/backfills/not-a-uuid", ""); got.Code != http.StatusBadRequest {
		t.Fatalf("malformed id expected 400 got %d", got.Code)
	}
}

func TestBackfillRouteAbsentWhenNotWired(t *testing.T) {
	validator, err := contract.New()
	if err != nil {
		t.Fatal(err)
	}
	codec, err := cursor.New([]byte(strings.Repeat("b", 32)))
	if err != nil {
		t.Fatal(err)
	}
	a := &API{
		Boundary: &httpapi.Boundary{Validator: validator, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))},
		Access:   &fakeAccess{}, Machines: &fakeMachines{}, Cursor: codec, Origin: "https://evia.example",
	}
	w := backfillRequest(a.Handler(), http.MethodGet, "/api/v1/projects/"+projectID+"/backfills", "")
	governanceStatus(t, w, http.StatusNotFound)
}
