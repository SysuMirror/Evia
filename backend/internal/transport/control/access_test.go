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

	"github.com/howtomakeaname/Evia/backend/internal/access"
	"github.com/howtomakeaname/Evia/backend/internal/platform/contract"
	"github.com/howtomakeaname/Evia/backend/internal/platform/cursor"
	"github.com/howtomakeaname/Evia/backend/internal/platform/fault"
	"github.com/howtomakeaname/Evia/backend/internal/platform/httpapi"
)

const userID = "11111111-1111-4111-8111-111111111111"
const projectID = "22222222-2222-4222-8222-222222222222"

type fakeAccess struct {
	AccessService
	created           int
	loggedOut         bool
	projects          []access.Project
	authErr, loginErr error
	projectErr        error
	projectCalls      int
	project           access.Project
	loginCalls        int
	prior             *access.Session
}

func (f *fakeAccess) Authenticate(_ context.Context, token string) (access.Session, error) {
	if f.authErr != nil {
		return access.Session{}, f.authErr
	}
	if token != "valid" {
		return access.Session{}, fault.New(fault.Unauthenticated, "SESSION_EXPIRED", "请登录")
	}
	return access.Session{UserID: userID, DisplayName: "Example", CSRF: "csrf", SecretHash: "prior-hash"}, nil
}
func (f *fakeAccess) Login(_ context.Context, _, _, _ string, prior *access.Session) (access.Session, string, error) {
	f.loginCalls++
	f.prior = prior
	if f.loginErr != nil {
		return access.Session{}, "", f.loginErr
	}
	return access.Session{UserID: userID, DisplayName: "Example", CSRF: "csrf"}, "valid", nil
}

