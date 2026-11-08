package main

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

	"github.com/howtomakeaname/Evia/backend/internal/access"
	"github.com/howtomakeaname/Evia/backend/internal/analysis"
	"github.com/howtomakeaname/Evia/backend/internal/platform/contract"
	"github.com/howtomakeaname/Evia/backend/internal/platform/cursor"
	"github.com/howtomakeaname/Evia/backend/internal/platform/httpapi"
	"github.com/howtomakeaname/Evia/backend/internal/reporting"
	"github.com/howtomakeaname/Evia/backend/internal/transport/control"
)

const runtimeProject = "22222222-2222-4222-8222-222222222222"
const runtimeActor = "11111111-1111-4111-8111-111111111111"
const runtimeQuery = `{"kind":"trend","environment":"development","time_range":{"from":"2026-11-07T10:00:00Z","to":"2026-11-07T11:00:00Z"},"timezone":"UTC"}`

type runtimeAnalysisAccess struct{ control.AccessService }

func (runtimeAnalysisAccess) Authenticate(context.Context, string) (access.Session, error) {
	return access.Session{UserID: runtimeActor, CSRF: "synthetic-csrf"}, nil
}
func (runtimeAnalysisAccess) Authorize(_ context.Context, actor, project string, _ bool) error {
	if actor != runtimeActor || project != runtimeProject {
		panic("runtime analysis authorization lost actor/project scope")
	}
	return nil
}

type runtimeAnalysisStore struct {
	analysis.Store
	reporting.SavedAnalysisStore
	jobs int
}

func (*runtimeAnalysisStore) AuthorizeAnalysisDiscovery(context.Context, string, string) error {
	return nil
}
func (*runtimeAnalysisStore) ListAnalysisCatalog(context.Context, string, string, string, string, int64, int) (reporting.CatalogSnapshot, error) {
	return reporting.CatalogSnapshot{Items: []reporting.PublishedDefinition{}}, nil
}
func (*runtimeAnalysisStore) ValidateAnalysisQuery(_ context.Context, _, _ string, r analysis.Request) (reporting.ResolvedValidation, error) {
	return reporting.ResolvedValidation{Request: r, Dependencies: []reporting.Dependency{}}, nil
}
func (s *runtimeAnalysisStore) EnqueueQuery(context.Context, string, string, analysis.Request) (analysis.Job, error) {
	s.jobs++
	delay := 1000
	return analysis.Job{ID: "33333333-3333-4333-8333-333333333333", State: "queued", CreatedAt: time.Now().UTC(), PollAfterMS: &delay}, nil
}

type runtimeAnalysisEngine struct{ calls int }

func (e *runtimeAnalysisEngine) Execute(context.Context, analysis.Scope, analysis.Request) (analysis.Result, error) {
	e.calls++
	return analysis.Result{}, nil
}

func TestRuntimeDiscoveryRoutesRemainAvailableWithoutExecution(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		t.Run(map[bool]string{false: "execution disabled", true: "execution enabled"}[enabled], func(t *testing.T) {
			v, e := contract.New()
			if e != nil {
				t.Fatal(e)
			}
			codec, e := cursor.New([]byte(strings.Repeat("s", 32)))
			if e != nil {
				t.Fatal(e)
			}
			api := &control.API{Boundary: &httpapi.Boundary{Validator: v, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))}, Access: runtimeAnalysisAccess{}, Cursor: codec, Origin: "https://evia.example"}
			store := &runtimeAnalysisStore{}
			engine := &runtimeAnalysisEngine{}
			var execution analysis.Engine
			if enabled {
				execution = engine
			}
			configureAnalysis(api, store, v, execution)
			h := api.Handler()
			call := func(method, path, body string, authenticated bool, status int) map[string]any {
				t.Helper()
				r := httptest.NewRequest(method, "/api/v1/projects/"+runtimeProject+path, strings.NewReader(body))
				r.Header.Set("Content-Type", "application/json")
				if authenticated {
					r.AddCookie(&http.Cookie{Name: "evia_session", Value: "synthetic"})
					r.Header.Set("X-CSRF-Token", "synthetic-csrf")
				}
				w := httptest.NewRecorder()
				h.ServeHTTP(w, r)
				if w.Code != status {
					t.Fatalf("%s %s status=%d want=%d body=%s", method, path, w.Code, status, w.Body.String())
				}
				var out map[string]any
				if e := json.Unmarshal(w.Body.Bytes(), &out); e != nil {
					t.Fatal(e)
				}
				return out
			}
			capabilities := call("GET", "/analysis-capabilities", "", true, 200)
			if capabilities["execution_available"] != enabled {
				t.Fatal("capabilities lost runtime engine availability", capabilities)
			}
			call("GET", "/analysis-catalog?environment=development", "", true, 200)
			validation := call("POST", "/query-validations", runtimeQuery, true, 200)
			if validation["valid"] != true || validation["execution_available"] != enabled || validation["execution_performed"] != false || store.jobs != 0 || engine.calls != 0 {
				t.Fatal("validation executed facts or changed runtime availability", validation, store.jobs, engine.calls)
			}
			call("GET", "/analysis-capabilities", "", false, 401)
			call("POST", "/query-validations", runtimeQuery, false, 401)
			status := http.StatusNotFound
			if enabled {
				status = http.StatusAccepted
			}
			call("POST", "/queries", runtimeQuery, true, status)
			if enabled && store.jobs != 1 || !enabled && store.jobs != 0 || engine.calls != 0 {
				t.Fatal("execution route did not follow runtime configuration", store.jobs, engine.calls)
			}
		})
	}
}
