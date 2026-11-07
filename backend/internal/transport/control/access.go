// Package control connects application services to the versioned management API.
package control

import (
	"context"
	"crypto/subtle"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/howtomakeaname/Evia/backend/internal/access"
	"github.com/howtomakeaname/Evia/backend/internal/gen/wire"
	"github.com/howtomakeaname/Evia/backend/internal/platform/cursor"
	"github.com/howtomakeaname/Evia/backend/internal/platform/fault"
	"github.com/howtomakeaname/Evia/backend/internal/platform/httpapi"
)

type AccessService interface {
	Login(context.Context, string, string, string, *access.Session) (access.Session, string, error)
	Authenticate(context.Context, string) (access.Session, error)
	Logout(context.Context, access.Session) error
	CreateProject(context.Context, string, string, string) (access.Project, error)
	ListProjects(context.Context, string, string, int) ([]access.Project, error)
	Authorize(context.Context, string, string, bool) error
}
type API struct {
	Boundary      *httpapi.Boundary
	Access        AccessService
	Catalog       CatalogService
	Sources       SourceService
	Analysis      AnalysisService
	Delivery      DeliveryService
	Cursor        *cursor.Codec
	Origin        string
	SecureCookies bool
}
type authorizedEndpoint func(http.ResponseWriter, *http.Request, access.Session) error

