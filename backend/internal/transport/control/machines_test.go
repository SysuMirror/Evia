package control

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/howtomakeaname/Evia/backend/internal/access"
	"github.com/howtomakeaname/Evia/backend/internal/catalog"
	"github.com/howtomakeaname/Evia/backend/internal/platform/contract"
	"github.com/howtomakeaname/Evia/backend/internal/platform/cursor"
	"github.com/howtomakeaname/Evia/backend/internal/platform/fault"
	"github.com/howtomakeaname/Evia/backend/internal/platform/httpapi"
)

const machinePrincipalID = "44444444-4444-4444-8444-444444444444"
const machineCredentialID = "55555555-5555-4555-8555-555555555555"
const machineToken = "evia_m_synthetic_http_test_not_a_real_credential_00000"
const machineURL = "/api/v1/projects/" + projectID + "/machine-principals"

type fakeMachines struct {
	MachineService
	authCalls, calls int
	credentials      int
	key              string
	revision         int64
	in               access.MachineCreate
	rotation         access.MachineRotate
	replay           bool
	err              error
}

func machineFixture() access.MachineIssued {
	now := time.Date(2026, 11, 9, 0, 0, 0, 0, time.UTC)
	return access.MachineIssued{
		Principal:  access.MachinePrincipal{ID: machinePrincipalID, ProjectID: projectID, Environment: "development", Name: "Build agent", Permissions: []string{"project.read", "catalog.drafts.write"}, Revision: 1, CreatedAt: now},
		Credential: access.MachineCredential{ID: machineCredentialID, PrincipalID: machinePrincipalID, CreatedAt: now, ExpiresAt: now.Add(24 * time.Hour)}, SecretOnce: machineToken,
	}
}
func (f *fakeMachines) AuthenticateMachine(_ context.Context, token string) (access.Identity, error) {
	f.authCalls++
	if token != machineToken {
		return access.Identity{}, fault.New(fault.Unauthenticated, "MACHINE_CREDENTIAL_INVALID", "机器凭据无效或已失效")
	}
	m := machineFixture()
	return access.Identity{PrincipalID: m.Principal.ID, CredentialID: m.Credential.ID, ProjectID: m.Principal.ProjectID, Environment: m.Principal.Environment, Name: m.Principal.Name, Permissions: m.Principal.Permissions, ExpiresAt: m.Credential.ExpiresAt}, nil
}
func (f *fakeMachines) issue(actor, project string) (access.MachineIssued, error) {
	if actor != userID || project != projectID {
		panic("machine administration impersonated a human")
	}
	f.calls++
	m := machineFixture()
	if f.replay {
		m.SecretOnce = ""
	}
	return m, f.err
}
func (f *fakeMachines) CreatePrincipal(_ context.Context, actor, project, key string, in access.MachineCreate) (access.MachineIssued, error) {
	f.key, f.in = key, in
	return f.issue(actor, project)
}
func (f *fakeMachines) RotateCredential(_ context.Context, actor, project, principal string, revision int64, key string, in access.MachineRotate) (access.MachineIssued, error) {
	if principal != machinePrincipalID {
		panic("wrong machine principal")
	}
	f.key, f.revision, f.rotation = key, revision, in
	return f.issue(actor, project)
}
func (f *fakeMachines) GetPrincipal(_ context.Context, actor, project, principal string) (access.MachinePrincipalDetail, error) {
	m, e := f.issue(actor, project)
	credentials := []access.MachineCredential{m.Credential}
	for len(credentials) < f.credentials {
		credentials = append(credentials, m.Credential)
	}
	return access.MachinePrincipalDetail{Principal: m.Principal, Credentials: credentials}, e
}

func TestMachineCredentialDetailAtLifetimeLimit(t *testing.T) {
	f := &fakeMachines{credentials: 128}
	r := httptest.NewRequest("GET", machineURL+"/"+machinePrincipalID, nil)
	r.AddCookie(&http.Cookie{Name: "evia_session", Value: "valid"})
	w := httptest.NewRecorder()
	machinesAPI(t, f, nil).ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	var out struct {
		Credentials []json.RawMessage `json:"credentials"`
	}
	if e := json.Unmarshal(w.Body.Bytes(), &out); e != nil {
		t.Fatal(e)
	}
	if len(out.Credentials) != 128 || strings.Contains(w.Body.String(), machineToken) {
		t.Fatal("incomplete or unsafe credential metadata")
	}
}
func (f *fakeMachines) ListPrincipals(_ context.Context, actor, project, after string, limit int) ([]access.MachinePrincipal, error) {
	m, e := f.issue(actor, project)
	return []access.MachinePrincipal{m.Principal}, e
}
func (f *fakeMachines) RevokePrincipal(_ context.Context, actor, project, principal string, revision int64) (access.MachinePrincipal, error) {
	f.revision = revision
	m, e := f.issue(actor, project)
	now := time.Now().UTC()
	m.Principal.RevokedAt = &now
	m.Principal.Revision++
	return m.Principal, e
}
func (f *fakeMachines) RevokeCredential(ctx context.Context, actor, project, principal, credential string, revision int64) (access.MachinePrincipal, error) {
	if credential != machineCredentialID {
		panic("wrong credential")
	}
	return f.RevokePrincipal(ctx, actor, project, principal, revision)
}

