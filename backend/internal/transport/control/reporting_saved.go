package control

import (
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/howtomakeaname/Evia/backend/internal/analysis"
	"github.com/howtomakeaname/Evia/backend/internal/catalog"
	"github.com/howtomakeaname/Evia/backend/internal/platform/httpapi"
	"github.com/howtomakeaname/Evia/backend/internal/reporting"
)

// SavedAnalysesAPI shares the management authentication and contract boundary.
// The service obtains immutable environment scope from the stored resource.
type SavedAnalysesAPI struct {
	API     *API
	Service reporting.SavedAnalysisService
}

func (a *SavedAnalysesAPI) Register(mux *http.ServeMux) {
	if a.Service == nil {
		return
	}
	const base = "/api/v1/projects/{project_id}/saved-analyses"
	mux.HandleFunc("GET "+base, a.API.projectAuth(false, a.list))
	mux.HandleFunc("POST "+base, a.API.projectAuth(true, a.create))
	mux.HandleFunc("GET "+base+"/{analysis_id}", a.API.projectAuth(false, a.get))
	mux.HandleFunc("GET "+base+"/{analysis_id}/draft", a.API.projectAuth(false, a.draft))
	mux.HandleFunc("PATCH "+base+"/{analysis_id}/draft", a.API.projectAuth(true, a.save))
	mux.HandleFunc("POST "+base+"/{analysis_id}/validations", a.API.projectAuth(true, a.validate))
	mux.HandleFunc("GET "+base+"/{analysis_id}/versions", a.API.projectAuth(false, a.versions))
	mux.HandleFunc("POST "+base+"/{analysis_id}/versions", a.API.projectAuth(true, a.publish))
	mux.HandleFunc("GET "+base+"/{analysis_id}/versions/{version}", a.API.projectAuth(false, a.version))
	mux.HandleFunc("POST "+base+"/{analysis_id}/versions/{version}/queries", a.API.projectAuth(true, a.execute))
	mux.HandleFunc("POST "+base+"/{analysis_id}/archive", a.API.projectAuth(true, a.archive))
	mux.HandleFunc("POST "+base+"/{analysis_id}/unarchive", a.API.projectAuth(true, a.unarchive))
}

func (a *SavedAnalysesAPI) scope(r *http.Request, s authorizedActor, write bool) (string, error) {
	if id := r.PathValue("analysis_id"); id != "" && !catalogUUID.MatchString(id) {
		return "", analysis.Invalid("INVALID_ANALYSIS_ID", "保存分析标识无效")
	}
	if v := r.PathValue("version"); v != "" {
		if _, e := savedVersion(v); e != nil {
			return "", e
		}
	}
	return a.API.catalogProject(r, s, write)
}
func savedVersion(value string) (int64, error) {
	version, e := strconv.ParseInt(value, 10, 64)
	if e != nil || version < 1 || version > catalog.MaxInteger || strconv.FormatInt(version, 10) != value {
		return 0, analysis.Invalid("INVALID_SAVED_ANALYSIS_VERSION", "分析版本必须为安全的正整数")
	}
	return version, nil
}
func savedQuery(r *http.Request, list, environment bool) (url.Values, error) {
	return savedQueryWith(r, list, environment, false)
}

// savedArchiveQuery is the dashboard listing allowlist with the closed-set
// view selector used to switch between active and archived dashboards.
func savedArchiveQuery(r *http.Request) (url.Values, error) {
	return savedQueryWith(r, true, true, true)
}

// savedDashboardListQuery additionally allows the folder_id scope, which is
// meaningful only for dashboard listing ("root" or a folder UUID), and the
// optional title substring search term q.
func savedDashboardListQuery(r *http.Request) (url.Values, error) {
	return savedQueryWith(r, true, true, true, "folder_id", "q")
}

