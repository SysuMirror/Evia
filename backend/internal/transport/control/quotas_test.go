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

	"github.com/howtomakeaname/Evia/backend/internal/gen/wire"
	"github.com/howtomakeaname/Evia/backend/internal/platform/budget"
	"github.com/howtomakeaname/Evia/backend/internal/platform/contract"
	"github.com/howtomakeaname/Evia/backend/internal/platform/cursor"
	"github.com/howtomakeaname/Evia/backend/internal/platform/fault"
	"github.com/howtomakeaname/Evia/backend/internal/platform/httpapi"
	"github.com/howtomakeaname/Evia/backend/internal/quotas"
)

type fakeQuotaService struct {
	policy     quotas.Policy
	configured bool
	replayed   bool
	putErr     error
	got        quotas.Input
}

func (f *fakeQuotaService) Get(_ context.Context, _, _, _ string) (quotas.Policy, bool, error) {
	return f.policy, f.configured, nil
}

func (f *fakeQuotaService) Put(_ context.Context, in quotas.Input, _ int64) (quotas.Policy, bool, error) {
	f.got = in
	return f.policy, f.replayed, f.putErr
}

type fakeQuotaRuntime struct {
	policies   []budget.ScopePolicy
	reset      bool
	applyErr   error
	gotScope   budget.Scope
	gotPolicy  budget.Policy
	applyCalls int
}

func (f *fakeQuotaRuntime) SnapshotPolicies(string) []budget.ScopePolicy { return f.policies }

func (f *fakeQuotaRuntime) ApplyOverride(_ context.Context, scope budget.Scope, policy budget.Policy) (bool, error) {
	f.applyCalls++
	f.gotScope, f.gotPolicy = scope, policy
	return f.reset, f.applyErr
}

func quotaAPIWithService(t *testing.T, svc QuotaService, runtime QuotaRuntime) http.Handler {
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
		Boundary:     &httpapi.Boundary{Validator: validator, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))},
		Access:       &fakeAccess{},
		Machines:     &fakeMachines{},
		Cursor:       codec,
		Origin:       "https://evia.example",
		Quotas:       svc,
		QuotaRuntime: runtime,
	}
	return a.Handler()
}

func quotaRequest(handler http.Handler, method, path string, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.AddCookie(&http.Cookie{Name: "evia_session", Value: "valid"})
	r.Header.Set("X-CSRF-Token", "csrf")
	r.Header.Set("Idempotency-Key", "idem-key")
	r.Header.Set("If-Match", `"0"`)
	if body != "" {
		r.Header.Set("Content-Type", "application/json")
	}
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	return w
}

func defaultRuntime() *fakeQuotaRuntime {
	return &fakeQuotaRuntime{
		reset: true,
		policies: []budget.ScopePolicy{
			{ProjectID: projectID, Environment: "development", Rate: 1000, Burst: 2000},
			{ProjectID: projectID, Environment: "staging", Rate: 1000, Burst: 2000},
			{ProjectID: projectID, Environment: "production", Rate: 1000, Burst: 2000},
		},
	}
}

func TestQuotaGetReturnsEffectiveDefaultWithoutOverride(t *testing.T) {
	handler := quotaAPIWithService(t, &fakeQuotaService{}, defaultRuntime())
	w := quotaRequest(handler, http.MethodGet, "/api/v1/projects/"+projectID+"/quotas?environment=production", "")
	governanceStatus(t, w, http.StatusOK)
	var out wire.CollectionQuota
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.Source != "default" || out.Override || out.Revision != 0 || out.RatePerSecond != 1000 || out.Burst != 2000 || out.CreatedAt != nil {
		t.Fatalf("default view wrong: %+v", out)
	}
	if w.Header().Get("ETag") != `"0"` {
		t.Fatalf("missing zero revision ETag: %q", w.Header().Get("ETag"))
	}
}

func TestQuotaGetReturnsPersistedOverride(t *testing.T) {
	svc := &fakeQuotaService{
		configured: true,
		policy: quotas.Policy{
			ProjectID: projectID, Environment: "production", Rate: 5, Burst: 60, Revision: 3,
			CreatedAt: "2026-11-12T10:00:00Z", UpdatedAt: "2026-11-12T11:00:00Z",
		},
	}
	handler := quotaAPIWithService(t, svc, defaultRuntime())
	w := quotaRequest(handler, http.MethodGet, "/api/v1/projects/"+projectID+"/quotas?environment=production", "")
	governanceStatus(t, w, http.StatusOK)
	var out wire.CollectionQuota
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.Source != "project_override" || !out.Override || out.Revision != 3 || out.RatePerSecond != 5 || out.Burst != 60 || out.CreatedAt == nil {
		t.Fatalf("override view wrong: %+v", out)
	}
}