func TestLoginAuthenticatesPriorBeforeReplacement(t *testing.T) {
	for _, tc := range []struct {
		name, cookie       string
		authErr, loginErr  error
		duplicate, rotated bool
		status, calls      int
	}{
		{name: "first login", status: 200, calls: 1},
		{name: "expired prior", cookie: "expired", status: 200, calls: 1},
		{name: "valid prior", cookie: "valid", rotated: true, status: 200, calls: 1},
		{name: "prior storage failure", cookie: "valid", authErr: errors.New("database unavailable"), status: 500},
		{name: "replacement failure", cookie: "valid", rotated: true, loginErr: errors.New("rotation transaction failed"), status: 500, calls: 1},
		{name: "concurrent revocation", cookie: "valid", rotated: true, loginErr: fault.New(fault.Unauthenticated, "SESSION_EXPIRED", "请登录"), status: 401, calls: 1},
		{name: "ambiguous cookie", cookie: "valid", duplicate: true, status: 401},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := &fakeAccess{authErr: tc.authErr, loginErr: tc.loginErr}
			r := httptest.NewRequest("POST", "/api/v1/session/login", strings.NewReader(`{"username":"example","password":"example"}`))
			r.Header.Set("Content-Type", "application/json")
			if tc.cookie != "" {
				r.AddCookie(&http.Cookie{Name: "evia_session", Value: tc.cookie})
			}
			if tc.duplicate {
				r.AddCookie(&http.Cookie{Name: "evia_session", Value: "second"})
			}
			w := httptest.NewRecorder()
			api(t, f).ServeHTTP(w, r)
			if w.Code != tc.status || f.loginCalls != tc.calls {
				t.Fatal(w.Code, f.loginCalls, w.Body.String())
			}
			if (f.prior != nil) != tc.rotated {
				t.Fatal("wrong prior credential passed to login")
			}
			if f.prior != nil && f.prior.SecretHash != "prior-hash" {
				t.Fatal("prior storage credential missing")
			}
			if tc.status >= 400 && len(w.Result().Cookies()) != 0 {
				t.Fatal("failed login issued a cookie")
			}
		})
	}
}
func (f *fakeAccess) Logout(context.Context, access.Session) error { f.loggedOut = true; return nil }
func (f *fakeAccess) CreateProject(_ context.Context, id, name, zone string) (access.Project, error) {
	if id != userID {
		panic("wrong principal")
	}
	f.created++
	return access.Project{ID: projectID, Name: name, Timezone: zone, Role: "owner"}, nil
}
func (f *fakeAccess) ListProjects(_ context.Context, id, after string, limit int) ([]access.Project, error) {
	if id != userID {
		panic("wrong principal")
	}
	return f.projects, nil
}
func api(t *testing.T, f *fakeAccess) http.Handler {
	t.Helper()
	v, e := contract.New()
	if e != nil {
		t.Fatal(e)
	}
	c, e := cursor.New([]byte(strings.Repeat("s", 32)))
	if e != nil {
		t.Fatal(e)
	}
	return (&API{Boundary: &httpapi.Boundary{Validator: v, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))}, Access: f, Cursor: c, Origin: "https://evia.example", SecureCookies: true}).Handler()
}
func TestMutationSecurityAndContract(t *testing.T) {
	f := &fakeAccess{}
	h := api(t, f)
	for _, tc := range []struct {
		name, token, csrf, origin, body string
		status                          int
	}{
		{"anonymous", "", "csrf", "", `{"name":"Forum","timezone":"UTC"}`, 401},
		{"expired", "bad", "csrf", "", `{"name":"Forum","timezone":"UTC"}`, 401},
		{"csrf", "valid", "", "", `{"name":"Forum","timezone":"UTC"}`, 403},
		{"origin", "valid", "csrf", "https://foreign.example", `{"name":"Forum","timezone":"UTC"}`, 403},
		{"extra", "valid", "csrf", "", `{"name":"Forum","timezone":"UTC","owner":"someone"}`, 400},
		{"success", "valid", "csrf", "https://evia.example", `{"name":"Forum","timezone":"UTC"}`, 201},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest("POST", "/api/v1/projects", strings.NewReader(tc.body))
			r.Header.Set("Content-Type", "application/json")
			r.Header.Set("X-CSRF-Token", tc.csrf)
			r.Header.Set("Origin", tc.origin)
			if tc.token != "" {
				r.AddCookie(&http.Cookie{Name: "evia_session", Value: tc.token})
			}
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != tc.status {
				t.Fatal(w.Code, w.Body.String())
			}
		})
	}
	if f.created != 1 {
		t.Fatal("unauthorized side effect", f.created)
	}
}
func TestSessionCookieAndLogout(t *testing.T) {
	f := &fakeAccess{}
	h := api(t, f)
	r := httptest.NewRequest("POST", "/api/v1/session/login", strings.NewReader(`{"username":"example","password":"example"}`))
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	cookies := w.Result().Cookies()
	if len(cookies) != 1 || !cookies[0].HttpOnly || !cookies[0].Secure || cookies[0].SameSite != http.SameSiteStrictMode || cookies[0].Path != "/" {
		t.Fatal("unsafe cookie", cookies)
	}
	r = httptest.NewRequest("DELETE", "/api/v1/session", nil)
	r.AddCookie(cookies[0])
	r.Header.Set("X-CSRF-Token", "csrf")
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 204 || !f.loggedOut || w.Result().Cookies()[0].MaxAge != -1 {
		t.Fatal(w.Code, "session not cleared")
	}
}
func TestPaginationAndStructuredRoutingErrors(t *testing.T) {
	f := &fakeAccess{projects: []access.Project{{ID: projectID, Name: "A", Timezone: "UTC", Role: "viewer"}, {ID: userID, Name: "B", Timezone: "UTC", Role: "owner"}}}
	h := api(t, f)
	for _, tc := range []struct {
		method, path string
		status       int
	}{{"GET", "/api/v1/projects?limit=1", 200}, {"GET", "/api/v1/projects?limit=1&limit=2", 400}, {"GET", "/api/v1/projects?limit=%zz", 400}, {"GET", "/api/v1/projects?limit=1;limit=2", 400}, {"GET", "/api/v1/projects?unexpected=%zz", 400}, {"GET", "/api/v1/projects?cursor=invalid", 400}, {"GET", "/api/v1/projects?environment=production", 400}, {"PUT", "/api/v1/projects", 405}, {"GET", "/api/v1/missing", 404}} {
		r := httptest.NewRequest(tc.method, tc.path, nil)
		r.AddCookie(&http.Cookie{Name: "evia_session", Value: "valid"})
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != tc.status {
			t.Fatal(tc, w.Code, w.Body.String())
		}
		var body map[string]any
		if e := json.Unmarshal(w.Body.Bytes(), &body); e != nil {
			t.Fatal(e)
		}
		if tc.status == 200 {
			if body["next_cursor"] == nil || len(body["items"].([]any)) != 1 {
				t.Fatal("pagination missing", body)
			}
		} else if body["request_id"] == nil {
			t.Fatal("unstructured failure", body)
		}
	}
}