// savedAnalysisListQuery additionally allows the optional title substring
// search term q; the shared archive allowlist stays q-free for metrics.
func savedAnalysisListQuery(r *http.Request) (url.Values, error) {
	return savedQueryWith(r, true, true, true, "q")
}
func savedQueryWith(r *http.Request, list, environment, archiveView bool, extra ...string) (url.Values, error) {
	if len(r.URL.RawQuery) > 8192 {
		return nil, analysis.Invalid("INVALID_QUERY", "查询参数过长")
	}
	q, e := url.ParseQuery(r.URL.RawQuery)
	if e != nil {
		return nil, analysis.Invalid("INVALID_QUERY", "查询参数编码无效")
	}
	for k, v := range q {
		allowed := k == "limit" || k == "cursor" || (environment && k == "environment") || (archiveView && k == "view")
		for _, name := range extra {
			if k == name {
				allowed = true
			}
		}
		if !list || len(v) != 1 || !allowed || k == "limit" && v[0] == "" {
			return nil, analysis.Invalid("INVALID_QUERY", "查询参数不符合接口定义")
		}
	}
	if environment && !catalog.ValidEnvironment(q.Get("environment")) {
		return nil, analysis.Invalid("INVALID_ENVIRONMENT", "请选择有效环境", "/environment")
	}
	if archiveView {
		switch q.Get("view") {
		case "", "active", "archived":
		default:
			return nil, analysis.Invalid("INVALID_ARCHIVE_VIEW", "看板归档视图参数无效", "/view")
		}
	}
	return q, nil
}
func savedLimit(q url.Values, max int) (int, error) {
	if _, ok := q["limit"]; !ok {
		return 20, nil
	}
	value := q.Get("limit")
	limit, e := strconv.Atoi(value)
	if e != nil || limit < 1 || limit > max || strconv.Itoa(limit) != value {
		return 0, analysis.Invalid("INVALID_LIMIT", "分页数量超出允许范围", "/limit")
	}
	return limit, nil
}
func savedIdempotency(r *http.Request) (string, error) {
	keys := r.Header.Values("Idempotency-Key")
	if len(keys) != 1 || len(keys[0]) < 1 || len(keys[0]) > 128 || strings.IndexFunc(keys[0], func(c rune) bool { return c < 33 || c > 126 }) >= 0 {
		return "", analysis.Invalid("INVALID_IDEMPOTENCY_KEY", "请提供单个 1 到 128 个可见 ASCII 字符的幂等键")
	}
	return keys[0], nil
}
func (a *SavedAnalysesAPI) create(w http.ResponseWriter, r *http.Request, s authorizedActor) error {
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
	var in reporting.SavedAnalysisCreate
	if e = a.API.Boundary.Decode(w, r, "SavedAnalysisCreate", &in); e != nil {
		return e
	}
	out, e := a.Service.CreateSavedAnalysis(r.Context(), s.ID, p, key, in)
	if e != nil {
		return e
	}
	return a.draftResponse(w, 201, out)
}
func (a *SavedAnalysesAPI) get(w http.ResponseWriter, r *http.Request, s authorizedActor) error {
	p, e := a.scope(r, s, false)
	if e != nil {
		return e
	}
	if _, e = savedQuery(r, false, false); e != nil {
		return e
	}
	out, e := a.Service.GetSavedAnalysis(r.Context(), s.ID, p, r.PathValue("analysis_id"))
	if e != nil {
		return e
	}
	return a.API.Boundary.JSON(w, 200, "SavedAnalysis", out)
}
func (a *SavedAnalysesAPI) draft(w http.ResponseWriter, r *http.Request, s authorizedActor) error {
	p, e := a.scope(r, s, false)
	if e != nil {
		return e
	}
	if _, e = savedQuery(r, false, false); e != nil {
		return e
	}
	out, e := a.Service.GetSavedAnalysisDraft(r.Context(), s.ID, p, r.PathValue("analysis_id"))
	if e != nil {
		return e
	}
	return a.draftResponse(w, 200, out)
}
func (a *SavedAnalysesAPI) draftResponse(w http.ResponseWriter, status int, out reporting.SavedAnalysisDraft) error {
	w.Header().Set("ETag", strconv.Quote(strconv.FormatInt(out.DraftRevision, 10)))
	return a.API.Boundary.JSON(w, status, "SavedAnalysisDraft", out)
}
func (a *SavedAnalysesAPI) save(w http.ResponseWriter, r *http.Request, s authorizedActor) error {
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
	var in reporting.SavedAnalysisUpdate
	if e = a.API.Boundary.Decode(w, r, "SavedAnalysisUpdate", &in); e != nil {
		return e
	}
	out, e := a.Service.SaveSavedAnalysisDraft(r.Context(), s.ID, p, r.PathValue("analysis_id"), revision, in.Definition)
	if e != nil {
		return e
	}
	return a.draftResponse(w, 200, out)
}
func (a *SavedAnalysesAPI) validate(w http.ResponseWriter, r *http.Request, s authorizedActor) error {
	p, e := a.scope(r, s, false)
	if e != nil {
		return e
	}
	if _, e = savedQuery(r, false, false); e != nil {
		return e
	}
	var in reporting.SavedAnalysisValidate
	if e = a.API.Boundary.Decode(w, r, "SavedAnalysisValidate", &in); e != nil {
		return e
	}
	out, e := a.Service.ValidateSavedAnalysis(r.Context(), s.ID, p, r.PathValue("analysis_id"), in)
	if e != nil {
		return e
	}
	return a.API.Boundary.JSON(w, 200, "SavedAnalysisValidation", out)
}
func (a *SavedAnalysesAPI) publish(w http.ResponseWriter, r *http.Request, s authorizedActor) error {
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
	var in reporting.SavedAnalysisPublish
	if e = a.API.Boundary.Decode(w, r, "SavedAnalysisPublish", &in); e != nil {
		return e
	}
	out, e := a.Service.PublishSavedAnalysis(r.Context(), s.ID, p, r.PathValue("analysis_id"), key, in)
	if e != nil {
		return e
	}
	return a.API.Boundary.JSON(w, 201, "SavedAnalysisVersion", out)
}
func (a *SavedAnalysesAPI) version(w http.ResponseWriter, r *http.Request, s authorizedActor) error {
	p, e := a.scope(r, s, false)
	if e != nil {
		return e
	}
	if _, e = savedQuery(r, false, false); e != nil {
		return e
	}
	version, _ := savedVersion(r.PathValue("version"))
	out, e := a.Service.GetSavedAnalysisVersion(r.Context(), s.ID, p, r.PathValue("analysis_id"), version)
	if e != nil {
		return e
	}
	return a.API.Boundary.JSON(w, 200, "SavedAnalysisVersion", out)
}
func (a *SavedAnalysesAPI) execute(w http.ResponseWriter, r *http.Request, s authorizedActor) error {
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
	var in reporting.SavedAnalysisExecute
	if e = a.API.Boundary.Decode(w, r, "SavedAnalysisExecute", &in); e != nil {
		return e
	}
	version, _ := savedVersion(r.PathValue("version"))
	out, e := a.Service.EnqueueSavedAnalysisVersion(r.Context(), s.ID, p, r.PathValue("analysis_id"), version, key, in)
	if e != nil {
		return e
	}
	return a.API.Boundary.JSON(w, 202, "QueryJob", out)
}
func (a *SavedAnalysesAPI) list(w http.ResponseWriter, r *http.Request, s authorizedActor) error {
	p, e := a.scope(r, s, false)
	if e != nil {
		return e
	}
	q, e := savedAnalysisListQuery(r)
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
	search, e := reporting.NormalizeCatalogSearch(q.Get("q"))
	if e != nil {
		return e
	}
	// The cursor is bound to every list predicate, including the search term.
	scope := strings.Join([]string{"saved_analyses", s.ID, p, q.Get("environment"), view, search}, ":")
	after, e := a.API.Cursor.Decode(scope, q.Get("cursor"))
	if e != nil || after != "" && !catalogUUID.MatchString(after) {
		return analysis.Invalid("INVALID_CURSOR", "分页游标已失效，请重新加载", "/cursor")
	}
	items, e := a.Service.ListSavedAnalyses(r.Context(), s.ID, p, q.Get("environment"), after, limit, archivedOnly, search)
	if e != nil {
		return e
	}
	out := struct {
		Items      []reporting.SavedAnalysis `json:"items"`
		NextCursor *string                   `json:"next_cursor"`
	}{Items: []reporting.SavedAnalysis{}}
	if len(items) > limit {
		next := a.API.Cursor.Encode(scope, items[limit-1].ID)
		out.NextCursor = &next
		items = items[:limit]
	}
	out.Items = append(out.Items, items...)
	return a.API.Boundary.JSON(w, 200, "SavedAnalysisList", out)
}

