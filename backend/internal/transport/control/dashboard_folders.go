package control

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/howtomakeaname/Evia/backend/internal/analysis"
	"github.com/howtomakeaname/Evia/backend/internal/platform/httpapi"
	"github.com/howtomakeaname/Evia/backend/internal/reporting"
)

// DashboardFoldersAPI exposes the bounded folder hierarchy with the same
// management authentication and contract boundary as the dashboard resource.
type DashboardFoldersAPI struct {
	API     *API
	Service reporting.DashboardFolderService
}

func (a *DashboardFoldersAPI) Register(mux *http.ServeMux) {
	if a.Service == nil {
		return
	}
	const base = "/api/v1/projects/{project_id}/dashboard-folders"
	mux.HandleFunc("GET "+base, a.API.projectAuth(false, a.list))
	mux.HandleFunc("POST "+base, a.API.projectAuth(true, a.create))
	mux.HandleFunc("GET "+base+"/{folder_id}", a.API.projectAuth(false, a.get))
	mux.HandleFunc("PATCH "+base+"/{folder_id}", a.API.projectAuth(true, a.update))
	mux.HandleFunc("DELETE "+base+"/{folder_id}", a.API.projectAuth(true, a.delete))
}

func (a *DashboardFoldersAPI) scope(r *http.Request, s authorizedActor, write bool) (string, error) {
	if id := r.PathValue("folder_id"); id != "" && !catalogUUID.MatchString(id) {
		return "", analysis.Invalid("INVALID_DASHBOARD_FOLDER_ID", "文件夹标识无效")
	}
	return a.API.catalogProject(r, s, write)
}

func (a *DashboardFoldersAPI) create(w http.ResponseWriter, r *http.Request, s authorizedActor) error {
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
	var in reporting.DashboardFolderCreate
	if e = a.API.Boundary.Decode(w, r, "DashboardFolderCreate", &in); e != nil {
		return e
	}
	out, e := a.Service.CreateDashboardFolder(r.Context(), s.ID, p, key, in)
	if e != nil {
		return e
	}
	w.Header().Set("ETag", strconv.Quote(strconv.FormatInt(out.Revision, 10)))
	return a.API.Boundary.JSON(w, 201, "DashboardFolder", out)
}

func (a *DashboardFoldersAPI) list(w http.ResponseWriter, r *http.Request, s authorizedActor) error {
	p, e := a.scope(r, s, false)
	if e != nil {
		return e
	}
	q, e := savedQuery(r, true, true)
	if e != nil {
		return e
	}
	limit, e := savedLimit(q, 100)
	if e != nil {
		return e
	}
	scope := strings.Join([]string{"dashboard_folders", s.ID, p, q.Get("environment")}, ":")
	after, e := a.API.Cursor.Decode(scope, q.Get("cursor"))
	if e != nil || after != "" && !catalogUUID.MatchString(after) {
		return analysis.Invalid("INVALID_CURSOR", "分页游标已失效，请重新加载", "/cursor")
	}
	items, e := a.Service.ListDashboardFolders(r.Context(), s.ID, p, q.Get("environment"), after, limit)
	if e != nil {
		return e
	}
	out := struct {
		Items      []reporting.DashboardFolder `json:"items"`
		NextCursor *string                     `json:"next_cursor"`
	}{Items: []reporting.DashboardFolder{}}
	if len(items) > limit {
		next := a.API.Cursor.Encode(scope, items[limit-1].ID)
		out.NextCursor = &next
		items = items[:limit]
	}
	out.Items = append(out.Items, items...)
	return a.API.Boundary.JSON(w, 200, "DashboardFolderList", out)
}

func (a *DashboardFoldersAPI) get(w http.ResponseWriter, r *http.Request, s authorizedActor) error {
	p, e := a.scope(r, s, false)
	if e != nil {
		return e
	}
	if _, e = savedQuery(r, false, false); e != nil {
		return e
	}
	out, e := a.Service.GetDashboardFolder(r.Context(), s.ID, p, r.PathValue("folder_id"))
	if e != nil {
		return e
	}
	return a.API.Boundary.JSON(w, 200, "DashboardFolder", out)
}

func (a *DashboardFoldersAPI) update(w http.ResponseWriter, r *http.Request, s authorizedActor) error {
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
	var in reporting.DashboardFolderUpdate
	if e = a.API.Boundary.Decode(w, r, "DashboardFolderUpdate", &in); e != nil {
		return e
	}
	out, e := a.Service.UpdateDashboardFolder(r.Context(), s.ID, p, r.PathValue("folder_id"), revision, in)
	if e != nil {
		return e
	}
	w.Header().Set("ETag", strconv.Quote(strconv.FormatInt(out.Revision, 10)))
	return a.API.Boundary.JSON(w, 200, "DashboardFolder", out)
}

func (a *DashboardFoldersAPI) delete(w http.ResponseWriter, r *http.Request, s authorizedActor) error {
	p, e := a.scope(r, s, true)
	if e != nil {
		return e
	}
	if _, e = savedQuery(r, false, false); e != nil {
		return e
	}
	if e = a.Service.DeleteDashboardFolder(r.Context(), s.ID, p, r.PathValue("folder_id")); e != nil {
		return e
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}
