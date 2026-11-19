package control

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"

	"github.com/howtomakeaname/Evia/backend/internal/analysis"
	"github.com/howtomakeaname/Evia/backend/internal/platform/fault"
)

type AnalysisService = analysis.QueryService

// AnalysisAPI registers only query routes. The shared API owns authentication,
// CSRF, origin checks, request IDs and the contract boundary.
type AnalysisAPI struct {
	API     *API
	Service AnalysisService
}

func (a *AnalysisAPI) Register(mux *http.ServeMux) {
	if a.Service == nil {
		return
	}
	const base = "/api/v1/projects/{project_id}"
	mux.HandleFunc("POST "+base+"/queries", a.API.projectAuth(true, a.create))
	mux.HandleFunc("POST "+base+"/journeys/query", a.API.projectAuth(true, a.journey))
	mux.HandleFunc("GET "+base+"/queries/{query_id}", a.API.projectAuth(false, a.get))
	mux.HandleFunc("DELETE "+base+"/queries/{query_id}", a.API.projectAuth(true, a.cancel))
	mux.HandleFunc("GET "+base+"/queries/{query_id}/result", a.API.projectAuth(false, a.result))
}
func (a *AnalysisAPI) scope(r *http.Request, s authorizedActor, result bool) (string, url.Values, error) {
	project := r.PathValue("project_id")
	if !catalogUUID.MatchString(project) {
		return "", nil, analysis.Invalid("INVALID_PROJECT_ID", "项目标识无效")
	}
	if id := r.PathValue("query_id"); id != "" && !catalogUUID.MatchString(id) {
		return "", nil, analysis.Invalid("INVALID_QUERY_ID", "查询标识无效")
	}
	if len(r.URL.RawQuery) > 8192 {
		return "", nil, analysis.Invalid("INVALID_QUERY", "查询参数过长")
	}
	values, e := url.ParseQuery(r.URL.RawQuery)
	if e != nil {
		return "", nil, analysis.Invalid("INVALID_QUERY", "查询参数编码无效")
	}
	for key, list := range values {
		// Only the result snapshot reader supports continuation parameters;
		// the other query routes reject every query string parameter.
		if !result || (key != "cursor" && key != "limit") || len(list) != 1 {
			return "", nil, analysis.Invalid("INVALID_QUERY", "此接口不接受这些查询参数")
		}
	}
	if e = a.API.authorizeProject(r, s, project, false); e != nil {
		return "", nil, e
	}
	return project, values, nil
}
func (a *AnalysisAPI) create(w http.ResponseWriter, r *http.Request, s authorizedActor) error {
	return a.createKind(w, r, s, false)
}
func (a *AnalysisAPI) journey(w http.ResponseWriter, r *http.Request, s authorizedActor) error {
	return a.createKind(w, r, s, true)
}
func (a *AnalysisAPI) createKind(w http.ResponseWriter, r *http.Request, s authorizedActor, journey bool) error {
	project, _, e := a.scope(r, s, false)
	if e != nil {
		return e
	}
	var raw json.RawMessage
	if e = a.API.Boundary.Decode(w, r, "QueryRequest", &raw); e != nil {
		return e
	}
	if journey {
		var kind struct {
			Kind string `json:"kind"`
		}
		if e = json.Unmarshal(raw, &kind); e != nil {
			return e
		}
		if kind.Kind != analysis.QueryKindJourney {
			return analysis.Invalid("INVALID_QUERY_KIND", "轨迹接口必须使用 journey 查询", "/kind")
		}
	}
	job, e := a.Service.Create(r.Context(), s.ID, project, raw)
	if e != nil {
		return e
	}
	return a.API.Boundary.JSON(w, 202, "QueryJob", job)
}
func (a *AnalysisAPI) get(w http.ResponseWriter, r *http.Request, s authorizedActor) error {
	project, _, e := a.scope(r, s, false)
	if e != nil {
		return e
	}
	job, e := a.Service.Get(r.Context(), s.ID, project, r.PathValue("query_id"))
	if e != nil {
		return e
	}
	return a.API.Boundary.JSON(w, 200, "QueryJob", job)
}
func (a *AnalysisAPI) cancel(w http.ResponseWriter, r *http.Request, s authorizedActor) error {
	project, _, e := a.scope(r, s, false)
	if e != nil {
		return e
	}
	job, e := a.Service.Cancel(r.Context(), s.ID, project, r.PathValue("query_id"))
	if e != nil {
		return e
	}
	return a.API.Boundary.JSON(w, 202, "QueryJob", job)
}
func (a *AnalysisAPI) result(w http.ResponseWriter, r *http.Request, s authorizedActor) error {
	project, values, e := a.scope(r, s, true)
	if e != nil {
		return e
	}
	queryID := r.PathValue("query_id")
	scope := "analysis_query_result:" + s.ID + ":" + project + ":" + queryID
	// Continuation parameters are fail-closed before the stored snapshot is read.
	position, paginate, e := a.resolveResultPosition(values, scope)
	if e != nil {
		return e
	}
	result, e := a.Service.Result(r.Context(), s.ID, project, queryID)
	if e != nil {
		return e
	}
	if !paginate {
		return a.API.Boundary.JSON(w, http.StatusOK, "QueryResult", result)
	}
	page, e := a.sliceQueryResult(result, position, scope)
	if e != nil {
		return e
	}
	return a.API.Boundary.JSON(w, http.StatusOK, "QueryResult", page)
}