type machineCatalog struct {
	CatalogService
	calls    int
	identity access.Identity
}

func (f *machineCatalog) CreateEvent(ctx context.Context, actor, project string, in catalog.EventInput) (catalog.Event, error) {
	identity, ok := access.IdentityFromContext(ctx)
	if !ok || actor != machinePrincipalID || project != projectID || identity.PrincipalID != actor || identity.CredentialID != machineCredentialID {
		panic("machine attribution lost")
	}
	f.calls++
	f.identity = identity
	return catalog.Event{TypeID: 1, Key: in.Key, DisplayName: in.DisplayName, PublishedVersions: []int64{}}, nil
}

func machinesAPI(t *testing.T, machines *fakeMachines, events *machineCatalog) http.Handler {
	t.Helper()
	validator, e := contract.New()
	if e != nil {
		t.Fatal(e)
	}
	codec, e := cursor.New([]byte(strings.Repeat("s", 32)))
	if e != nil {
		t.Fatal(e)
	}
	api := &API{Boundary: &httpapi.Boundary{Validator: validator, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))}, Access: &catalogAccess{fakeAccess: &fakeAccess{}}, Machines: machines, Cursor: codec, Origin: "https://evia.example"}
	if events != nil {
		api.Catalog = events
	}
	return api.Handler()
}

func TestMachineAuthenticationModes(t *testing.T) {
	for _, tc := range []struct {
		name, path, method, auth, cookie, origin, site string
		duplicate                                      bool
		status, authCalls                              int
	}{
		{name: "valid bearer", path: "/api/v1/machine-identity", auth: "Bearer " + machineToken, status: 200, authCalls: 1},
		{name: "case insensitive scheme", path: "/api/v1/machine-identity", auth: "bearer " + machineToken, status: 200, authCalls: 1},
		{name: "invalid bearer no fallback", path: "/api/v1/machine-identity", auth: "Bearer invalid", status: 401, authCalls: 1},
		{name: "collection key cannot manage", path: "/api/v1/machine-identity", auth: "Bearer public_write_key", status: 401, authCalls: 1},
		{name: "wrong scheme", path: "/api/v1/machine-identity", auth: "Basic " + machineToken, status: 401},
		{name: "ambiguous whitespace", path: "/api/v1/machine-identity", auth: "Bearer  " + machineToken, status: 401},
		{name: "duplicate bearer", path: "/api/v1/machine-identity", auth: "Bearer " + machineToken, duplicate: true, status: 401},
		{name: "cookie and bearer", path: "/api/v1/machine-identity", auth: "Bearer " + machineToken, cookie: "valid", status: 401},
		{name: "expired cookie and bearer", path: "/api/v1/machine-identity", auth: "Bearer " + machineToken, cookie: "expired", status: 401},
		{name: "cross origin", path: "/api/v1/machine-identity", auth: "Bearer " + machineToken, origin: "https://other.example", status: 403},
		{name: "cross site", path: "/api/v1/machine-identity", auth: "Bearer " + machineToken, site: "cross-site", status: 403},
		{name: "session cannot self discover machine", path: "/api/v1/machine-identity", cookie: "valid", status: 401},
		{name: "machine cannot enumerate projects", path: "/api/v1/projects", auth: "Bearer " + machineToken, status: 401},
		{name: "machine cannot administer machines", path: machineURL, auth: "Bearer " + machineToken, status: 401},
		{name: "machine cannot login", path: "/api/v1/session/login", method: "POST", auth: "Bearer " + machineToken, status: 401},
		{name: "unknown self query", path: "/api/v1/machine-identity?scope=all", auth: "Bearer " + machineToken, status: 400, authCalls: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := &fakeMachines{}
			method := tc.method
			if method == "" {
				method = "GET"
			}
			r := httptest.NewRequest(method, tc.path, nil)
			if tc.auth != "" {
				r.Header.Set("Authorization", tc.auth)
			}
			if tc.duplicate {
				r.Header.Add("Authorization", tc.auth)
			}
			if tc.cookie != "" {
				r.AddCookie(&http.Cookie{Name: "evia_session", Value: tc.cookie})
			}
			if tc.origin != "" {
				r.Header.Set("Origin", tc.origin)
			}
			if tc.site != "" {
				r.Header.Set("Sec-Fetch-Site", tc.site)
			}
			w := httptest.NewRecorder()
			machinesAPI(t, f, nil).ServeHTTP(w, r)
			if w.Code != tc.status || f.authCalls != tc.authCalls {
				t.Fatalf("status %d, auth calls %d: %s", w.Code, f.authCalls, w.Body.String())
			}
			if strings.Contains(w.Body.String(), machineToken) || len(w.Result().Cookies()) != 0 || w.Header().Get("Cache-Control") != "no-store" {
				t.Fatal("credential handling regression")
			}
		})
	}
}

