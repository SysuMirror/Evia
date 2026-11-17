package control

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/howtomakeaname/Evia/backend/internal/analysis"
	"github.com/howtomakeaname/Evia/backend/internal/platform/httpapi"
	"github.com/howtomakeaname/Evia/backend/internal/reporting"
)

// DashboardsAPI shares the management authentication and contract boundary.
// The service obtains immutable environment scope from the stored resource.
type DashboardsAPI struct {
	API     *API
	Service reporting.DashboardService
}

func (a *DashboardsAPI) Register(mux *http.ServeMux) {
	if a.Service == nil {
		return
	}
	const base = "/api/v1/projects/{project_id}/dashboards"
	mux.HandleFunc("GET "+base, a.API.projectAuth(false, a.list))
	mux.HandleFunc("POST "+base, a.API.projectAuth(true, a.create))
	mux.HandleFunc("GET "+base+"/{dashboard_id}", a.API.projectAuth(false, a.get))
	mux.HandleFunc("GET "+base+"/{dashboard_id}/draft", a.API.projectAuth(false, a.draft))
	mux.HandleFunc("PATCH "+base+"/{dashboard_id}/draft", a.API.projectAuth(true, a.save))
	mux.HandleFunc("POST "+base+"/{dashboard_id}/validations", a.API.projectAuth(true, a.validate))
	mux.HandleFunc("GET "+base+"/{dashboard_id}/versions", a.API.projectAuth(false, a.versions))
	mux.HandleFunc("POST "+base+"/{dashboard_id}/versions", a.API.projectAuth(true, a.publish))
	mux.HandleFunc("GET "+base+"/{dashboard_id}/versions/{version}", a.API.projectAuth(false, a.version))
	mux.HandleFunc("POST "+base+"/{dashboard_id}/versions/{version}/runs", a.API.projectAuth(true, a.execute))
	mux.HandleFunc("GET /api/v1/projects/{project_id}/dashboard-runs/{run_id}", a.API.projectAuth(false, a.run))
	mux.HandleFunc("DELETE /api/v1/projects/{project_id}/dashboard-runs/{run_id}", a.API.projectAuth(true, a.cancelRun))
	mux.HandleFunc("POST "+base+"/{dashboard_id}/archive", a.API.projectAuth(true, a.archive))
	mux.HandleFunc("POST "+base+"/{dashboard_id}/unarchive", a.API.projectAuth(true, a.unarchive))
	mux.HandleFunc("POST "+base+"/{dashboard_id}/copy", a.API.projectAuth(true, a.copy))
}