// queryResultPosition is the signed, scope-bound continuation state embedded
// in a result cursor. The stored snapshot is immutable, so the offset is stable.
type queryResultPosition struct {
	Offset int `json:"o"`
	Limit  int `json:"l"`
}

func (a *AnalysisAPI) resolveResultPosition(values url.Values, scope string) (queryResultPosition, bool, error) {
	if values.Get("cursor") == "" && !values.Has("limit") {
		return queryResultPosition{}, false, nil
	}
	if a.API.Cursor == nil {
		return queryResultPosition{}, false, fault.New(fault.Unavailable, "QUERY_RESULT_CURSOR_UNAVAILABLE", "结果分页尚未配置")
	}
	position := queryResultPosition{}
	if raw := values.Get("cursor"); raw != "" {
		decoded, err := a.API.Cursor.Decode(scope, raw)
		if err != nil {
			return position, false, governanceCursorError()
		}
		if err = governanceDecodePosition(decoded, &position); err != nil {
			return position, false, err
		}
		if position.Offset < 0 || position.Limit < 1 {
			return position, false, governanceCursorError()
		}
		if values.Has("limit") {
			requested, err := parseQueryResultLimit(values)
			if err != nil {
				return position, false, err
			}
			if requested != position.Limit {
				return position, false, fault.New(fault.Invalid, "INVALID_LIMIT", "分页大小须与首页保持一致")
			}
		}
		return position, true, nil
	}
	limit, err := parseQueryResultLimit(values)
	if err != nil {
		return position, false, err
	}
	return queryResultPosition{Offset: 0, Limit: limit}, true, nil
}

func (a *AnalysisAPI) sliceQueryResult(result analysis.Result, position queryResultPosition, scope string) (analysis.Result, error) {
	total := len(result.Rows)
	if position.Offset > total {
		return analysis.Result{}, governanceCursorError()
	}
	start := position.Offset
	end := start + position.Limit
	if end > total {
		end = total
	}
	page := result
	page.Rows = result.Rows[start:end]
	page.NextCursor = nil
	if end < total {
		data, err := json.Marshal(queryResultPosition{Offset: end, Limit: position.Limit})
		if err != nil {
			return analysis.Result{}, err
		}
		token := a.API.Cursor.Encode(scope, string(data))
		page.NextCursor = &token
	}
	return page, nil
}

func parseQueryResultLimit(values url.Values) (int, error) {
	list, ok := values["limit"]
	if !ok || len(list) != 1 {
		return 0, fault.New(fault.Invalid, "INVALID_LIMIT", "缺少分页大小")
	}
	raw := list[0]
	limit, err := strconv.Atoi(raw)
	if err != nil || limit < 1 || limit > 200 || strconv.Itoa(limit) != raw {
		return 0, fault.New(fault.Invalid, "INVALID_LIMIT", "分页大小必须是 1 到 200 的整数")
	}
	return limit, nil
}
