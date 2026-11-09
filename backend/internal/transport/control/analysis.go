package control

import (
	"encoding/json"
	"net/http"
	"net/url"

	"github.com/howtomakeaname/Evia/backend/internal/analysis"
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
func (a *AnalysisAPI) scope(r *http.Request, s authorizedActor, result bool) (string, error) {
	project := r.PathValue("project_id")
	if !catalogUUID.MatchString(project) {
		return "", analysis.Invalid("INVALID_PROJECT_ID", "项目标识无效")
	}
	if id := r.PathValue("query_id"); id != "" && !catalogUUID.MatchString(id) {
		return "", analysis.Invalid("INVALID_QUERY_ID", "查询标识无效")
	}
	if len(r.URL.RawQuery) > 8192 {
		return "", analysis.Invalid("INVALID_QUERY", "查询参数过长")
	}
	values, e := url.ParseQuery(r.URL.RawQuery)
	if e != nil {
		return "", analysis.Invalid("INVALID_QUERY", "查询参数编码无效")
	}
	for key, list := range values {
		if !result || key != "cursor" || len(list) != 1 {
			return "", analysis.Invalid("INVALID_QUERY", "此接口不接受这些查询参数")
		}
		if list[0] != "" {
			return "", analysis.Invalid("UNSUPPORTED_QUERY_CURSOR", "当前版本提供有界结果快照")
		}
	}
	if e = a.API.authorizeProject(r, s, project, false); e != nil {
		return "", e
	}
	return project, nil
}
func (a *AnalysisAPI) create(w http.ResponseWriter, r *http.Request, s authorizedActor) error {
	return a.createKind(w, r, s, false)
}
func (a *AnalysisAPI) journey(w http.ResponseWriter, r *http.Request, s authorizedActor) error {
	return a.createKind(w, r, s, true)
}
func (a *AnalysisAPI) createKind(w http.ResponseWriter, r *http.Request, s authorizedActor, journey bool) error {
	project, e := a.scope(r, s, false)
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
		if kind.Kind != "journey" {
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
	project, e := a.scope(r, s, false)
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
	project, e := a.scope(r, s, false)
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
	project, e := a.scope(r, s, true)
	if e != nil {
		return e
	}
	result, e := a.Service.Result(r.Context(), s.ID, project, r.PathValue("query_id"))
	if e != nil {
		return e
	}
	return a.API.Boundary.JSON(w, 200, "QueryResult", result)
}
