package control

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/howtomakeaname/Evia/backend/internal/analysis"
	"github.com/howtomakeaname/Evia/backend/internal/exporting"
	"github.com/howtomakeaname/Evia/backend/internal/gen/wire"
	"github.com/howtomakeaname/Evia/backend/internal/platform/contract"
	"github.com/howtomakeaname/Evia/backend/internal/platform/cursor"
	"github.com/howtomakeaname/Evia/backend/internal/platform/httpapi"
)

const exportJobID = "88888888-8888-4888-8888-888888888888"

type fakeExportService struct {
	created          bool
	job              exporting.Job
	page             exporting.Page
	grantToken       string
	grant            exporting.Grant
	redeemJob        exporting.Job
	createInput      exporting.Input
	savedCreateInput exporting.SavedAnalysisExportInput
	savedAnalysisID  string
	savedVersion     int64
}

func (s *fakeExportService) CreateJob(_ context.Context, in exporting.Input) (exporting.Job, bool, error) {
	s.createInput = in
	return s.job, s.created, nil
}
func (s *fakeExportService) CreateSavedAnalysisExportJob(_ context.Context, _, _, analysisID string, version int64, in exporting.SavedAnalysisExportInput) (exporting.Job, bool, error) {
	s.savedAnalysisID = analysisID
	s.savedVersion = version
	s.savedCreateInput = in
	return s.job, s.created, nil
}
func (s *fakeExportService) GetExportJob(context.Context, string, string, string) (exporting.Job, error) {
	return s.job, nil
}
func (s *fakeExportService) ListExportJobs(_ context.Context, _, _ string, filter exporting.ListFilter) (exporting.Page, error) {
	s.page.Items = []exporting.Job{s.job}
	return s.page, nil
}
func (s *fakeExportService) CancelExportJob(context.Context, string, string, string) (exporting.Job, error) {
	cancelled := s.job
	cancelled.State = exporting.StateCancelled
	return cancelled, nil
}
func (s *fakeExportService) MintGrant(context.Context, string, string, string) (string, exporting.Grant, error) {
	return s.grantToken, s.grant, nil
}
func (s *fakeExportService) Redeem(context.Context, string) (exporting.Job, exporting.Grant, error) {
	return s.redeemJob, exporting.Grant{}, nil
}

func exportAPI(t *testing.T, svc ExportService, downloads exporting.ArtifactStore) (*API, http.Handler) {
	t.Helper()
	validator, err := contract.New()
	if err != nil {
		t.Fatal(err)
	}
	codec, err := cursor.New([]byte(strings.Repeat("f", 32)))
	if err != nil {
		t.Fatal(err)
	}
	a := &API{
		Boundary:        &httpapi.Boundary{Validator: validator, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))},
		Access:          &fakeAccess{},
		Machines:        &fakeMachines{},
		Cursor:          codec,
		Origin:          "https://evia.example",
		Exports:         svc,
		ExportValidator: validator,
		ExportDownloads: downloads,
	}
	return a, a.Handler()
}

func exportRequest(handler http.Handler, method, path, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.AddCookie(&http.Cookie{Name: "evia_session", Value: "valid"})
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("X-CSRF-Token", "csrf")
	r.Header.Set("Idempotency-Key", "export-http-key")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	return w
}

func sampleExportJob() exporting.Job {
	now := time.Date(2026, 11, 12, 10, 0, 0, 0, time.UTC)
	rows := 3
	bytesVal := int64(420)
	sha := strings.Repeat("a", 64)
	path := "/var/exports/" + exportJobID + ".json"
	finished := now.Add(time.Minute)
	return exporting.Job{
		JobID: exportJobID, ProjectID: projectID, Environment: "development", ActorID: userID,
		Name: "weekly", Format: exporting.FormatJSON, State: exporting.StateCompleted,
		ResultRows: &rows, ArtifactBytes: &bytesVal, ArtifactSHA256: &sha, ArtifactPath: &path,
		CreatedAt: now, UpdatedAt: now, FinishedAt: &finished,
	}
}

const validExportBody = `{"name":"weekly","environment":"development","query":{
 "kind":"trend","environment":"development",
 "time_range":{"from":"2026-11-07T10:00:00Z","to":"2026-11-07T11:00:00Z"},"timezone":"UTC"}}`

func TestExportCreateValidatesAndMaps(t *testing.T) {
	svc := &fakeExportService{created: true, job: sampleExportJob()}
	_, handler := exportAPI(t, svc, nil)
	w := exportRequest(handler, http.MethodPost, "/api/v1/projects/"+projectID+"/exports", validExportBody)
	governanceStatus(t, w, http.StatusCreated)
	var out wire.ExportJob
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil || out.JobID != exportJobID || out.State != exporting.StateCompleted {
		t.Fatalf("bad response: %v body=%s", err, w.Body.String())
	}
	if svc.createInput.Name != "weekly" || svc.createInput.Request.Kind != "trend" || svc.createInput.IdempotencyKey != "export-http-key" {
		t.Fatalf("input mapping wrong: %+v", svc.createInput)
	}
}

