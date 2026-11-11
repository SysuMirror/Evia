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

	"github.com/howtomakeaname/Evia/backend/internal/erasure"
	"github.com/howtomakeaname/Evia/backend/internal/gen/wire"
	"github.com/howtomakeaname/Evia/backend/internal/platform/contract"
	"github.com/howtomakeaname/Evia/backend/internal/platform/cursor"
	"github.com/howtomakeaname/Evia/backend/internal/platform/fault"
	"github.com/howtomakeaname/Evia/backend/internal/platform/httpapi"
)

const erasureJobID = "77777777-7777-4777-8777-777777777777"

type fakeErasureService struct {
	createInput erasure.Input
	created     bool
	job         erasure.Job
	listFilter  erasure.ListFilter
	page        erasure.Page
	err         error
}

func (s *fakeErasureService) CreateJob(_ context.Context, in erasure.Input) (erasure.Job, bool, error) {
	s.createInput = in
	return s.job, s.created, s.err
}
func (s *fakeErasureService) GetJob(_ context.Context, _, _, _ string) (erasure.Job, error) {
	return s.job, s.err
}
func (s *fakeErasureService) ListJobs(_ context.Context, _, _ string, filter erasure.ListFilter) (erasure.Page, error) {
	s.listFilter = filter
	return s.page, s.err
}

func erasureAPI(t *testing.T, svc ErasureService, machines MachineService) (*API, http.Handler) {
	t.Helper()
	validator, err := contract.New()
	if err != nil {
		t.Fatal(err)
	}
	codec, err := cursor.New([]byte(strings.Repeat("e", 32)))
	if err != nil {
		t.Fatal(err)
	}
	a := &API{
		Boundary: &httpapi.Boundary{Validator: validator, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))},
		Access:   &fakeAccess{}, Machines: machines, Cursor: codec, Origin: "https://evia.example",
		Erasure: svc,
	}
	return a, a.Handler()
}

func erasureHTTPRequest(handler http.Handler, method, path, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.AddCookie(&http.Cookie{Name: "evia_session", Value: "valid"})
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("X-CSRF-Token", "csrf")
	r.Header.Set("Idempotency-Key", "erasure-http-key")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	return w
}

func sampleErasureJob() erasure.Job {
	expiry := time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC)
	return erasure.Job{
		JobID: erasureJobID, ProjectID: projectID, Environment: "development", ActorID: "actor",
		SubjectKind: erasure.SubjectUID, SubjectValue: "user-1", Reason: "request",
		State: erasure.StateQueued, BackupExpiryAt: &expiry, CreatedAt: time.Now().UTC(),
	}
}

func TestErasureCreateMapsInputAndDistinguishesReplay(t *testing.T) {
	svc := &fakeErasureService{created: true, job: sampleErasureJob()}
	_, handler := erasureAPI(t, svc, &fakeMachines{})
	body := `{"environment":"development","uid":"user-1","reason":"request"}`
	w := erasureHTTPRequest(handler, http.MethodPost, "/api/v1/projects/"+projectID+"/erasure-jobs", body)
	governanceStatus(t, w, http.StatusCreated)
	var out wire.ErasureJob
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil || out.JobID != erasureJobID || out.State != erasure.StateQueued || out.BackupExpiryAt == nil {
		t.Fatalf("bad erasure response: %s", w.Body.String())
	}
	if svc.createInput.SubjectKind != erasure.SubjectUID || svc.createInput.SubjectValue != "user-1" ||
		svc.createInput.Environment != "development" || svc.createInput.IdempotencyKey != "erasure-http-key" ||
		len(svc.createInput.RequestHash) != 64 {
		t.Fatalf("service input mapping wrong: %+v", svc.createInput)
	}
	// Replay returns 200 with the same body.
	svc.created = false
	w = erasureHTTPRequest(handler, http.MethodPost, "/api/v1/projects/"+projectID+"/erasure-jobs", body)
	governanceStatus(t, w, http.StatusOK)
}

func TestErasureCreateRejectsAmbiguousIdentity(t *testing.T) {
	for _, body := range []string{
		`{"environment":"development","reason":"request"}`,
		`{"environment":"development","uid":"user-1","anonymous_id":"11111111-1111-4111-8111-111111111111","reason":"request"}`,
	} {
		svc := &fakeErasureService{}
		_, handler := erasureAPI(t, svc, &fakeMachines{})
		w := erasureHTTPRequest(handler, http.MethodPost, "/api/v1/projects/"+projectID+"/erasure-jobs", body)
		governanceStatus(t, w, http.StatusBadRequest)
		if svc.createInput.IdempotencyKey != "" {
			t.Fatal("ambiguous identity reached the service")
		}
	}
}

