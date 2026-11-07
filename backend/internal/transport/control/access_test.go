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
	created   int
	loggedOut bool
	projects  []access.Project
}

func (f *fakeAccess) Authenticate(_ context.Context, token string) (access.Session, error) {
	if token != "valid" {
		return access.Session{}, fault.New(fault.Unauthenticated, "SESSION_EXPIRED", "请登录")
	}
	return access.Session{UserID: userID, DisplayName: "Example", CSRF: "csrf"}, nil
}
func (f *fakeAccess) Login(context.Context, string, string, string) (access.Session, string, error) {
	return access.Session{UserID: userID, DisplayName: "Example", CSRF: "csrf"}, "valid", nil
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
	}{{"GET", "/api/v1/projects?limit=1", 200}, {"GET", "/api/v1/projects?limit=1&limit=2", 400}, {"GET", "/api/v1/projects?cursor=invalid", 400}, {"GET", "/api/v1/projects?environment=production", 400}, {"PUT", "/api/v1/projects", 405}, {"GET", "/api/v1/missing", 404}} {
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
