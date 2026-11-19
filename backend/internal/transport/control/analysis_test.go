package control

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/howtomakeaname/Evia/backend/internal/analysis"
	"github.com/howtomakeaname/Evia/backend/internal/platform/contract"
	"github.com/howtomakeaname/Evia/backend/internal/platform/cursor"
	"github.com/howtomakeaname/Evia/backend/internal/platform/fault"
	"github.com/howtomakeaname/Evia/backend/internal/platform/httpapi"
)

const queryJobID = "44444444-4444-4444-8444-444444444444"
const queryURL = "/api/v1/projects/" + projectID + "/queries"
const validQueryBody = `{"kind":"trend","environment":"development","time_range":{"from":"2026-11-07T10:00:00Z","to":"2026-11-07T11:00:00Z"},"timezone":"UTC"}`

type analysisFake struct {
	calls    int
	err      error
	rowCount int
}

func (f *analysisFake) job(actor, project string) (analysis.Job, error) {
	if actor != userID || project != projectID {
		panic("query escaped actor/project scope")
	}
	f.calls++
	return analysis.Job{ID: queryJobID, State: analysis.JobStateQueued, CreatedAt: time.Date(2026, 11, 7, 10, 0, 0, 0, time.UTC)}, f.err
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
	rows := make([]map[string]any, 0, f.rowCount)
	for i := 0; i < f.rowCount; i++ {
		rows = append(rows, map[string]any{"seq": i})
	}
	return analysis.Result{QueryID: id, Columns: []analysis.Column{{Key: "seq", Type: "integer"}}, Rows: rows, Metadata: analysis.Metadata{Accuracy: analysis.AccuracyExact, SampleRate: 1, Coverage: analysis.Coverage{TimeRange: analysis.TimeRange{From: time.Date(2026, 11, 7, 10, 0, 0, 0, time.UTC), To: time.Date(2026, 11, 7, 11, 0, 0, 0, time.UTC)}, State: analysis.CoverageStateProvisional, KnownGaps: []string{"pipeline_completeness_not_established"}}, GeneratedAt: time.Date(2026, 11, 7, 11, 0, 0, 0, time.UTC)}}, e
}
func analysisHandler(t *testing.T, f *analysisFake, access *catalogAccess) http.Handler {
	t.Helper()
	validator, e := contract.New()
	if e != nil {
		t.Fatal(e)
	}
	codec, err := cursor.New([]byte(strings.Repeat("q", 32)))
	if err != nil {
		t.Fatal(err)
	}
	api := &API{Boundary: &httpapi.Boundary{Validator: validator, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))}, Access: access, Cursor: codec, Origin: "https://evia.example"}
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

func analysisResultCodec(t *testing.T) *cursor.Codec {
	t.Helper()
	codec, err := cursor.New([]byte(strings.Repeat("q", 32)))
	if err != nil {
		t.Fatal(err)
	}
	return codec
}

type queryResultPage struct {
	QueryID    string            `json:"query_id"`
	Columns    []analysis.Column `json:"columns"`
	Rows       []map[string]any  `json:"rows"`
	NextCursor *string           `json:"next_cursor"`
}