func TestErasureListAndGet(t *testing.T) {
	job := sampleErasureJob()
	svc := &fakeErasureService{job: job, page: erasure.Page{Items: []erasure.Job{job}}}
	_, handler := erasureAPI(t, svc, &fakeMachines{})
	w := erasureHTTPRequest(handler, http.MethodGet, "/api/v1/projects/"+projectID+"/erasure-jobs?limit=50", "")
	governanceStatus(t, w, http.StatusOK)
	var list wire.ErasureList
	if err := json.Unmarshal(w.Body.Bytes(), &list); err != nil || len(list.Items) != 1 || list.NextCursor != nil {
		t.Fatalf("bad list response: %s", w.Body.String())
	}
	if svc.listFilter.Limit != 50 || svc.listFilter.After != "" {
		t.Fatalf("list filter mapping wrong: %+v", svc.listFilter)
	}
	// An over-limit page produces a signed cursor covering the keyset position.
	svc.page = erasure.Page{Items: []erasure.Job{job, job}, After: erasureJobID}
	w = erasureHTTPRequest(handler, http.MethodGet, "/api/v1/projects/"+projectID+"/erasure-jobs?limit=1", "")
	governanceStatus(t, w, http.StatusOK)
	if err := json.Unmarshal(w.Body.Bytes(), &list); err != nil || len(list.Items) != 1 || list.NextCursor == nil {
		t.Fatalf("expected a cursored single-item page: %s", w.Body.String())
	}
	if svc.listFilter.Limit != 1 {
		t.Fatalf("limit mapping wrong: %+v", svc.listFilter)
	}
	raw, err := erasureAPICodec(t).Decode("erasure_jobs:"+userID+":"+projectID, *list.NextCursor)
	if err != nil || !strings.Contains(raw, erasureJobID) {
		t.Fatal("cursor not decodable", raw, err)
	}
	w = erasureHTTPRequest(handler, http.MethodGet, "/api/v1/projects/"+projectID+"/erasure-jobs/"+erasureJobID, "")
	governanceStatus(t, w, http.StatusOK)
	w = erasureHTTPRequest(handler, http.MethodGet, "/api/v1/projects/"+projectID+"/erasure-jobs/not-a-uuid", "")
	governanceStatus(t, w, http.StatusBadRequest)
}

func erasureAPICodec(t *testing.T) *cursor.Codec {
	t.Helper()
	codec, err := cursor.New([]byte(strings.Repeat("e", 32)))
	if err != nil {
		t.Fatal(err)
	}
	return codec
}

func TestErasureMachineRequiresExactGrant(t *testing.T) {
	body := `{"environment":"development","uid":"user-1","reason":"request"}`
	// A machine without data.erasure is rejected at the HTTP boundary.
	denied := &fakeErasureService{job: sampleErasureJob(), created: true}
	_, deniedHandler := erasureAPI(t, denied, &scopedMachineAuthentication{permissions: []string{"project.read"}})
	r := httptest.NewRequest(http.MethodPost, "/api/v1/projects/"+projectID+"/erasure-jobs", strings.NewReader(body))
	r.Header.Set("Authorization", "Bearer "+machineToken)
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("X-CSRF-Token", "csrf")
	r.Header.Set("Idempotency-Key", "machine-erasure")
	w := httptest.NewRecorder()
	deniedHandler.ServeHTTP(w, r)
	governanceStatus(t, w, http.StatusForbidden)
	if denied.createInput.IdempotencyKey != "" {
		t.Fatal("unauthorized machine reached the service")
	}
	// The explicit grant lets the request through to the service.
	granted := &fakeErasureService{job: sampleErasureJob(), created: true}
	_, grantedHandler := erasureAPI(t, granted, &scopedMachineAuthentication{permissions: []string{"data.erasure"}})
	r2 := httptest.NewRequest(http.MethodPost, "/api/v1/projects/"+projectID+"/erasure-jobs", strings.NewReader(body))
	r2.Header.Set("Authorization", "Bearer "+machineToken)
	r2.Header.Set("Content-Type", "application/json")
	r2.Header.Set("X-CSRF-Token", "csrf")
	r2.Header.Set("Idempotency-Key", "machine-erasure")
	w2 := httptest.NewRecorder()
	grantedHandler.ServeHTTP(w2, r2)
	governanceStatus(t, w2, http.StatusCreated)
	if granted.createInput.SubjectValue != "user-1" {
		t.Fatal("authorized machine request did not reach the service")
	}
}

func TestErasurePropagatesServiceConflict(t *testing.T) {
	svc := &fakeErasureService{err: fault.New(fault.Conflict, "ERASURE_JOB_ACTIVE", "该身份已有进行中的删除作业")}
	_, handler := erasureAPI(t, svc, &fakeMachines{})
	w := erasureHTTPRequest(handler, http.MethodPost, "/api/v1/projects/"+projectID+"/erasure-jobs", `{"environment":"development","uid":"user-1","reason":"request"}`)
	governanceStatus(t, w, http.StatusConflict)
}
