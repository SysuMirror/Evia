package control

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"testing"
	"time"

	"github.com/howtomakeaname/Evia/backend/internal/analysis"
	"github.com/howtomakeaname/Evia/backend/internal/platform/contract"
	"github.com/howtomakeaname/Evia/backend/internal/platform/fault"
	"github.com/howtomakeaname/Evia/backend/internal/platform/httpapi"
)

const queryJobID = "44444444-4444-4444-8444-444444444444"
const queryURL = "/api/v1/projects/" + projectID + "/queries"
const validQueryBody = `{"kind":"trend","environment":"development","time_range":{"from":"2026-11-07T10:00:00Z","to":"2026-11-07T11:00:00Z"},"timezone":"UTC"}`

type analysisFake struct {
	calls int
	err   error
}

func (f *analysisFake) job(actor, project string) (analysis.Job, error) {
	if actor != userID || project != projectID {
		panic("query escaped actor/project scope")
	}
	f.calls++
	return analysis.Job{ID: queryJobID, State: "queued", CreatedAt: time.Date(2026, 11, 7, 10, 0, 0, 0, time.UTC)}, f.err
}
func (f *analysisFake) Create(_ context.Context, a, p string, _ json.RawMessage) (analysis.Job, error) {
	return f.job(a, p)
}
func (f *analysisFake) Get(_ context.Context, a, p, id string) (analysis.Job, error) {
	if id != queryJobID {
		panic("query ID lost")
	}
	return f.job(a, p)
}
func (f *analysisFake) Cancel(ctx context.Context, a, p, id string) (analysis.Job, error) {
	return f.Get(ctx, a, p, id)
}
func (f *analysisFake) Result(_ context.Context, a, p, id string) (analysis.Result, error) {
	_, e := f.job(a, p)
	return analysis.Result{QueryID: id, Columns: []analysis.Column{}, Rows: []map[string]any{}, Metadata: analysis.Metadata{Accuracy: "exact", SampleRate: 1, Coverage: analysis.Coverage{TimeRange: analysis.TimeRange{From: time.Date(2026, 11, 7, 10, 0, 0, 0, time.UTC), To: time.Date(2026, 11, 7, 11, 0, 0, 0, time.UTC)}, State: "provisional", KnownGaps: []string{"pipeline_completeness_not_established"}}, GeneratedAt: time.Date(2026, 11, 7, 11, 0, 0, 0, time.UTC)}}, e
}
func analysisHandler(t *testing.T, f *analysisFake, access *catalogAccess) http.Handler {
	t.Helper()
	validator, e := contract.New()
	if e != nil {
		t.Fatal(e)
	}
	api := &API{Boundary: &httpapi.Boundary{Validator: validator, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))}, Access: access, Origin: "https://evia.example"}
	mux := http.NewServeMux()
	(&AnalysisAPI{API: api, Service: f}).Register(mux)
	return api.Boundary.Middleware(mux)
}
func TestAnalysisHTTPContractAuthenticationAndScope(t *testing.T) {
	for _, tc := range []struct {
		name, method, path, body string
		headers                  map[string]string
		status, calls            int
	}{
		{"submit", "POST", queryURL, validQueryBody, nil, 202, 1},
		{"poll", "GET", queryURL + "/" + queryJobID, "", nil, 200, 1},
		{"result with no observed watermark", "GET", queryURL + "/" + queryJobID + "/result", "", nil, 200, 1},
		{"cancel", "DELETE", queryURL + "/" + queryJobID, "", nil, 202, 1},
		{"CSRF", "POST", queryURL, validQueryBody, map[string]string{"X-CSRF-Token": ""}, 403, 0},
		{"origin", "POST", queryURL, validQueryBody, map[string]string{"Origin": "https://other.invalid"}, 403, 0},
		{"malformed query", "POST", queryURL, `{"kind":"raw_sql"}`, nil, 400, 0},
		{"duplicate field", "POST", queryURL, `{"kind":"trend","kind":"events"}`, nil, 400, 0},
		{"wrong journey kind", "POST", "/api/v1/projects/" + projectID + "/journeys/query", validQueryBody, nil, 400, 0},
		{"unscoped cursor", "GET", queryURL + "/" + queryJobID + "/result?cursor=other-scope", "", nil, 400, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fake := &analysisFake{}
			access := &catalogAccess{fakeAccess: &fakeAccess{}}
			handler := analysisHandler(t, fake, access)
			catalogStatus(t, catalogRequest(handler, tc.method, tc.path, tc.body, tc.headers), tc.status)
			if fake.calls != tc.calls {
				t.Fatal(fake.calls, tc.calls)
			}
		})
	}
	fake := &analysisFake{}
	access := &catalogAccess{fakeAccess: &fakeAccess{}, deny: fault.New(fault.NotFound, "PROJECT_NOT_FOUND", "项目不可见")}
	catalogStatus(t, catalogRequest(analysisHandler(t, fake, access), "POST", queryURL, validQueryBody, nil), 404)
	if fake.calls != 0 {
		t.Fatal("denied project reached query service")
	}
}
