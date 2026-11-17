package control

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/howtomakeaname/Evia/backend/internal/gen/wire"
	"github.com/howtomakeaname/Evia/backend/internal/platform/contract"
	"github.com/howtomakeaname/Evia/backend/internal/platform/cursor"
	"github.com/howtomakeaname/Evia/backend/internal/platform/fault"
	"github.com/howtomakeaname/Evia/backend/internal/platform/httpapi"
	"github.com/howtomakeaname/Evia/backend/internal/reports"
)

type fakeReportService struct {
	out              reports.Report
	list             []reports.Report
	err              error
	create           reports.Input
	update           reports.Input
	expectedRevision int64
	listCall         struct {
		actor, project, environment, after string
		limit                              int
		search                             string
	}
	deletedID string
}

func (s *fakeReportService) Create(_ context.Context, in reports.Input, expected int64) (reports.Report, error) {
	s.create = in
	s.expectedRevision = expected
	if s.err != nil {
		return reports.Report{}, s.err
	}
	return s.out, nil
}

func (s *fakeReportService) List(_ context.Context, actor, project, environment, after string, limit int, search string) ([]reports.Report, error) {
	s.listCall.actor, s.listCall.project, s.listCall.environment, s.listCall.after, s.listCall.limit, s.listCall.search = actor, project, environment, after, limit, search
	if s.err != nil {
		return nil, s.err
	}
	return s.list, nil
}

func (s *fakeReportService) Get(_ context.Context, _, _, id string) (reports.Report, error) {
	if s.err != nil {
		return reports.Report{}, s.err
	}
	s.out.ID = id
	return s.out, nil
}

func (s *fakeReportService) Update(_ context.Context, in reports.Input, expected int64) (reports.Report, error) {
	s.update = in
	s.expectedRevision = expected
	if s.err != nil {
		return reports.Report{}, s.err
	}
	return s.out, nil
}

func (s *fakeReportService) Delete(_ context.Context, _, _, id string) error {
	s.deletedID = id
	return s.err
}

func reportsAPI(t *testing.T, svc ReportService) (*API, http.Handler) {
	t.Helper()
	validator, err := contract.New()
	if err != nil {
		t.Fatal(err)
	}
	codec, err := cursor.New([]byte(strings.Repeat("p", 32)))
	if err != nil {
		t.Fatal(err)
	}
	a := &API{
		Boundary: &httpapi.Boundary{Validator: validator, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))},
		Access:   &fakeAccess{}, Machines: &fakeMachines{}, Cursor: codec, Origin: "https://evia.example",
		Reports: svc,
	}
	return a, a.Handler()
}

const reportBody = `{"environment":"development","definition":{"title":"周报","description":"合成说明","sections":[{"reading_order":1,"section_title":"第一章","analysis_id":"44444444-4444-4444-8444-444444444444","analysis_version":1},{"reading_order":2,"section_title":"第二章","analysis_id":"55555555-5555-4555-8555-555555555555","analysis_version":3}]}}`

func reportRequest(handler http.Handler, method, path, body, ifMatch string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.AddCookie(&http.Cookie{Name: "evia_session", Value: "valid"})
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("X-CSRF-Token", "csrf")
	r.Header.Set("Idempotency-Key", "report-http-key")
	if ifMatch != "" {
		r.Header.Set("If-Match", ifMatch)
	}
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	return w
}

func sampleReport() reports.Report {
	return reports.Report{
		ID: "66666666-6666-4666-8666-666666666666", ProjectID: projectID, Environment: "development", Revision: 1,
		Definition: reports.Definition{
			Title: "周报", Description: "合成说明",
			Sections: []reports.Section{
				{SectionID: "77777777-7777-4777-8777-777777777777", ReadingOrder: 1, SectionTitle: "第一章", AnalysisID: "44444444-4444-4444-8444-444444444444", AnalysisVersion: 1},
			},
		},
		CreatedAt: "2026-11-12T00:00:00Z", UpdatedAt: "2026-11-12T00:00:00Z",
	}
}