func (f *fakeAccess) GetProject(_ context.Context, actor, project string) (access.Project, error) {
	f.projectCalls++
	if actor != userID || project != projectID {
		return access.Project{}, errors.New("incorrect project scope")
	}
	return f.project, f.projectErr
}
func TestGetProjectHTTPPermissionsAndBoundaries(t *testing.T) {
	const base = "/api/v1/projects/" + projectID
	for _, tc := range []struct {
		name, path, role string
		err              error
		status, calls    int
	}{
		{name: "owner", path: base, role: "owner", status: 200, calls: 1},
		{name: "editor", path: base, role: "editor", status: 200, calls: 1},
		{name: "viewer", path: base, role: "viewer", status: 200, calls: 1},
		{name: "invalid id", path: "/api/v1/projects/invalid", status: 400},
		{name: "unknown query", path: base + "?cursor=abc", status: 400},
		{name: "malformed query", path: base + "?invalid=%zz", status: 400},
		{name: "invisible", path: base, err: fault.New(fault.NotFound, "PROJECT_NOT_FOUND", "不可见"), status: 404, calls: 1},
		{name: "dependency", path: base, err: errors.New("private database failure"), status: 500, calls: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := &fakeAccess{project: access.Project{ID: projectID, Name: "Example", Timezone: "UTC", Role: tc.role}, projectErr: tc.err}
			out := catalogStatus(t, catalogRequest(api(t, f), "GET", tc.path, "", nil), tc.status)
			if f.projectCalls != tc.calls {
				t.Fatal("wrong read count", f.projectCalls)
			}
			if tc.status == 200 {
				permissions := out["permissions"].([]any)
				expected := 5
				if tc.role == "owner" {
					expected = 16
				} else if tc.role == "editor" {
					expected = 9
				}
				if len(permissions) != expected || out["id"] != projectID {
					t.Fatal(out)
				}
			}
		})
	}
	f := &fakeAccess{}
	catalogStatus(t, publicConfigRequest(api(t, f), "GET", base, nil), 401)
	if f.projectCalls != 0 {
		t.Fatal("unauthenticated project read")
	}
}

func TestProjectContentReadPermissionIsOwnerOnly(t *testing.T) {
	for _, role := range []string{"owner", "editor", "viewer"} {
		has := false
		for _, permission := range projectWire(access.Project{Role: role}).Permissions {
			has = has || permission == access.PermissionContentRead
		}
		if has != (role == "owner") {
			t.Fatalf("%s has incorrect content.read policy", role)
		}
	}
}

func TestProjectMetricPermissionsMatchHumanRolePolicy(t *testing.T) {
	for _, role := range []string{"owner", "editor", "viewer"} {
		has := map[string]bool{}
		for _, permission := range projectWire(access.Project{Role: role}).Permissions {
			has[permission] = true
		}
		for _, permission := range []string{access.PermissionMetricsRead, access.PermissionMetricsRun} {
			if !has[permission] {
				t.Fatalf("%s cannot discover %s", role, permission)
			}
		}
		for _, permission := range []string{access.PermissionMetricsWrite, access.PermissionMetricsPublish} {
			if has[permission] != (role == "owner" || role == "editor") {
				t.Fatalf("%s has incorrect %s policy", role, permission)
			}
		}
	}
}