func (a *SavedAnalysesAPI) archive(w http.ResponseWriter, r *http.Request, s authorizedActor) error {
	p, e := a.scope(r, s, true)
	if e != nil {
		return e
	}
	if _, e = savedQuery(r, false, false); e != nil {
		return e
	}
	out, e := a.Service.ArchiveSavedAnalysis(r.Context(), s.ID, p, r.PathValue("analysis_id"))
	if e != nil {
		return e
	}
	return a.API.Boundary.JSON(w, 200, "SavedAnalysis", out)
}

func (a *SavedAnalysesAPI) unarchive(w http.ResponseWriter, r *http.Request, s authorizedActor) error {
	p, e := a.scope(r, s, true)
	if e != nil {
		return e
	}
	if _, e = savedQuery(r, false, false); e != nil {
		return e
	}
	out, e := a.Service.UnarchiveSavedAnalysis(r.Context(), s.ID, p, r.PathValue("analysis_id"))
	if e != nil {
		return e
	}
	return a.API.Boundary.JSON(w, 200, "SavedAnalysis", out)
}
func (a *SavedAnalysesAPI) versions(w http.ResponseWriter, r *http.Request, s authorizedActor) error {
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
	id := r.PathValue("analysis_id")
	scope := strings.Join([]string{"saved_analysis_versions", s.ID, p, id}, ":")
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
	items, e := a.Service.ListSavedAnalysisVersions(r.Context(), s.ID, p, id, after, limit)
	if e != nil {
		return e
	}
	out := struct {
		Items      []reporting.SavedAnalysisVersion `json:"items"`
		NextCursor *string                          `json:"next_cursor"`
	}{Items: []reporting.SavedAnalysisVersion{}}
	if len(items) > limit {
		next := a.API.Cursor.Encode(scope, strconv.FormatInt(items[limit-1].Version, 10))
		out.NextCursor = &next
		items = items[:limit]
	}
	out.Items = append(out.Items, items...)
	return a.API.Boundary.JSON(w, 200, "SavedAnalysisVersionList", out)
}