func TestMachineMutationCarriesIndependentIdentity(t *testing.T) {
	f := &fakeMachines{}
	events := &machineCatalog{}
	h := machinesAPI(t, f, events)
	for _, project := range []string{projectID, catalogReleaseID} {
		r := httptest.NewRequest("POST", "/api/v1/projects/"+project+"/event-definitions", strings.NewReader(`{"event_key":"page_opened","display_name":"Page opened"}`))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Authorization", "Bearer "+machineToken)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		expected := 201
		if project != projectID {
			expected = 404
		}
		if w.Code != expected {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	if events.calls != 1 {
		t.Fatal("cross-project call reached service")
	}
}

func TestMachineCredentialAdministrationContract(t *testing.T) {
	create := `{"name":"Build agent","environment":"development","permissions":["project.read"],"expires_at":"2026-11-10T00:00:00Z"}`
	for _, tc := range []struct {
		name, method, path, body, revision string
		omitCSRF, omitKey, replay          bool
		err                                error
		status, calls                      int
	}{
		{name: "create", method: "POST", path: machineURL, body: create, status: 201, calls: 1},
		{name: "create explicit metric grants", method: "POST", path: machineURL, body: strings.Replace(create, `["project.read"]`, `["metrics.read","metrics.write","metrics.publish","metrics.run"]`, 1), status: 201, calls: 1},
		{name: "create data governance grants", method: "POST", path: machineURL, body: strings.Replace(create, `["project.read"]`, `["data.erasure","data.export"]`, 1), status: 201, calls: 1},
		{name: "lost create response retry", method: "POST", path: machineURL, body: create, replay: true, status: 201, calls: 1},
		{name: "missing csrf", method: "POST", path: machineURL, body: create, omitCSRF: true, status: 403},
		{name: "missing key", method: "POST", path: machineURL, body: create, omitKey: true, status: 400},
		{name: "unknown grant", method: "POST", path: machineURL, body: strings.Replace(create, "project.read", "all.*", 1), status: 400},
		{name: "duplicate field", method: "POST", path: machineURL, body: strings.Replace(create, `"name":`, `"name":"Other","name":`, 1), status: 400},
		{name: "not owner", method: "POST", path: machineURL, body: create, err: fault.New(fault.Forbidden, "MACHINE_OWNER_REQUIRED", "仅项目所有者可管理机器凭据"), status: 403, calls: 1},
		{name: "database failed", method: "POST", path: machineURL, body: create, err: errors.New(machineToken), status: 500, calls: 1},
		{name: "detail", method: "GET", path: machineURL + "/" + machinePrincipalID, status: 200, calls: 1},
		{name: "list", method: "GET", path: machineURL, status: 200, calls: 1},
		{name: "unknown list filter", method: "GET", path: machineURL + "?environment=development", status: 400},
		{name: "rotate", method: "POST", path: machineURL + "/" + machinePrincipalID + "/credential-rotations", body: `{"expires_at":"2026-11-10T00:00:00Z","overlap_seconds":0}`, revision: `"3"`, status: 201, calls: 1},
		{name: "rotate missing revision", method: "POST", path: machineURL + "/" + machinePrincipalID + "/credential-rotations", body: `{"expires_at":"2026-11-10T00:00:00Z","overlap_seconds":0}`, status: 400},
		{name: "revoke principal", method: "DELETE", path: machineURL + "/" + machinePrincipalID, revision: `"3"`, status: 200, calls: 1},
		{name: "revoke credential", method: "DELETE", path: machineURL + "/" + machinePrincipalID + "/credentials/" + machineCredentialID, revision: `"3"`, status: 200, calls: 1},
		{name: "bad credential id", method: "DELETE", path: machineURL + "/" + machinePrincipalID + "/credentials/not-id", revision: `"3"`, status: 400},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := &fakeMachines{replay: tc.replay, err: tc.err}
			r := httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.body))
			r.Header.Set("Content-Type", "application/json")
			r.AddCookie(&http.Cookie{Name: "evia_session", Value: "valid"})
			if !tc.omitCSRF {
				r.Header.Set("X-CSRF-Token", "csrf")
			}
			if !tc.omitKey {
				r.Header.Set("Idempotency-Key", "test-request")
			}
			if tc.revision != "" {
				r.Header.Set("If-Match", tc.revision)
			}
			w := httptest.NewRecorder()
			machinesAPI(t, f, nil).ServeHTTP(w, r)
			if w.Code != tc.status || f.calls != tc.calls {
				t.Fatal(w.Code, f.calls, w.Body.String())
			}
			if tc.status >= 400 || tc.replay || tc.method != "POST" {
				if strings.Contains(w.Body.String(), machineToken) {
					t.Fatal("secret unexpectedly returned")
				}
			}
			if tc.status == 201 {
				var out map[string]any
				if e := json.Unmarshal(w.Body.Bytes(), &out); e != nil {
					t.Fatal(e)
				}
				if out["secret_available"] != !tc.replay {
					t.Fatal("wrong secret availability")
				}
				_, has := out["secret_once"]
				if has == tc.replay {
					t.Fatal("secret replayed or omitted")
				}
				if w.Header().Get("ETag") == "" {
					t.Fatal("missing revision")
				}
			}
		})
	}
}