func (a *API) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/v1/session/login", a.Boundary.Handler(a.login))
	mux.HandleFunc("GET /api/v1/session", a.auth(false, func(w http.ResponseWriter, r *http.Request, s access.Session) error {
		return a.Boundary.JSON(w, 200, "Session", sessionWire(s))
	}))
	mux.HandleFunc("DELETE /api/v1/session", a.auth(true, a.logout))
	mux.HandleFunc("GET /api/v1/projects", a.auth(false, a.listProjects))
	mux.HandleFunc("POST /api/v1/projects", a.auth(true, a.createProject))
	a.registerCatalog(mux)
	a.registerSources(mux)
	(&AnalysisAPI{API: a, Service: a.Analysis}).Register(mux)
	a.registerDelivery(mux)
	mux.HandleFunc("/", a.Boundary.Handler(func(w http.ResponseWriter, r *http.Request) error {
		// Preserve a structured 405 for known paths instead of ServeMux's text response.
		for _, method := range []string{"GET", "POST", "PATCH", "DELETE"} {
			probe := r.Clone(r.Context())
			probe.Method = method
			_, pattern := mux.Handler(probe)
			if pattern != "/" && pattern != "" {
				w.Header().Add("Allow", method)
			}
		}
		if w.Header().Get("Allow") != "" {
			return fault.New(fault.Invalid, "METHOD_NOT_ALLOWED", "此接口不支持该请求方法")
		}
		return fault.New(fault.NotFound, "ENDPOINT_NOT_FOUND", "接口不存在")
	}))
	return a.Boundary.Middleware(mux)
}
func (a *API) origin(r *http.Request) error {
	if r.Header.Get("Sec-Fetch-Site") == "cross-site" {
		return fault.New(fault.Forbidden, "ORIGIN_FORBIDDEN", "请求来源不被允许")
	}
	if origin := r.Header.Get("Origin"); origin != "" && origin != a.Origin {
		return fault.New(fault.Forbidden, "ORIGIN_FORBIDDEN", "请求来源不被允许")
	}
	return nil
}
func (a *API) auth(mutate bool, next authorizedEndpoint) http.HandlerFunc {
	return a.Boundary.Handler(func(w http.ResponseWriter, r *http.Request) error {
		if e := a.origin(r); e != nil {
			return e
		}
		cookies := r.CookiesNamed("evia_session")
		if len(cookies) != 1 {
			return fault.New(fault.Unauthenticated, "SESSION_REQUIRED", "请先登录")
		}
		session, e := a.Access.Authenticate(r.Context(), cookies[0].Value)
		if e != nil {
			return e
		}
		if mutate && (len(r.Header.Values("X-CSRF-Token")) != 1 || subtle.ConstantTimeCompare([]byte(r.Header.Get("X-CSRF-Token")), []byte(session.CSRF)) != 1) {
			return fault.New(fault.Forbidden, "CSRF_REQUIRED", "请求验证已失效，请刷新后重试")
		}
		return next(w, r, session)
	})
}
func (a *API) cookie(w http.ResponseWriter, secret string, maxAge int) {
	c := &http.Cookie{Name: "evia_session", Value: secret, Path: "/", HttpOnly: true, Secure: a.SecureCookies, SameSite: http.SameSiteStrictMode, MaxAge: maxAge}
	if maxAge < 0 {
		c.Expires = time.Unix(1, 0)
	}
	http.SetCookie(w, c)
}
func sessionWire(s access.Session) wire.Session {
	return wire.Session{UserID: s.UserID, DisplayName: s.DisplayName, CSRFToken: s.CSRF}
}
func (a *API) login(w http.ResponseWriter, r *http.Request) error {
	if e := a.origin(r); e != nil {
		return e
	}
	var in wire.LoginInput
	if e := a.Boundary.Decode(w, r, "LoginInput", &in); e != nil {
		return e
	}
	var prior *access.Session
	cookies := r.CookiesNamed("evia_session")
	if len(cookies) > 1 {
		return fault.New(fault.Unauthenticated, "SESSION_REQUIRED", "请先登录")
	}
	if len(cookies) == 1 {
		session, e := a.Access.Authenticate(r.Context(), cookies[0].Value)
		if e != nil && fault.Public(e).Kind != fault.Unauthenticated {
			return e
		}
		if e == nil {
			prior = &session
		}
	}
	ip, _, e := net.SplitHostPort(r.RemoteAddr)
	if e != nil {
		ip = r.RemoteAddr
	}
	// Proxy headers are intentionally ignored until trusted proxies are configured.
	session, secret, e := a.Access.Login(r.Context(), in.Username, in.Password, ip, prior)
	if e != nil {
		return e
	}
	a.cookie(w, secret, 8*60*60)
	return a.Boundary.JSON(w, 200, "Session", sessionWire(session))
}
func (a *API) logout(w http.ResponseWriter, r *http.Request, s access.Session) error {
	if e := a.Access.Logout(r.Context(), s); e != nil {
		return e
	}
	a.cookie(w, "", -1)
	w.WriteHeader(http.StatusNoContent)
	return nil
}
func projectWire(p access.Project) wire.Project {
	permissions := []string{"project.read"}
	if p.Role == "owner" || p.Role == "editor" {
		permissions = append(permissions, "project.write")
	}
	return wire.Project{ID: p.ID, Name: p.Name, Timezone: p.Timezone, Capabilities: []string{}, Permissions: permissions}
}
func (a *API) createProject(w http.ResponseWriter, r *http.Request, s access.Session) error {
	var in wire.ProjectInput
	if e := a.Boundary.Decode(w, r, "ProjectInput", &in); e != nil {
		return e
	}
	project, e := a.Access.CreateProject(r.Context(), s.UserID, in.Name, in.Timezone)
	if e != nil {
		return e
	}
	return a.Boundary.JSON(w, 201, "Project", projectWire(project))
}
func (a *API) listProjects(w http.ResponseWriter, r *http.Request, s access.Session) error {
	for key, values := range r.URL.Query() {
		if (key != "cursor" && key != "limit") || len(values) != 1 {
			return fault.New(fault.Invalid, "INVALID_QUERY", "查询参数不符合接口定义")
		}
	}
	limit, e := httpapi.Limit(r)
	if e != nil {
		return e
	}
	scope := "projects:" + s.UserID
	after, e := a.Cursor.Decode(scope, r.URL.Query().Get("cursor"))
	if e != nil {
		return fault.New(fault.Invalid, "INVALID_CURSOR", "分页游标已失效，请重新加载")
	}
	projects, e := a.Access.ListProjects(r.Context(), s.UserID, after, limit)
	if e != nil {
		return e
	}
	out := wire.ProjectList{Items: make([]wire.Project, 0)}
	if len(projects) > limit {
		next := a.Cursor.Encode(scope, projects[limit-1].ID)
		out.NextCursor = &next
		projects = projects[:limit]
	}
	for _, p := range projects {
		out.Items = append(out.Items, projectWire(p))
	}
	return a.Boundary.JSON(w, 200, "ProjectList", out)
}

func NormalizeOrigin(origin string) string { return strings.TrimSuffix(origin, "/") }