func TestReportCreateMapsInputAndReturns201(t *testing.T) {
	svc := &fakeReportService{out: sampleReport()}
	_, handler := reportsAPI(t, svc)
	w := reportRequest(handler, http.MethodPost, "/api/v1/projects/"+projectID+"/reports", reportBody, `"0"`)
	governanceStatus(t, w, http.StatusCreated)
	if svc.expectedRevision != 0 || svc.create.ActorID != userID || svc.create.ProjectID != projectID ||
		svc.create.Environment != "development" || svc.create.IdempotencyKey != "report-http-key" ||
		len(svc.create.RequestHash) != 64 || len(svc.create.Definition.Sections) != 2 ||
		svc.create.Definition.Sections[1].AnalysisVersion != 3 {
		t.Fatalf("service input mapping wrong: %+v rev=%d", svc.create, svc.expectedRevision)
	}
	var out wire.Report
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil || out.ReportID == "" || out.Revision != 1 ||
		len(out.Definition.Sections) != 1 || out.Definition.Sections[0].SectionID == "" {
		t.Fatalf("response wire wrong: %s", w.Body.String())
	}
	if w.Header().Get("ETag") != `"1"` {
		t.Fatalf("ETag = %q", w.Header().Get("ETag"))
	}
}

func TestReportCreateRejectsNonZeroRevisionAndBadBodies(t *testing.T) {
	svc := &fakeReportService{}
	_, handler := reportsAPI(t, svc)
	w := reportRequest(handler, http.MethodPost, "/api/v1/projects/"+projectID+"/reports", reportBody, `"5"`)
	governanceStatus(t, w, http.StatusBadRequest)
	if svc.create.IdempotencyKey != "" {
		t.Fatal("positive revision reached the service on create")
	}
	// Contract validation rejects structurally impossible bodies.
	for _, body := range []string{
		`{"environment":"development","definition":{"title":"周报","description":"","sections":[]}}`,
		`{"environment":"edge","definition":{"title":"周报","description":"","sections":[{"reading_order":1,"section_title":"第一章","analysis_id":"44444444-4444-4444-8444-444444444444","analysis_version":1}]}}`,
		`{"environment":"development","definition":{"title":"周报","description":"","sections":[{"reading_order":99,"section_title":"第一章","analysis_id":"44444444-4444-4444-8444-444444444444","analysis_version":1}]}}`,
	} {
		w = reportRequest(handler, http.MethodPost, "/api/v1/projects/"+projectID+"/reports", body, `"0"`)
		governanceStatus(t, w, http.StatusBadRequest)
	}
}

func TestReportUpdateMapsPathIDAndPositiveRevision(t *testing.T) {
	report := sampleReport()
	report.Revision = 2
	svc := &fakeReportService{out: report}
	_, handler := reportsAPI(t, svc)
	w := reportRequest(handler, http.MethodPatch, "/api/v1/projects/"+projectID+"/reports/"+report.ID, reportBody, `"2"`)
	governanceStatus(t, w, http.StatusOK)
	if svc.update.ReportID != report.ID || svc.expectedRevision != 2 ||
		svc.update.IdempotencyKey != "report-http-key" || len(svc.update.RequestHash) != 64 {
		t.Fatalf("update mapping wrong: %+v rev=%d", svc.update, svc.expectedRevision)
	}
	if w.Header().Get("ETag") != `"2"` {
		t.Fatalf("ETag = %q", w.Header().Get("ETag"))
	}
	// Zero revision is a create-only precondition and must fail on update.
	w = reportRequest(handler, http.MethodPatch, "/api/v1/projects/"+projectID+"/reports/"+report.ID, reportBody, `"0"`)
	governanceStatus(t, w, http.StatusBadRequest)
}

func TestReportGetRejectsMalformedID(t *testing.T) {
	svc := &fakeReportService{out: sampleReport()}
	_, handler := reportsAPI(t, svc)
	w := reportRequest(handler, http.MethodGet, "/api/v1/projects/"+projectID+"/reports/not-a-uuid", "", "")
	governanceStatus(t, w, http.StatusBadRequest)
}

func TestReportDeleteReturnsNoContent(t *testing.T) {
	report := sampleReport()
	svc := &fakeReportService{}
	_, handler := reportsAPI(t, svc)
	w := reportRequest(handler, http.MethodDelete, "/api/v1/projects/"+projectID+"/reports/"+report.ID, "", "")
	if w.Code != http.StatusNoContent || w.Body.Len() != 0 {
		t.Fatalf("delete status=%d body=%q", w.Code, w.Body.String())
	}
	if svc.deletedID != report.ID {
		t.Fatalf("delete id=%q", svc.deletedID)
	}
}