func TestQuotaPatchAppliesOverrideAndMapsRuntimeStatus(t *testing.T) {
	runtime := defaultRuntime()
	svc := &fakeQuotaService{policy: quotas.Policy{
		ProjectID: projectID, Environment: "staging", Rate: 7.5, Burst: 90, Revision: 1,
		CreatedAt: "2026-11-12T10:00:00Z", UpdatedAt: "2026-11-12T10:00:00Z",
	}}
	handler := quotaAPIWithService(t, svc, runtime)
	body := `{"environment":"staging","rate_per_second":7.5,"burst":90}`
	w := quotaRequest(handler, http.MethodPatch, "/api/v1/projects/"+projectID+"/quotas", body)
	governanceStatus(t, w, http.StatusOK)
	var out wire.CollectionQuota
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.RuntimeStatus != "applied" || out.Revision != 1 || out.Burst != 90 {
		t.Fatalf("patch response wrong: %+v", out)
	}
	if runtime.applyCalls != 1 || runtime.gotScope.Environment != "staging" || runtime.gotPolicy.Burst != 90 {
		t.Fatalf("runtime apply wrong: %+v %+v", runtime.gotScope, runtime.gotPolicy)
	}
	if svc.got.IdempotencyKey != "idem-key" || len(svc.got.RequestHash) != 64 {
		t.Fatalf("service input missing ledger fields: %+v", svc.got)
	}
}

func TestQuotaPatchReportsPendingBucketReset(t *testing.T) {
	runtime := defaultRuntime()
	runtime.reset = false
	svc := &fakeQuotaService{policy: quotas.Policy{ProjectID: projectID, Environment: "production", Rate: 5, Burst: 60, Revision: 1}}
	handler := quotaAPIWithService(t, svc, runtime)
	w := quotaRequest(handler, http.MethodPatch, "/api/v1/projects/"+projectID+"/quotas", `{"environment":"production","rate_per_second":5,"burst":60}`)
	governanceStatus(t, w, http.StatusOK)
	var out wire.CollectionQuota
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.RuntimeStatus != "bucket_reset_pending" {
		t.Fatalf("expected pending runtime status, got %s", out.RuntimeStatus)
	}
}

func TestQuotaPatchRuntimeFailureIsUnavailable(t *testing.T) {
	runtime := defaultRuntime()
	runtime.applyErr = fault.New(fault.Unavailable, "COLLECTION_BUDGET_UNAVAILABLE", "redis down")
	svc := &fakeQuotaService{policy: quotas.Policy{ProjectID: projectID, Environment: "production", Rate: 5, Burst: 60, Revision: 1}}
	handler := quotaAPIWithService(t, svc, runtime)
	w := quotaRequest(handler, http.MethodPatch, "/api/v1/projects/"+projectID+"/quotas", `{"environment":"production","rate_per_second":5,"burst":60}`)
	governanceStatus(t, w, http.StatusServiceUnavailable)
}

func TestQuotaRejectsBadInputAndUnauthenticated(t *testing.T) {
	handler := quotaAPIWithService(t, &fakeQuotaService{}, defaultRuntime())
	for _, tc := range []struct {
		method, path, body string
	}{
		{http.MethodGet, "/api/v1/projects/" + projectID + "/quotas", ""},
		{http.MethodGet, "/api/v1/projects/not-a-uuid/quotas?environment=production", ""},
		{http.MethodGet, "/api/v1/projects/" + projectID + "/quotas?environment=prod", ""},
		{http.MethodPatch, "/api/v1/projects/" + projectID + "/quotas", `{"environment":"production","rate_per_second":0,"burst":60}`},
	} {
		if got := quotaRequest(handler, tc.method, tc.path, tc.body); got.Code != http.StatusBadRequest {
			t.Fatalf("%s %s expected 400 got %d: %s", tc.method, tc.path, got.Code, got.Body.String())
		}
	}
	r := httptest.NewRequest(http.MethodGet, "/api/v1/projects/"+projectID+"/quotas?environment=production", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", w.Code)
	}
}

func TestQuotaRouteAbsentWhenNotWired(t *testing.T) {
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
	w := quotaRequest(a.Handler(), http.MethodGet, "/api/v1/projects/"+projectID+"/quotas?environment=production", "")
	if w.Code != http.StatusNotFound {
		t.Fatalf("unwired quota API must not mount routes, got %d", w.Code)
	}
}

func TestQuotaPatchReplaySkipsRuntimeRepublish(t *testing.T) {
	runtime := defaultRuntime()
	svc := &fakeQuotaService{
		replayed: true,
		policy:   quotas.Policy{ProjectID: projectID, Environment: "production", Rate: 5, Burst: 60, Revision: 1},
	}
	handler := quotaAPIWithService(t, svc, runtime)
	w := quotaRequest(handler, http.MethodPatch, "/api/v1/projects/"+projectID+"/quotas", `{"environment":"production","rate_per_second":5,"burst":60}`)
	governanceStatus(t, w, http.StatusOK)
	if runtime.applyCalls != 0 {
		t.Fatalf("idempotent replay must not republish runtime policy, calls=%d", runtime.applyCalls)
	}
	var out wire.CollectionQuota
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.RuntimeStatus != "applied" {
		t.Fatalf("replay should report applied without touching runtime: %s", out.RuntimeStatus)
	}
}
