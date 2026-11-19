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

	"github.com/howtomakeaname/Evia/backend/internal/gen/wire"
	"github.com/howtomakeaname/Evia/backend/internal/identity"
	"github.com/howtomakeaname/Evia/backend/internal/platform/contract"
	"github.com/howtomakeaname/Evia/backend/internal/platform/cursor"
	"github.com/howtomakeaname/Evia/backend/internal/platform/httpapi"
)

type fakeIdentityService struct {
	listFilter   identity.ReadFilter
	listEnv      string
	page         identity.Page
	revision     identity.Revision
	resolveCalls []string
	resolution   identity.Resolution
	err          error
}

func (s *fakeIdentityService) ListRevisions(_ context.Context, _, _, environment string, filter identity.ReadFilter) (identity.Page, error) {
	s.listFilter, s.listEnv = filter, environment
	return s.page, s.err
}
func (s *fakeIdentityService) GetRevision(_ context.Context, _, _, _ string, revision int64) (identity.Revision, error) {
	s.revision.Revision = revision
	return s.revision, s.err
}
func (s *fakeIdentityService) ResolveMember(_ context.Context, _, _, _ string, revision int64, kind, value string) (identity.Resolution, error) {
	s.resolveCalls = append(s.resolveCalls, kind, value)
	s.resolution.Revision = revision
	return s.resolution, s.err
}

func identityAPI(t *testing.T, svc IdentityService) (*API, http.Handler) {
	t.Helper()
	validator, err := contract.New()
	if err != nil {
		t.Fatal(err)
	}
	codec, err := cursor.New([]byte(strings.Repeat("i", 32)))
	if err != nil {
		t.Fatal(err)
	}
	a := &API{
		Boundary: &httpapi.Boundary{Validator: validator, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))},
		Access:   &fakeAccess{}, Cursor: codec, Origin: "https://evia.example",
		Identity: svc,
	}
	return a, a.Handler()
}

func identityHTTPRequest(handler http.Handler, method, path string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, nil)
	r.AddCookie(&http.Cookie{Name: "evia_session", Value: "valid"})
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	return w
}

func sampleIdentityRevisions() identity.Page {
	built := time.Date(2026, 11, 20, 4, 0, 0, 0, time.UTC)
	watermark := built.Add(-time.Minute)
	code := "IDENTITY_SCAN_FAILED"
	return identity.Page{
		Items: []identity.Revision{
			{Revision: 2, Watermark: &watermark, EdgeCount: 4, ActivePersonCount: 2, MemberCount: 6, State: identity.RevisionCompleted, BuiltAt: &built},
			{Revision: 1, EdgeCount: 0, ActivePersonCount: 0, MemberCount: 0, State: identity.RevisionFailed, BuiltAt: &built, ErrorCode: &code},
		},
		NextRevision: 1,
	}
}

func TestIdentityListMapsFilterAndPaginates(t *testing.T) {
	svc := &fakeIdentityService{page: sampleIdentityRevisions()}
	_, handler := identityAPI(t, svc)
	w := identityHTTPRequest(handler, http.MethodGet, "/api/v1/projects/"+projectID+"/identity-revisions?environment=development&limit=2")
	governanceStatus(t, w, http.StatusOK)
	var out wire.IdentityRevisionList
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil || len(out.Items) != 2 || out.NextCursor == nil {
		t.Fatalf("bad identity list response: %s", w.Body.String())
	}
	if out.Items[0].Revision != 2 || out.Items[0].State != identity.RevisionCompleted || out.Items[0].WatermarkReceivedAt == nil {
		t.Fatalf("completed revision mapping wrong: %+v", out.Items[0])
	}
	if out.Items[1].State != identity.RevisionFailed || out.Items[1].ErrorCode == nil {
		t.Fatalf("failed revision mapping wrong: %+v", out.Items[1])
	}
	if svc.listEnv != "development" || svc.listFilter.Limit != 2 {
		t.Fatalf("filter mapping wrong: %+v", svc.listFilter)
	}
	// The next cursor continues after the last served revision.
	path := "/api/v1/projects/" + projectID + "/identity-revisions?environment=development&cursor=" + *out.NextCursor
	w = identityHTTPRequest(handler, http.MethodGet, path)
	governanceStatus(t, w, http.StatusOK)
	if svc.listFilter.AfterRevision != 1 {
		t.Fatalf("cursor continuation wrong: %+v", svc.listFilter)
	}
}

