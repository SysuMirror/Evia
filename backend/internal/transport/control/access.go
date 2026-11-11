// Package control connects application services to the versioned management API.
package control

import (
	"context"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/howtomakeaname/Evia/backend/internal/access"
	"github.com/howtomakeaname/Evia/backend/internal/gen/wire"
	"github.com/howtomakeaname/Evia/backend/internal/platform/cursor"
	"github.com/howtomakeaname/Evia/backend/internal/platform/fault"
	"github.com/howtomakeaname/Evia/backend/internal/platform/httpapi"
	"github.com/howtomakeaname/Evia/backend/internal/reporting"
)

type AccessService interface {
	Login(context.Context, string, string, string, *access.Session) (access.Session, string, error)
	Authenticate(context.Context, string) (access.Session, error)
	Logout(context.Context, access.Session) error
	CreateProject(context.Context, string, string, string) (access.Project, error)
	ListProjects(context.Context, string, string, int) ([]access.Project, error)
	GetProject(context.Context, string, string) (access.Project, error)
	Authorize(context.Context, string, string, bool) error
}
type API struct {
	Boundary        *httpapi.Boundary
	Access          AccessService
	Machines        MachineService
	Memberships     MembershipService
	Settings        SettingsService
	AuditLogs       AuditService
	Catalog         CatalogService
	Sources         SourceService
	Analysis        AnalysisService
	Discovery       reporting.DiscoveryService
	SavedAnalyses   reporting.SavedAnalysisService
	Metrics         reporting.MetricService
	FormulaMetrics  reporting.FormulaMetricService
	Dashboards      reporting.DashboardService
	Delivery        DeliveryService
	Erasure         ErasureService
	Retention       RetentionService
	RetentionSweeps RetentionSweepService
	Cursor          *cursor.Codec
	Origin          string
	SecureCookies   bool
}
type authorizedActor struct {
	ID      string
	Session *access.Session
}
type authorizedEndpoint func(http.ResponseWriter, *http.Request, authorizedActor) error

func (a *API) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/v1/session/login", a.Boundary.Handler(a.login))
	mux.HandleFunc("GET /api/v1/session", a.auth(false, func(w http.ResponseWriter, r *http.Request, actor authorizedActor) error {
		return a.Boundary.JSON(w, 200, "Session", sessionWire(*actor.Session))
	}))
	mux.HandleFunc("DELETE /api/v1/session", a.auth(true, a.logout))
	mux.HandleFunc("GET /api/v1/projects", a.auth(false, a.listProjects))
	mux.HandleFunc("POST /api/v1/projects", a.auth(true, a.createProject))
	mux.HandleFunc("GET /api/v1/projects/{project_id}", a.projectAuth(false, a.getProject))
	a.registerMachines(mux)
	a.registerGovernance(mux)
	a.registerCatalog(mux)
	a.registerSources(mux)
	(&AnalysisAPI{API: a, Service: a.Analysis}).Register(mux)
	(&DiscoveryAPI{API: a, Service: a.Discovery}).Register(mux)
	(&SavedAnalysesAPI{API: a, Service: a.SavedAnalyses}).Register(mux)
	(&MetricsAPI{API: a, Service: a.Metrics}).Register(mux)
	(&FormulaMetricsAPI{API: a, Service: a.FormulaMetrics}).Register(mux)
	(&DashboardsAPI{API: a, Service: a.Dashboards}).Register(mux)
	(&ErasureAPI{API: a, Service: a.Erasure}).Register(mux)
	(&RetentionAPI{API: a, Service: a.Retention, Sweeps: a.RetentionSweeps}).Register(mux)
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
	if len(r.Header.Values("Authorization")) != 0 {
		return fault.New(fault.Unauthenticated, "AUTHENTICATION_MODE_INVALID", "登录接口不接受 Bearer 凭据")
	}
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
func (a *API) logout(w http.ResponseWriter, r *http.Request, actor authorizedActor) error {
	if e := a.Access.Logout(r.Context(), *actor.Session); e != nil {
		return e
	}
	a.cookie(w, "", -1)
	w.WriteHeader(http.StatusNoContent)
	return nil
}
func projectWire(p access.Project) wire.Project {
	permissions := []string{"project.read", "settings.read", access.PermissionMetricsRead, access.PermissionMetricsRun}
	if p.Role == "owner" || p.Role == "editor" {
		permissions = append(permissions, "project.write", access.PermissionMetricsWrite, access.PermissionMetricsPublish)
	}
	if p.Role == "owner" {
		permissions = append(permissions, "machine_principals.manage", "settings.write", "members.manage", "audit.read", access.PermissionDataErasure)
	}
	return wire.Project{ID: p.ID, Name: p.Name, Timezone: p.Timezone, Capabilities: []string{}, Permissions: permissions}
}
func (a *API) createProject(w http.ResponseWriter, r *http.Request, s authorizedActor) error {
	var in wire.ProjectInput
	if e := a.Boundary.Decode(w, r, "ProjectInput", &in); e != nil {
		return e
	}
	project, e := a.Access.CreateProject(r.Context(), s.ID, in.Name, in.Timezone)
	if e != nil {
		return e
	}
	return a.Boundary.JSON(w, 201, "Project", projectWire(project))
}
func (a *API) listProjects(w http.ResponseWriter, r *http.Request, s authorizedActor) error {
	for key, values := range r.URL.Query() {
		if (key != "cursor" && key != "limit") || len(values) != 1 {
			return fault.New(fault.Invalid, "INVALID_QUERY", "查询参数不符合接口定义")
		}
	}
	limit, e := httpapi.Limit(r)
	if e != nil {
		return e
	}
	scope := "projects:" + s.ID
	after, e := a.Cursor.Decode(scope, r.URL.Query().Get("cursor"))
	if e != nil {
		return fault.New(fault.Invalid, "INVALID_CURSOR", "分页游标已失效，请重新加载")
	}
	projects, e := a.Access.ListProjects(r.Context(), s.ID, after, limit)
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

func (a *API) getProject(w http.ResponseWriter, r *http.Request, s authorizedActor) error {
	projectID := r.PathValue("project_id")
	if !catalogUUID.MatchString(projectID) {
		return fault.New(fault.Invalid, "INVALID_PROJECT_ID", "项目标识格式不正确")
	}
	if _, e := catalogQuery(r, false); e != nil {
		return e
	}
	project, e := a.Access.GetProject(r.Context(), s.ID, projectID)
	if e != nil {
		return e
	}
	out := projectWire(project)
	if identity, ok := access.IdentityFromContext(r.Context()); ok {
		out.Permissions = append([]string{}, identity.Permissions...)
	}
	return a.Boundary.JSON(w, 200, "Project", out)
}
