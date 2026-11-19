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

	"github.com/howtomakeaname/Evia/backend/internal/ingestion"
	"github.com/howtomakeaname/Evia/backend/internal/platform/contract"
	"github.com/howtomakeaname/Evia/backend/internal/platform/cursor"
	"github.com/howtomakeaname/Evia/backend/internal/platform/fault"
	"github.com/howtomakeaname/Evia/backend/internal/platform/httpapi"
)

const sourceURL = "/api/v1/projects/" + projectID + "/sources"

type sourceServiceFake struct {
	SourceService
	calls            int
	err              error
	revision         int64
	key, environment string
}

func sourceResource() ingestion.Source {
	return ingestion.Source{ID: catalogReleaseID, ProjectID: projectID, Name: "Synthetic", Kind: ingestion.SourceKindServer, Environment: "development", WriteKey: "public-key", Revision: 1, Status: ingestion.SourceStatusActive, ApplicationState: ingestion.ApplicationStatePending, AllowedOrigins: []string{}}
}
func (f *sourceServiceFake) CreateSource(context.Context, string, string, ingestion.SourceInput) (ingestion.Issued, error) {
	f.calls++
	return ingestion.Issued{Source: sourceResource(), SecretOnce: "once-secret"}, f.err
}
func (f *sourceServiceFake) GetSource(context.Context, string, string, string) (ingestion.Source, error) {
	f.calls++
	return sourceResource(), f.err
}
func (f *sourceServiceFake) ListSources(_ context.Context, _, _, env, after string, limit int) ([]ingestion.Source, error) {
	f.calls++
	f.environment = env
	return []ingestion.Source{sourceResource()}, f.err
}
func (f *sourceServiceFake) UpdateSource(_ context.Context, _, _, _ string, revision int64, _ ingestion.SourceUpdate) (ingestion.Source, error) {
	f.calls++
	f.revision = revision
	return sourceResource(), f.err
}
func (f *sourceServiceFake) RotateSource(_ context.Context, _, _, _, key string, _ int64) (ingestion.Issued, error) {
	f.calls++
	f.key = key
	return ingestion.Issued{Source: sourceResource(), SecretOnce: "rotated-once"}, f.err
}
func sourceAPI(t *testing.T, s *sourceServiceFake, authorization *catalogAccess) http.Handler {
	t.Helper()
	v, e := contract.New()
	if e != nil {
		t.Fatal(e)
	}
	c, e := cursor.New([]byte(strings.Repeat("a", 32)))
	if e != nil {
		t.Fatal(e)
	}
	return (&API{Boundary: &httpapi.Boundary{Validator: v, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))}, Access: authorization, Sources: s, Cursor: c, Origin: "https://evia.example", SecureCookies: true}).Handler()
}
func TestSourceManagementAuthenticationAndWireSecrecy(t *testing.T) {
	for _, tc := range []struct {
		name, method, path, body, cookie, csrf, revision, key string
		status                                                int
		secret                                                bool
	}{
		{name: "anonymous public key", method: "GET", path: sourceURL, cookie: "public-key", status: 401},
		{name: "list", method: "GET", path: sourceURL + "?environment=development", cookie: "valid", status: 200},
		{name: "detail excludes secret", method: "GET", path: sourceURL + "/" + catalogReleaseID, cookie: "valid", status: 200},
		{name: "create", method: "POST", path: sourceURL, cookie: "valid", csrf: "csrf", body: `{"name":"Synthetic","kind":"server","environment":"development"}`, status: 201, secret: true},
		{name: "csrf", method: "POST", path: sourceURL, cookie: "valid", body: `{"name":"Synthetic","kind":"server","environment":"development"}`, status: 403},
		{name: "missing revision", method: "PATCH", path: sourceURL + "/" + catalogReleaseID, cookie: "valid", csrf: "csrf", body: `{"status":"disabled"}`, status: 400},
		{name: "patch", method: "PATCH", path: sourceURL + "/" + catalogReleaseID, cookie: "valid", csrf: "csrf", revision: `"1"`, body: `{"status":"disabled"}`, status: 200},
		{name: "invalid source scope field", method: "PATCH", path: sourceURL + "/" + catalogReleaseID, cookie: "valid", csrf: "csrf", revision: `"1"`, body: `{"environment":"production"}`, status: 400},
		{name: "rotation", method: "POST", path: sourceURL + "/" + catalogReleaseID + "/credential-rotations", cookie: "valid", csrf: "csrf", key: "rotation-key", body: `{"overlap_seconds":0}`, status: 201, secret: true},
		{name: "rotation missing key", method: "POST", path: sourceURL + "/" + catalogReleaseID + "/credential-rotations", cookie: "valid", csrf: "csrf", body: `{"overlap_seconds":0}`, status: 400},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := &sourceServiceFake{}
			h := sourceAPI(t, f, &catalogAccess{fakeAccess: &fakeAccess{}})
			r := httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.body))
			r.Header.Set("Content-Type", "application/json")
			r.Header.Set("X-CSRF-Token", tc.csrf)
			if tc.revision != "" {
				r.Header.Set("If-Match", tc.revision)
			}
			if tc.key != "" {
				r.Header.Set("Idempotency-Key", tc.key)
			}
			if tc.cookie != "" {
				r.AddCookie(&http.Cookie{Name: "evia_session", Value: tc.cookie})
			}
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != tc.status {
				t.Fatal(w.Code, w.Body.String())
			}
			var body map[string]any
			if e := json.Unmarshal(w.Body.Bytes(), &body); e != nil {
				t.Fatal(e)
			}
			_, secret := body["server_secret_once"]
			if secret != tc.secret {
				t.Fatal("server secret scope changed", body)
			}
			if tc.status >= 400 && f.calls != 0 {
				t.Fatal("rejected request called source service")
			}
		})
	}
}
func TestSourceManagementAuthorizationAndSafeFailures(t *testing.T) {
	for _, tc := range []struct {
		name              string
		authErr, storeErr error
		status            int
	}{
		{"invisible project", fault.New(fault.NotFound, "PROJECT_NOT_FOUND", "not visible"), nil, 404},
		{"viewer write", fault.New(fault.Forbidden, "PROJECT_FORBIDDEN", "forbidden"), nil, 403},
		{"stale write", nil, ingestion.RevisionConflict(), 409},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := &sourceServiceFake{err: tc.storeErr}
			r := httptest.NewRequest("PATCH", sourceURL+"/"+catalogReleaseID, strings.NewReader(`{"status":"disabled"}`))
			r.Header.Set("Content-Type", "application/json")
			r.Header.Set("If-Match", `"1"`)
			r.Header.Set("X-CSRF-Token", "csrf")
			r.AddCookie(&http.Cookie{Name: "evia_session", Value: "valid"})
			w := httptest.NewRecorder()
			sourceAPI(t, f, &catalogAccess{fakeAccess: &fakeAccess{}, deny: tc.authErr}).ServeHTTP(w, r)
			if w.Code != tc.status {
				t.Fatal(w.Code, w.Body.String())
			}
			if tc.authErr != nil && f.calls != 0 {
				t.Fatal("unauthorized service call")
			}
		})
	}
}