func TestAnalysisQueryResultPagination(t *testing.T) {
	const total = 5
	resultPath := queryURL + "/" + queryJobID + "/result"
	scope := "analysis_query_result:" + userID + ":" + projectID + ":" + queryJobID

	newHandler := func() (*analysisFake, http.Handler) {
		fake := &analysisFake{rowCount: total}
		access := &catalogAccess{fakeAccess: &fakeAccess{}}
		return fake, analysisHandler(t, fake, access)
	}
	t.Run("legacy full snapshot stays byte compatible", func(t *testing.T) {
		fake, handler := newHandler()
		w := catalogRequest(handler, http.MethodGet, resultPath, "", nil)
		catalogStatus(t, w, http.StatusOK)
		var page queryResultPage
		if err := json.Unmarshal(w.Body.Bytes(), &page); err != nil {
			t.Fatal(err)
		}
		if len(page.Rows) != total || page.NextCursor != nil || fake.calls != 1 {
			t.Fatalf("legacy snapshot changed: rows=%d cursor=%v calls=%d", len(page.Rows), page.NextCursor, fake.calls)
		}
	})

	t.Run("walk every page and reconstruct the snapshot", func(t *testing.T) {
		_, handler := newHandler()
		var got []int
		path := resultPath + "?limit=2"
		pages := 0
		for {
			w := catalogRequest(handler, http.MethodGet, path, "", nil)
			catalogStatus(t, w, http.StatusOK)
			var page queryResultPage
			if err := json.Unmarshal(w.Body.Bytes(), &page); err != nil {
				t.Fatal(err)
			}
			if len(page.Columns) != 1 || page.QueryID != queryJobID {
				t.Fatal("page lost columns or query identity")
			}
			for _, row := range page.Rows {
				got = append(got, int(row["seq"].(float64)))
			}
			pages++
			if page.NextCursor == nil {
				break
			}
			if pages > total {
				t.Fatal("pagination did not terminate")
			}
			path = resultPath + "?cursor=" + url.QueryEscape(*page.NextCursor)
		}
		if pages != 3 || len(got) != total {
			t.Fatalf("pages=%d rows=%d", pages, len(got))
		}
		for i := 0; i < total; i++ {
			if got[i] != i {
				t.Fatalf("row order drifted at %d: %v", i, got)
			}
		}
	})

	t.Run("limit bounds are fail closed before reading the snapshot", func(t *testing.T) {
		for _, raw := range []string{"0", "201", "-1", "abc", "1.5", "2&limit=2"} {
			fake, handler := newHandler()
			w := catalogRequest(handler, http.MethodGet, resultPath+"?limit="+raw, "", nil)
			catalogStatus(t, w, http.StatusBadRequest)
			if fake.calls != 0 {
				t.Fatalf("limit=%s reached the snapshot store", raw)
			}
		}
		for _, raw := range []string{"1", "200"} {
			fake, handler := newHandler()
			w := catalogRequest(handler, http.MethodGet, resultPath+"?limit="+raw, "", nil)
			catalogStatus(t, w, http.StatusOK)
			if fake.calls != 1 {
				t.Fatalf("limit=%s calls=%d", raw, fake.calls)
			}
		}
	})

	t.Run("malformed and foreign cursors are rejected before reading", func(t *testing.T) {
		fake, handler := newHandler()
		catalogStatus(t, catalogRequest(handler, http.MethodGet, resultPath+"?cursor=garbage", "", nil), http.StatusBadRequest)
		if fake.calls != 0 {
			t.Fatal("garbage cursor reached the store")
		}

		_, first := newHandler()
		w := catalogRequest(first, http.MethodGet, resultPath+"?limit=2", "", nil)
		var page queryResultPage
		if err := json.Unmarshal(w.Body.Bytes(), &page); err != nil || page.NextCursor == nil {
			t.Fatal("first page missing cursor")
		}
		parts := strings.Split(*page.NextCursor, ".")
		if len(parts) != 2 {
			t.Fatal("cursor token shape changed")
		}
		// Flip the first signature character so the decoded HMAC necessarily changes.
		tampered := parts[0] + "." + flipBase64(parts[1][:1]) + parts[1][1:]
		_, tamperedHandler := newHandler()
		catalogStatus(t, catalogRequest(tamperedHandler, http.MethodGet, resultPath+"?cursor="+url.QueryEscape(tampered), "", nil), http.StatusBadRequest)

		codec := analysisResultCodec(t)
		foreign := codec.Encode("analysis_query_result:"+userID+":"+projectID+":00000000-0000-0000-0000-000000000000", `{"o":2,"l":2}`)
		_, foreignHandler := newHandler()
		catalogStatus(t, catalogRequest(foreignHandler, http.MethodGet, resultPath+"?cursor="+url.QueryEscape(foreign), "", nil), http.StatusBadRequest)
	})

	t.Run("page size must stay consistent with the first page", func(t *testing.T) {
		_, handler := newHandler()
		w := catalogRequest(handler, http.MethodGet, resultPath+"?limit=2", "", nil)
		var page queryResultPage
		if err := json.Unmarshal(w.Body.Bytes(), &page); err != nil || page.NextCursor == nil {
			t.Fatal("first page missing cursor")
		}
		fake, conflicting := newHandler()
		catalogStatus(t, catalogRequest(conflicting, http.MethodGet, resultPath+"?limit=3&cursor="+url.QueryEscape(*page.NextCursor), "", nil), http.StatusBadRequest)
		if fake.calls != 0 {
			t.Fatal("conflicting limit reached the store")
		}
		_, consistent := newHandler()
		catalogStatus(t, catalogRequest(consistent, http.MethodGet, resultPath+"?limit=2&cursor="+url.QueryEscape(*page.NextCursor), "", nil), http.StatusOK)
	})

	t.Run("terminal replay is idempotent and out of range offsets fail", func(t *testing.T) {
		codec := analysisResultCodec(t)
		terminal := codec.Encode(scope, `{"o":5,"l":2}`)
		_, handler := newHandler()
		w := catalogRequest(handler, http.MethodGet, resultPath+"?cursor="+url.QueryEscape(terminal), "", nil)
		catalogStatus(t, w, http.StatusOK)
		var page queryResultPage
		if err := json.Unmarshal(w.Body.Bytes(), &page); err != nil || len(page.Rows) != 0 || page.NextCursor != nil {
			t.Fatalf("terminal replay not idempotent: %+v", page)
		}

		for _, raw := range []string{`{"o":6,"l":2}`, `{"o":0,"l":0}`, `{"o":0,"l":2,"x":1}`} {
			token := codec.Encode(scope, raw)
			_, bad := newHandler()
			catalogStatus(t, catalogRequest(bad, http.MethodGet, resultPath+"?cursor="+url.QueryEscape(token), "", nil), http.StatusBadRequest)
		}
	})

	t.Run("other query routes reject continuation parameters", func(t *testing.T) {
		fake, handler := newHandler()
		catalogStatus(t, catalogRequest(handler, http.MethodGet, queryURL+"/"+queryJobID+"?limit=2", "", nil), http.StatusBadRequest)
		catalogStatus(t, catalogRequest(handler, http.MethodGet, resultPath+"?unknown=1", "", nil), http.StatusBadRequest)
		if fake.calls != 0 {
			t.Fatal("illegal parameters reached the query service")
		}
	})
}

func flipBase64(last string) string {
	if last == "A" {
		return "B"
	}
	return "A"
}