func TestIdentityListRequiresKnownQueryParameters(t *testing.T) {
	_, handler := identityAPI(t, &fakeIdentityService{})
	w := identityHTTPRequest(handler, http.MethodGet, "/api/v1/projects/"+projectID+"/identity-revisions?environment=development&unknown=1")
	governanceStatus(t, w, http.StatusBadRequest)
	w = identityHTTPRequest(handler, http.MethodGet, "/api/v1/projects/"+projectID+"/identity-revisions")
	governanceStatus(t, w, http.StatusBadRequest)
}

func TestIdentityGetParsesRevision(t *testing.T) {
	svc := &fakeIdentityService{}
	svc.page = sampleIdentityRevisions()
	svc.revision = svc.page.Items[0]
	_, handler := identityAPI(t, svc)
	w := identityHTTPRequest(handler, http.MethodGet, "/api/v1/projects/"+projectID+"/identity-revisions/7?environment=staging")
	governanceStatus(t, w, http.StatusOK)
	var out wire.IdentityRevision
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil || out.Revision != 7 {
		t.Fatalf("bad revision response: %s", w.Body.String())
	}
	w = identityHTTPRequest(handler, http.MethodGet, "/api/v1/projects/"+projectID+"/identity-revisions/not-a-number?environment=staging")
	governanceStatus(t, w, http.StatusBadRequest)
	w = identityHTTPRequest(handler, http.MethodGet, "/api/v1/projects/"+projectID+"/identity-revisions/0?environment=staging")
	governanceStatus(t, w, http.StatusBadRequest)
}

func TestIdentityResolveAcceptsExactlyOneIdentity(t *testing.T) {
	svc := &fakeIdentityService{resolution: identity.Resolution{
		Revision:         3,
		ResolvedPersonID: "88888888-8888-4888-8888-888888888888",
		Members: []identity.Member{
			{ResolvedPersonID: "88888888-8888-4888-8888-888888888888", Kind: identity.MemberUID, Value: "user-1", FirstSeenAt: time.Date(2026, 11, 20, 3, 0, 0, 0, time.UTC), JoinedRevision: 1},
		},
	}}
	_, handler := identityAPI(t, svc)
	w := identityHTTPRequest(handler, http.MethodGet, "/api/v1/projects/"+projectID+"/identity-revisions/3/resolve?environment=development&uid=user-1")
	governanceStatus(t, w, http.StatusOK)
	var out wire.IdentityResolution
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil || out.Revision != 3 || len(out.Members) != 1 {
		t.Fatalf("bad resolution response: %s", w.Body.String())
	}
	if len(svc.resolveCalls) != 2 || svc.resolveCalls[0] != identity.MemberUID || svc.resolveCalls[1] != "user-1" {
		t.Fatalf("resolve mapping wrong: %v", svc.resolveCalls)
	}
	// Both or neither identity query parameters are rejected.
	w = identityHTTPRequest(handler, http.MethodGet, "/api/v1/projects/"+projectID+"/identity-revisions/3/resolve?environment=development&uid=user-1&anonymous_id=11111111-1111-4111-8111-111111111111")
	governanceStatus(t, w, http.StatusBadRequest)
	w = identityHTTPRequest(handler, http.MethodGet, "/api/v1/projects/"+projectID+"/identity-revisions/3/resolve?environment=development")
	governanceStatus(t, w, http.StatusBadRequest)
	w = identityHTTPRequest(handler, http.MethodGet, "/api/v1/projects/"+projectID+"/identity-revisions/3/resolve?environment=development&anonymous_id=not-a-uuid")
	governanceStatus(t, w, http.StatusBadRequest)
}

func TestIdentityRoutesRequireAuthentication(t *testing.T) {
	_, handler := identityAPI(t, &fakeIdentityService{})
	r := httptest.NewRequest(http.MethodGet, "/api/v1/projects/"+projectID+"/identity-revisions?environment=development", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	governanceStatus(t, w, http.StatusUnauthorized)
}