func (a *DashboardsAPI) scope(r *http.Request, s authorizedActor, write bool) (string, error) {
	if id := r.PathValue("dashboard_id"); id != "" && !catalogUUID.MatchString(id) {
		return "", analysis.Invalid("INVALID_DASHBOARD_ID", "看板标识无效")
	}
	if v := r.PathValue("version"); v != "" {
		if _, e := savedVersion(v); e != nil {
			return "", e
		}
	}
	if id := r.PathValue("run_id"); id != "" && !catalogUUID.MatchString(id) {
		return "", analysis.Invalid("INVALID_DASHBOARD_RUN_ID", "看板运行标识无效")
	}
	return a.API.catalogProject(r, s, write)
}
func (a *DashboardsAPI) create(w http.ResponseWriter, r *http.Request, s authorizedActor) error {
	p, e := a.scope(r, s, true)
	if e != nil {
		return e
	}
	if _, e = savedQuery(r, false, false); e != nil {
		return e
	}
	key, e := savedIdempotency(r)
	if e != nil {
		return e
	}
	var in reporting.DashboardCreate
	if e = a.API.Boundary.Decode(w, r, "DashboardCreate", &in); e != nil {
		return e
	}
	// Markdown safety rules are not OpenAPI-expressible; reject unsafe text
	// cards at the boundary so no service implementation can admit them.
	if e = reporting.ValidateDashboardTextCards(in.Definition); e != nil {
		return e
	}
	out, e := a.Service.CreateDashboard(r.Context(), s.ID, p, key, in)
	if e != nil {
		return e
	}
	return a.draftResponse(w, 201, out)
}
func (a *DashboardsAPI) get(w http.ResponseWriter, r *http.Request, s authorizedActor) error {
	p, e := a.scope(r, s, false)
	if e != nil {
		return e
	}
	if _, e = savedQuery(r, false, false); e != nil {
		return e
	}
	out, e := a.Service.GetDashboard(r.Context(), s.ID, p, r.PathValue("dashboard_id"))
	if e != nil {
		return e
	}
	return a.API.Boundary.JSON(w, 200, "Dashboard", out)
}
func (a *DashboardsAPI) draft(w http.ResponseWriter, r *http.Request, s authorizedActor) error {
	p, e := a.scope(r, s, false)
	if e != nil {
		return e
	}
	if _, e = savedQuery(r, false, false); e != nil {
		return e
	}
	out, e := a.Service.GetDashboardDraft(r.Context(), s.ID, p, r.PathValue("dashboard_id"))
	if e != nil {
		return e
	}
	return a.draftResponse(w, 200, out)
}
func (a *DashboardsAPI) draftResponse(w http.ResponseWriter, status int, out reporting.DashboardDraft) error {
	w.Header().Set("ETag", strconv.Quote(strconv.FormatInt(out.DraftRevision, 10)))
	return a.API.Boundary.JSON(w, status, "DashboardDraft", out)
}
func (a *DashboardsAPI) save(w http.ResponseWriter, r *http.Request, s authorizedActor) error {
	p, e := a.scope(r, s, true)
	if e != nil {
		return e
	}
	if _, e = savedQuery(r, false, false); e != nil {
		return e
	}
	if len(r.Header.Values("If-Match")) != 1 || !catalogETag.MatchString(r.Header.Get("If-Match")) {
		return analysis.Invalid("REVISION_REQUIRED", "请提供单个带双引号的 If-Match 版本")
	}
	revision, e := httpapi.IfMatch(r)
	if e != nil {
		return e
	}
	var in reporting.DashboardUpdate
	if e = a.API.Boundary.Decode(w, r, "DashboardUpdate", &in); e != nil {
		return e
	}
	if e = reporting.ValidateDashboardTextCards(in.Definition); e != nil {
		return e
	}
	out, e := a.Service.SaveDashboardDraft(r.Context(), s.ID, p, r.PathValue("dashboard_id"), revision, in.Definition, in.FolderID)
	if e != nil {
		return e
	}
	return a.draftResponse(w, 200, out)
}
func (a *DashboardsAPI) validate(w http.ResponseWriter, r *http.Request, s authorizedActor) error {
	p, e := a.scope(r, s, false)
	if e != nil {
		return e
	}
	if _, e = savedQuery(r, false, false); e != nil {
		return e
	}
	var in reporting.DashboardValidate
	if e = a.API.Boundary.Decode(w, r, "DashboardValidate", &in); e != nil {
		return e
	}
	out, e := a.Service.ValidateDashboard(r.Context(), s.ID, p, r.PathValue("dashboard_id"), in)
	if e != nil {
		return e
	}
	return a.API.Boundary.JSON(w, 200, "DashboardValidation", out)
}
func (a *DashboardsAPI) publish(w http.ResponseWriter, r *http.Request, s authorizedActor) error {
	p, e := a.scope(r, s, true)
	if e != nil {
		return e
	}
	if _, e = savedQuery(r, false, false); e != nil {
		return e
	}
	key, e := savedIdempotency(r)
	if e != nil {
		return e
	}
	var in reporting.DashboardPublish
	if e = a.API.Boundary.Decode(w, r, "DashboardPublish", &in); e != nil {
		return e
	}
	out, e := a.Service.PublishDashboard(r.Context(), s.ID, p, r.PathValue("dashboard_id"), key, in)
	if e != nil {
		return e
	}
	return a.API.Boundary.JSON(w, 201, "DashboardVersion", out)
}
func (a *DashboardsAPI) version(w http.ResponseWriter, r *http.Request, s authorizedActor) error {
	p, e := a.scope(r, s, false)
	if e != nil {
		return e
	}
	if _, e = savedQuery(r, false, false); e != nil {
		return e
	}
	version, _ := savedVersion(r.PathValue("version"))
	out, e := a.Service.GetDashboardVersion(r.Context(), s.ID, p, r.PathValue("dashboard_id"), version)
	if e != nil {
		return e
	}
	return a.API.Boundary.JSON(w, 200, "DashboardVersion", out)
}
func (a *DashboardsAPI) execute(w http.ResponseWriter, r *http.Request, s authorizedActor) error {
	p, e := a.scope(r, s, false)
	if e != nil {
		return e
	}
	if _, e = savedQuery(r, false, false); e != nil {
		return e
	}
	key, e := savedIdempotency(r)
	if e != nil {
		return e
	}
	var in reporting.DashboardExecute
	if e = a.API.Boundary.Decode(w, r, "DashboardExecute", &in); e != nil {
		return e
	}
	version, _ := savedVersion(r.PathValue("version"))
	out, e := a.Service.EnqueueDashboardRun(r.Context(), s.ID, p, r.PathValue("dashboard_id"), version, key, in)
	if e != nil {
		return e
	}
	return a.API.Boundary.JSON(w, 202, "DashboardRun", out)
}
func (a *DashboardsAPI) list(w http.ResponseWriter, r *http.Request, s authorizedActor) error {
	p, e := a.scope(r, s, false)
	if e != nil {
		return e
	}
	q, e := savedDashboardListQuery(r)
	if e != nil {
		return e
	}
	limit, e := savedLimit(q, 100)
	if e != nil {
		return e
	}
	view := q.Get("view")
	if view == "" {
		view = "active"
	}
	archivedOnly := view == "archived"
	folderScope, e := reporting.NormalizeDashboardFolderScope(q.Get("folder_id"))
	if e != nil {
		return e
	}
	search, e := reporting.NormalizeCatalogSearch(q.Get("q"))
	if e != nil {
		return e
	}
	// The cursor is bound to every list predicate, including the search term,
	// so a stale cursor from a different query is rejected by the codec.
	scope := strings.Join([]string{"dashboards", s.ID, p, q.Get("environment"), view, folderScope, search}, ":")
	after, e := a.API.Cursor.Decode(scope, q.Get("cursor"))
	if e != nil || after != "" && !catalogUUID.MatchString(after) {
		return analysis.Invalid("INVALID_CURSOR", "分页游标已失效，请重新加载", "/cursor")
	}
	items, e := a.Service.ListDashboards(r.Context(), s.ID, p, q.Get("environment"), after, limit, archivedOnly, folderScope, search)
	if e != nil {
		return e
	}
	out := struct {
		Items      []reporting.Dashboard `json:"items"`
		NextCursor *string               `json:"next_cursor"`
	}{Items: []reporting.Dashboard{}}
	if len(items) > limit {
		next := a.API.Cursor.Encode(scope, items[limit-1].ID)
		out.NextCursor = &next
		items = items[:limit]
	}
	out.Items = append(out.Items, items...)
	return a.API.Boundary.JSON(w, 200, "DashboardList", out)
}
func (a *DashboardsAPI) versions(w http.ResponseWriter, r *http.Request, s authorizedActor) error {
	p, e := a.scope(r, s, false)
	if e != nil {
		return e
	}
	q, e := savedQuery(r, true, false)
	if e != nil {
		return e
	}
	limit, e := savedLimit(q, 50)
	if e != nil {
		return e
	}
	id := r.PathValue("dashboard_id")
	scope := strings.Join([]string{"dashboard_versions", s.ID, p, id}, ":")
	position, e := a.API.Cursor.Decode(scope, q.Get("cursor"))
	if e != nil {
		return analysis.Invalid("INVALID_CURSOR", "分页游标已失效，请重新加载", "/cursor")
	}
	var after int64
	if position != "" {
		after, e = savedVersion(position)
		if e != nil {
			return analysis.Invalid("INVALID_CURSOR", "分页游标已失效，请重新加载", "/cursor")
		}
	}
	items, e := a.Service.ListDashboardVersions(r.Context(), s.ID, p, id, after, limit)
	if e != nil {
		return e
	}
	out := struct {
		Items      []reporting.DashboardVersion `json:"items"`
		NextCursor *string                      `json:"next_cursor"`
	}{Items: []reporting.DashboardVersion{}}
	if len(items) > limit {
		next := a.API.Cursor.Encode(scope, strconv.FormatInt(items[limit-1].Version, 10))
		out.NextCursor = &next
		items = items[:limit]
	}
	out.Items = append(out.Items, items...)
	return a.API.Boundary.JSON(w, 200, "DashboardVersionList", out)
}