func TestExportCreateRejectsInvalidQuery(t *testing.T) {
	svc := &fakeExportService{created: true, job: sampleExportJob()}
	_, handler := exportAPI(t, svc, nil)
	body := `{"name":"weekly","environment":"development","query":{"kind":"unknown","environment":"development"}}`
	w := exportRequest(handler, http.MethodPost, "/api/v1/projects/"+projectID+"/exports", body)
	governanceStatus(t, w, http.StatusBadRequest)
}

const savedAnalysisID = "77777777-7777-4777-8777-777777777777"

const validSavedExportBody = `{"name":"version export","format":"csv",
 "time_range":{"from":"2026-11-07T10:00:00Z","to":"2026-11-07T11:00:00Z"},"timezone":"UTC"}`

func TestSavedAnalysisExportCreateValidatesAndMaps(t *testing.T) {
	job := sampleExportJob()
	job.Source = &exporting.SavedAnalysisSource{AnalysisID: savedAnalysisID, Version: 3}
	svc := &fakeExportService{created: true, job: job}
	_, handler := exportAPI(t, svc, nil)
	path := "/api/v1/projects/" + projectID + "/saved-analyses/" + savedAnalysisID + "/versions/3/exports"
	w := exportRequest(handler, http.MethodPost, path, validSavedExportBody)
	governanceStatus(t, w, http.StatusCreated)
	var out wire.ExportJob
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil || out.JobID != exportJobID {
		t.Fatalf("bad response: %v body=%s", err, w.Body.String())
	}
	if out.SourceAnalysisID == nil || *out.SourceAnalysisID != savedAnalysisID ||
		out.SourceAnalysisVersion == nil || *out.SourceAnalysisVersion != 3 {
		t.Fatalf("source witness missing from response: %+v", out)
	}
	if svc.savedAnalysisID != savedAnalysisID || svc.savedVersion != 3 {
		t.Fatalf("route params not mapped: %s %d", svc.savedAnalysisID, svc.savedVersion)
	}
	in := svc.savedCreateInput
	if in.Name != "version export" || in.Format != exporting.FormatCSV || in.Timezone != "UTC" ||
		in.IdempotencyKey != "export-http-key" || len(in.RequestHash) != 64 ||
		in.TimeRange.From.IsZero() || in.TimeRange.To.IsZero() {
		t.Fatalf("saved export input mapping wrong: %+v", in)
	}
}

func TestSavedAnalysisExportRejectsBadVersionAndBody(t *testing.T) {
	svc := &fakeExportService{created: true, job: sampleExportJob()}
	_, handler := exportAPI(t, svc, nil)
	base := "/api/v1/projects/" + projectID + "/saved-analyses/" + savedAnalysisID + "/versions/"
	// Zero/non-numeric versions are rejected before the service is touched.
	w := exportRequest(handler, http.MethodPost, base+"0/exports", validSavedExportBody)
	governanceStatus(t, w, http.StatusBadRequest)
	w = exportRequest(handler, http.MethodPost, base+"abc/exports", validSavedExportBody)
	governanceStatus(t, w, http.StatusBadRequest)
	// The schema requires the bounded time range and timezone.
	w = exportRequest(handler, http.MethodPost, base+"1/exports", `{"name":"missing range"}`)
	governanceStatus(t, w, http.StatusBadRequest)
	if svc.savedCreateInput.Name != "" {
		t.Fatal("rejected request must not reach the service")
	}
}

func TestExportListGetAndCancel(t *testing.T) {
	svc := &fakeExportService{job: sampleExportJob()}
	_, handler := exportAPI(t, svc, nil)
	w := exportRequest(handler, http.MethodGet, "/api/v1/projects/"+projectID+"/exports?limit=20", "")
	governanceStatus(t, w, http.StatusOK)
	var list wire.ExportList
	if err := json.Unmarshal(w.Body.Bytes(), &list); err != nil || len(list.Items) != 1 {
		t.Fatalf("bad list: %s", w.Body.String())
	}
	w = exportRequest(handler, http.MethodGet, "/api/v1/projects/"+projectID+"/exports/"+exportJobID, "")
	governanceStatus(t, w, http.StatusOK)
	w = exportRequest(handler, http.MethodDelete, "/api/v1/projects/"+projectID+"/exports/"+exportJobID, "")
	governanceStatus(t, w, http.StatusOK)
	var cancelled wire.ExportJob
	if err := json.Unmarshal(w.Body.Bytes(), &cancelled); err != nil || cancelled.State != exporting.StateCancelled {
		t.Fatalf("cancel mapping wrong: %s", w.Body.String())
	}
}

func TestExportGrantMint(t *testing.T) {
	expiry := time.Now().Add(exporting.GrantTTL).UTC()
	svc := &fakeExportService{
		job:        sampleExportJob(),
		grantToken: strings.Repeat("9", 64),
		grant:      exporting.Grant{ExpiresAt: expiry},
	}
	_, handler := exportAPI(t, svc, nil)
	w := exportRequest(handler, http.MethodPost, "/api/v1/projects/"+projectID+"/exports/"+exportJobID+"/download-grants", "")
	governanceStatus(t, w, http.StatusCreated)
	var grant wire.ExportGrant
	if err := json.Unmarshal(w.Body.Bytes(), &grant); err != nil || len(grant.Token) != 64 || grant.ExpiresAt == "" {
		t.Fatalf("grant response wrong: %s", w.Body.String())
	}
}

