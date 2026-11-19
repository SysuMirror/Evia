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

	"github.com/howtomakeaname/Evia/backend/internal/analysis"
	"github.com/howtomakeaname/Evia/backend/internal/platform/contract"
	"github.com/howtomakeaname/Evia/backend/internal/platform/cursor"
	"github.com/howtomakeaname/Evia/backend/internal/platform/httpapi"
	"github.com/howtomakeaname/Evia/backend/internal/reporting"
	"github.com/howtomakeaname/Evia/backend/internal/transport/control"
)

const runtimeSavedID = "44444444-4444-4444-8444-444444444444"

type runtimeSavedStore struct {
	runtimeAnalysisStore
	reads int
}

func (s *runtimeSavedStore) ListSavedAnalyses(_ context.Context, actor, project, environment, after string, limit int, archivedOnly bool, search string) ([]reporting.SavedAnalysis, error) {
	if actor != runtimeActor || project != runtimeProject || environment != "development" || after != "" || limit < 1 || archivedOnly || search != "" {
		panic("saved analysis configuration lost runtime scope")
	}
	s.reads++
	now := time.Date(2026, 11, 8, 10, 0, 0, 0, time.UTC)
	return []reporting.SavedAnalysis{{ID: runtimeSavedID, ProjectID: project, Environment: environment, Title: "Synthetic saved count", Kind: analysis.QueryKindTrend, DraftRevision: 1, PublishedVersion: 1, CreatedAt: now, UpdatedAt: now}}, nil
}

func (s *runtimeSavedStore) EnqueueSavedAnalysisVersion(ctx context.Context, actor, project, id string, version int64, key string, in reporting.SavedAnalysisExecute) (analysis.Job, error) {
	if actor != runtimeActor || project != runtimeProject || id != runtimeSavedID || version != 1 || key != "synthetic-saved-run" || in.Timezone != "UTC" {
		panic("saved execution lost immutable version or request scope")
	}
	return s.EnqueueQuery(ctx, actor, project, analysis.Request{})
}

func TestRuntimeSavedAnalysisConfigurationAndExecutionAvailability(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		t.Run(map[bool]string{false: "execution disabled", true: "execution enabled"}[enabled], func(t *testing.T) {
			validator, err := contract.New()
			if err != nil {
				t.Fatal(err)
			}
			codec, err := cursor.New([]byte(strings.Repeat("s", 32)))
			if err != nil {
				t.Fatal(err)
			}
			api := &control.API{Boundary: &httpapi.Boundary{Validator: validator, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))}, Access: runtimeAnalysisAccess{}, Cursor: codec, Origin: "https://evia.example"}
			store := &runtimeSavedStore{}
			engine := &runtimeAnalysisEngine{}
			var execution analysis.Engine
			if enabled {
				execution = engine
			}
			configureAnalysis(api, store, validator, execution)
			handler := api.Handler()
			call := func(method, path, body string, authenticated bool) *httptest.ResponseRecorder {
				t.Helper()
				r := httptest.NewRequest(method, "/api/v1/projects/"+runtimeProject+"/saved-analyses"+path, strings.NewReader(body))
				r.Header.Set("Content-Type", "application/json")
				if authenticated {
					r.AddCookie(&http.Cookie{Name: "evia_session", Value: "synthetic"})
					r.Header.Set("X-CSRF-Token", "synthetic-csrf")
					r.Header.Set("Idempotency-Key", "synthetic-saved-run")
				}
				w := httptest.NewRecorder()
				handler.ServeHTTP(w, r)
				return w
			}
			list := call("GET", "?environment=development", "", true)
			if list.Code != http.StatusOK || store.reads != 1 {
				t.Fatalf("configuration unavailable: status=%d body=%s", list.Code, list.Body.String())
			}
			if w := call("GET", "?environment=development", "", false); w.Code != http.StatusUnauthorized || store.reads != 1 {
				t.Fatal("unauthenticated configuration reached storage")
			}
			response := call("POST", "/"+runtimeSavedID+"/versions/1/queries", `{"time_range":{"from":"2026-11-08T10:00:00Z","to":"2026-11-08T11:00:00Z"},"timezone":"UTC"}`, true)
			want := http.StatusServiceUnavailable
			if enabled {
				want = http.StatusAccepted
			}
			if response.Code != want {
				t.Fatalf("execution availability status=%d want=%d body=%s", response.Code, want, response.Body.String())
			}
			if enabled {
				var job analysis.Job
				if err := json.Unmarshal(response.Body.Bytes(), &job); err != nil || job.State != analysis.JobStateQueued || store.jobs != 1 {
					t.Fatal("configured execution did not enter the existing job service", err)
				}
			} else if store.jobs != 0 {
				t.Fatal("disabled execution enqueued work")
			}
			if engine.calls != 0 {
				t.Fatal("HTTP request executed facts synchronously")
			}
		})
	}
}