func (a *DashboardsAPI) run(w http.ResponseWriter, r *http.Request, s authorizedActor) error {
	p, e := a.scope(r, s, false)
	if e != nil {
		return e
	}
	if _, e = savedQuery(r, false, false); e != nil {
		return e
	}
	out, e := a.Service.GetDashboardRun(r.Context(), s.ID, p, r.PathValue("run_id"))
	if e != nil {
		return e
	}
	return a.API.Boundary.JSON(w, 200, "DashboardRun", out)
}
func (a *DashboardsAPI) cancelRun(w http.ResponseWriter, r *http.Request, s authorizedActor) error {
	p, e := a.scope(r, s, false)
	if e != nil {
		return e
	}
	if _, e = savedQuery(r, false, false); e != nil {
		return e
	}
	out, e := a.Service.CancelDashboardRun(r.Context(), s.ID, p, r.PathValue("run_id"))
	if e != nil {
		return e
	}
	return a.API.Boundary.JSON(w, 200, "DashboardRun", out)
}

func (a *DashboardsAPI) archive(w http.ResponseWriter, r *http.Request, s authorizedActor) error {
	p, e := a.scope(r, s, true)
	if e != nil {
		return e
	}
	if _, e = savedQuery(r, false, false); e != nil {
		return e
	}
	out, e := a.Service.ArchiveDashboard(r.Context(), s.ID, p, r.PathValue("dashboard_id"))
	if e != nil {
		return e
	}
	return a.API.Boundary.JSON(w, 200, "Dashboard", out)
}

func (a *DashboardsAPI) unarchive(w http.ResponseWriter, r *http.Request, s authorizedActor) error {
	p, e := a.scope(r, s, true)
	if e != nil {
		return e
	}
	if _, e = savedQuery(r, false, false); e != nil {
		return e
	}
	out, e := a.Service.UnarchiveDashboard(r.Context(), s.ID, p, r.PathValue("dashboard_id"))
	if e != nil {
		return e
	}
	return a.API.Boundary.JSON(w, 200, "Dashboard", out)
}

func (a *DashboardsAPI) copy(w http.ResponseWriter, r *http.Request, s authorizedActor) error {
	p, e := a.scope(r, s, true)
	if e != nil {
		return e
	}
	if _, e = savedQuery(r, false, false); e != nil {
		return e
	}
	key, e := savedIdempotency(r)
	if e != nil {
		return e
	}
	var in reporting.DashboardCopy
	if e = a.API.Boundary.Decode(w, r, "DashboardCopy", &in); e != nil {
		return e
	}
	out, e := a.Service.CopyDashboard(r.Context(), s.ID, p, r.PathValue("dashboard_id"), key, in)
	if e != nil {
		return e
	}
	return a.draftResponse(w, 201, out)
}