func TestExportDownloadRedeemsAndStreamsArtifact(t *testing.T) {
	dir := t.TempDir()
	downloads, err := exporting.NewFileArtifactStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	artifactPath := filepath.Join(dir, exportJobID+".json")
	payload := `{"query_id":"q1","columns":[],"rows":[]}`
	if err = os.WriteFile(artifactPath, []byte(payload), 0o600); err != nil {
		t.Fatal(err)
	}
	job := sampleExportJob()
	job.ArtifactPath = &artifactPath
	svc := &fakeExportService{redeemJob: job}
	_, handler := exportAPI(t, svc, downloads)

	// No session: the capability token alone must authorize the download.
	r := httptest.NewRequest(http.MethodGet, "/api/v1/exports/downloads/"+strings.Repeat("7", 64), nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	governanceStatus(t, w, http.StatusOK)
	if w.Header().Get("Content-Disposition") == "" || w.Header().Get("X-Content-SHA256") != strings.Repeat("a", 64) {
		t.Fatalf("download headers wrong: %+v", w.Header())
	}
	if w.Body.String() != payload {
		t.Fatalf("streamed payload mismatch: %q", w.Body.String())
	}

	// Malformed tokens are rejected before reaching the service.
	r = httptest.NewRequest(http.MethodGet, "/api/v1/exports/downloads/short", nil)
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	governanceStatus(t, w, http.StatusForbidden)
}

func TestExportDownloadUsesCsvMediaType(t *testing.T) {
	dir := t.TempDir()
	downloads, err := exporting.NewFileArtifactStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	artifactPath := filepath.Join(dir, exportJobID+".csv")
	payload := "subject,count\nuser1,2\n"
	if err = os.WriteFile(artifactPath, []byte(payload), 0o600); err != nil {
		t.Fatal(err)
	}
	job := sampleExportJob()
	job.Format = exporting.FormatCSV
	job.ArtifactPath = &artifactPath
	svc := &fakeExportService{redeemJob: job}
	_, handler := exportAPI(t, svc, downloads)

	r := httptest.NewRequest(http.MethodGet, "/api/v1/exports/downloads/"+strings.Repeat("7", 64), nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	// The artifact stream is not the JSON envelope, so assert the shared
	// boundary headers directly instead of using governanceStatus.
	if w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
	if w.Header().Get("Cache-Control") != "no-store" || w.Header().Get("X-Request-ID") == "" {
		t.Fatalf("missing shared response boundary: %v", w.Header())
	}
	if ct := w.Header().Get("Content-Type"); ct != "text/csv; charset=utf-8" {
		t.Fatalf("csv content type wrong: %s", ct)
	}
	if cd := w.Header().Get("Content-Disposition"); !strings.Contains(cd, exportJobID+".csv") {
		t.Fatalf("csv filename extension wrong: %s", cd)
	}
	if w.Body.String() != payload {
		t.Fatalf("streamed csv payload mismatch: %q", w.Body.String())
	}
}

func TestExportDownloadUsesNDJSONMediaType(t *testing.T) {
	dir := t.TempDir()
	downloads, err := exporting.NewFileArtifactStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	artifactPath := filepath.Join(dir, exportJobID+".ndjson")
	payload := "{\"count\":2,\"subject\":\"user1\"}\n{\"count\":1,\"subject\":\"user2\"}\n"
	if err = os.WriteFile(artifactPath, []byte(payload), 0o600); err != nil {
		t.Fatal(err)
	}
	job := sampleExportJob()
	job.Format = exporting.FormatNDJSON
	job.ArtifactPath = &artifactPath
	svc := &fakeExportService{redeemJob: job}
	_, handler := exportAPI(t, svc, downloads)

	r := httptest.NewRequest(http.MethodGet, "/api/v1/exports/downloads/"+strings.Repeat("7", 64), nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
	if ct := w.Header().Get("Content-Type"); ct != "application/x-ndjson; charset=utf-8" {
		t.Fatalf("ndjson content type wrong: %s", ct)
	}
	if cd := w.Header().Get("Content-Disposition"); !strings.Contains(cd, exportJobID+".ndjson") {
		t.Fatalf("ndjson filename extension wrong: %s", cd)
	}
	if w.Body.String() != payload {
		t.Fatalf("streamed ndjson payload mismatch: %q", w.Body.String())
	}
}

func TestExportRoutesRequireSession(t *testing.T) {
	svc := &fakeExportService{job: sampleExportJob()}
	_, handler := exportAPI(t, svc, nil)
	r := httptest.NewRequest(http.MethodGet, "/api/v1/projects/"+projectID+"/exports", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	if w.Code == http.StatusOK {
		t.Fatal("export list without a session must be rejected")
	}
}

var _ analysis.Request
