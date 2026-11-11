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
	"github.com/howtomakeaname/Evia/backend/internal/platform/contract"
	"github.com/howtomakeaname/Evia/backend/internal/platform/cursor"
	"github.com/howtomakeaname/Evia/backend/internal/platform/fault"
	"github.com/howtomakeaname/Evia/backend/internal/platform/httpapi"
	"github.com/howtomakeaname/Evia/backend/internal/retention"
)

type fakeRetentionService struct {
	policy     retention.Policy
	configured bool
	putInput   retention.Input
	expected   int64
	getEnv     string
	err        error
}

func (s *fakeRetentionService) Get(_ context.Context, _, _, environment string) (retention.Policy, bool, error) {
	s.getEnv = environment
	return s.policy, s.configured, s.err
}

func (s *fakeRetentionService) Put(_ context.Context, in retention.Input, expected int64) (retention.Policy, error) {
	s.putInput = in
	s.expected = expected
	if s.err != nil {
		return retention.Policy{}, s.err
	}
	return s.policy, nil
}

func retentionAPI(t *testing.T, svc RetentionService) (*API, http.Handler) {
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
		Retention: svc,
	}
	return a, a.Handler()
}

func retentionRequest(handler http.Handler, method, path, body, ifMatch string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.AddCookie(&http.Cookie{Name: "evia_session", Value: "valid"})
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("X-CSRF-Token", "csrf")
	r.Header.Set("Idempotency-Key", "retention-http-key")
	if ifMatch != "" {
		r.Header.Set("If-Match", ifMatch)
	}
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	return w
}

func TestRetentionGetUnconfiguredReportsZeroRevision(t *testing.T) {
	svc := &fakeRetentionService{configured: false}
	_, handler := retentionAPI(t, svc)
	w := retentionRequest(handler, http.MethodGet, "/api/v1/projects/"+projectID+"/retention-policy?environment=production", "", "")
	governanceStatus(t, w, http.StatusOK)
	var out wire.RetentionPolicy
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.RetentionDays != nil || out.Revision != 0 || out.CreatedAt != nil || svc.getEnv != "production" {
		t.Fatalf("unconfigured policy wrong: %+v", out)
	}
	if w.Header().Get("ETag") != `"0"` {
		t.Fatalf("ETag for unconfigured policy = %q", w.Header().Get("ETag"))
	}
}

func TestRetentionGetConfiguredMapsPolicy(t *testing.T) {
	svc := &fakeRetentionService{
		configured: true,
		policy: retention.Policy{
			ProjectID: projectID, Environment: "staging", RetentionDays: 180, Revision: 3,
			CreatedAt: "2026-11-01T00:00:00Z", UpdatedAt: "2026-11-10T00:00:00Z",
		},
	}
	_, handler := retentionAPI(t, svc)
	w := retentionRequest(handler, http.MethodGet, "/api/v1/projects/"+projectID+"/retention-policy?environment=staging", "", "")
	governanceStatus(t, w, http.StatusOK)
	var out wire.RetentionPolicy
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.RetentionDays == nil || *out.RetentionDays != 180 || out.Revision != 3 ||
		out.CreatedAt == nil || *out.CreatedAt != "2026-11-01T00:00:00Z" || w.Header().Get("ETag") != `"3"` {
		t.Fatalf("configured policy mapping wrong: %+v header=%v", out, w.Header())
	}
}

func TestRetentionPutCreateMapsInputAndAllowsZeroRevision(t *testing.T) {
	svc := &fakeRetentionService{policy: retention.Policy{
		ProjectID: projectID, Environment: "development", RetentionDays: 90, Revision: 1,
		CreatedAt: "2026-11-11T00:00:00Z", UpdatedAt: "2026-11-11T00:00:00Z",
	}}
	_, handler := retentionAPI(t, svc)
	body := `{"environment":"development","retention_days":90}`
	w := retentionRequest(handler, http.MethodPut, "/api/v1/projects/"+projectID+"/retention-policy", body, `"0"`)
	governanceStatus(t, w, http.StatusOK)
	if svc.expected != 0 || svc.putInput.RetentionDays != 90 || svc.putInput.Environment != "development" ||
		svc.putInput.IdempotencyKey != "retention-http-key" || len(svc.putInput.RequestHash) != 64 {
		t.Fatalf("service input mapping wrong: %+v expected=%d", svc.putInput, svc.expected)
	}
	var out wire.RetentionPolicy
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil || out.Revision != 1 || out.RetentionDays == nil {
		t.Fatalf("put response wrong: %s", w.Body.String())
	}
}

func TestRetentionPutRejectsMissingRevisionAndContractViolations(t *testing.T) {
	svc := &fakeRetentionService{}
	_, handler := retentionAPI(t, svc)
	// Missing If-Match is rejected before the body is decoded.
	w := retentionRequest(handler, http.MethodPut, "/api/v1/projects/"+projectID+"/retention-policy",
		`{"environment":"development","retention_days":90}`, "")
	governanceStatus(t, w, http.StatusBadRequest)
	if svc.putInput.IdempotencyKey != "" {
		t.Fatal("request without revision reached the service")
	}
	// The OpenAPI minimum/maximum is enforced at the boundary.
	for _, body := range []string{
		`{"environment":"development","retention_days":29}`,
		`{"environment":"development","retention_days":3651}`,
		`{"environment":"edge","retention_days":90}`,
		`{"environment":"development"}`,
	} {
		w = retentionRequest(handler, http.MethodPut, "/api/v1/projects/"+projectID+"/retention-policy", body, `"0"`)
		governanceStatus(t, w, http.StatusBadRequest)
	}
	// GET without the required environment query is invalid.
	w = retentionRequest(handler, http.MethodGet, "/api/v1/projects/"+projectID+"/retention-policy", "", "")
	governanceStatus(t, w, http.StatusBadRequest)
}

func TestRetentionPutPropagatesConflict(t *testing.T) {
	svc := &fakeRetentionService{err: fault.New(fault.Conflict, "RETENTION_REVISION_CONFLICT", "修订冲突")}
	_, handler := retentionAPI(t, svc)
	w := retentionRequest(handler, http.MethodPut, "/api/v1/projects/"+projectID+"/retention-policy",
		`{"environment":"production","retention_days":365}`, `"7"`)
	governanceStatus(t, w, http.StatusConflict)
}