func TestReportListEncodesCursorAndRejectsBadQuery(t *testing.T) {
	svc := &fakeReportService{list: []reports.Report{sampleReport(), sampleReport(), sampleReport()}}
	a, handler := reportsAPI(t, svc)
	base := "/api/v1/projects/" + projectID + "/reports?environment=development&limit=2"
	w := reportRequest(handler, http.MethodGet, base, "", "")
	governanceStatus(t, w, http.StatusOK)
	if svc.listCall.limit != 3 || svc.listCall.environment != "development" || svc.listCall.after != "" {
		t.Fatalf("list args wrong: %+v", svc.listCall)
	}
	var out wire.ReportList
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil || len(out.Items) != 2 || out.NextCursor == nil {
		t.Fatalf("first page wrong: %s", w.Body.String())
	}
	// The cursor must resolve back to the keyset position under the same scope.
	scope := "reports:" + userID + ":" + projectID + ":development:"
	after, err := a.Cursor.Decode(scope, *out.NextCursor)
	if err != nil || after == "" {
		t.Fatalf("cursor not decodable: %v %s", err, *out.NextCursor)
	}
	svc.list = []reports.Report{sampleReport()}
	w = reportRequest(handler, http.MethodGet, base+"&cursor="+*out.NextCursor, "", "")
	governanceStatus(t, w, http.StatusOK)
	if err = json.Unmarshal(w.Body.Bytes(), &out); err != nil || len(out.Items) != 1 || out.NextCursor != nil {
		t.Fatalf("final page wrong: %s", w.Body.String())
	}
	if svc.listCall.after == "" {
		t.Fatal("cursor did not reach the service as an after marker")
	}
	// Missing environment and unknown parameters are contract violations.
	w = reportRequest(handler, http.MethodGet, "/api/v1/projects/"+projectID+"/reports", "", "")
	governanceStatus(t, w, http.StatusBadRequest)
	w = reportRequest(handler, http.MethodGet, "/api/v1/projects/"+projectID+"/reports?environment=development&unexpected=1", "", "")
	governanceStatus(t, w, http.StatusBadRequest)
}

func TestReportListSearchTerm(t *testing.T) {
	svc := &fakeReportService{list: []reports.Report{sampleReport(), sampleReport()}}
	a, handler := reportsAPI(t, svc)
	// A bounded search term is trimmed, forwarded and bound to the cursor scope.
	w := reportRequest(
		handler,
		http.MethodGet,
		"/api/v1/projects/"+projectID+"/reports?environment=development&limit=1&q=%20%E5%91%A8%E6%8A%A5%20",
		"",
		"",
	)
	governanceStatus(t, w, http.StatusOK)
	if svc.listCall.search != "周报" {
		t.Fatalf("search term must be trimmed and forwarded, got %q", svc.listCall.search)
	}
	var out wire.ReportList
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil || out.NextCursor == nil {
		t.Fatalf("missing continuation cursor: %s", w.Body.String())
	}
	searchScope := "reports:" + userID + ":" + projectID + ":development:周报"
	if decoded, err := a.Cursor.Decode(searchScope, *out.NextCursor); err != nil || decoded != sampleReport().ID {
		t.Fatal("cursor must be bound to the search scope")
	}
	// Unknown query parameters stay rejected even though q is now allowlisted.
	w = reportRequest(
		handler,
		http.MethodGet,
		"/api/v1/projects/"+projectID+"/reports?environment=development&q=x&unexpected=1",
		"",
		"",
	)
	governanceStatus(t, w, http.StatusBadRequest)
	// An overlong term is rejected at the boundary without dispatching.
	before := len(svc.list)
	w = reportRequest(
		handler,
		http.MethodGet,
		"/api/v1/projects/"+projectID+"/reports?environment=development&q="+url.QueryEscape(strings.Repeat("字", 129)),
		"",
		"",
	)
	governanceStatus(t, w, http.StatusBadRequest)
	if len(svc.list) != before {
		t.Fatal("overlong search term changed service state")
	}
}

func TestReportPropagatesDomainFaults(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want int
	}{
		{"not found", fault.New(fault.NotFound, "REPORT_NOT_FOUND", "报告不存在"), http.StatusNotFound},
		{"conflict", fault.New(fault.Conflict, "IDEMPOTENCY_CONFLICT", "幂等冲突"), http.StatusConflict},
		{"forbidden", fault.New(fault.Forbidden, "REPORT_FORBIDDEN", "无权操作"), http.StatusForbidden},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc := &fakeReportService{err: tc.err}
			_, handler := reportsAPI(t, svc)
			w := reportRequest(handler, http.MethodGet, "/api/v1/projects/"+projectID+"/reports/66666666-6666-4666-8666-666666666666", "", "")
			governanceStatus(t, w, tc.want)
		})
	}
}
