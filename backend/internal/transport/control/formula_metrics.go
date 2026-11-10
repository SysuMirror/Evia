package control

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/howtomakeaname/Evia/backend/internal/access"
	"github.com/howtomakeaname/Evia/backend/internal/analysis"
	"github.com/howtomakeaname/Evia/backend/internal/platform/httpapi"
	"github.com/howtomakeaname/Evia/backend/internal/reporting"
)

// FormulaMetricsAPI exposes the composite formula metric management lifecycle.
// It reuses the metrics.* permissions and never registers an execution route:
// composite execution enters through the query worker in a later vertical.
type FormulaMetricsAPI struct {
	API     *API
	Service reporting.FormulaMetricService
}

func (a *FormulaMetricsAPI) Register(mux *http.ServeMux) {
	if a.Service == nil {
		return
	}
	const base = "/api/v1/projects/{project_id}/formula-metrics"
	mux.HandleFunc("GET "+base, a.API.projectPermissionAuth(false, access.PermissionMetricsRead, a.list))
	mux.HandleFunc("POST "+base, a.API.projectPermissionAuth(true, access.PermissionMetricsWrite, a.create))
	mux.HandleFunc("GET "+base+"/{metric_id}", a.API.projectPermissionAuth(false, access.PermissionMetricsRead, a.get))
	mux.HandleFunc("GET "+base+"/{metric_id}/draft", a.API.projectPermissionAuth(false, access.PermissionMetricsRead, a.draft))
	mux.HandleFunc("PATCH "+base+"/{metric_id}/draft", a.API.projectPermissionAuth(true, access.PermissionMetricsWrite, a.save))
	mux.HandleFunc("POST "+base+"/{metric_id}/validations", a.API.projectPermissionAuth(true, access.PermissionMetricsWrite, a.validate))
	mux.HandleFunc("GET "+base+"/{metric_id}/versions", a.API.projectPermissionAuth(false, access.PermissionMetricsRead, a.versions))
	mux.HandleFunc("POST "+base+"/{metric_id}/versions", a.API.projectPermissionAuth(true, access.PermissionMetricsPublish, a.publish))
	mux.HandleFunc("GET "+base+"/{metric_id}/versions/{version}", a.API.projectPermissionAuth(false, access.PermissionMetricsRead, a.version))
}

func (a *FormulaMetricsAPI) scope(r *http.Request, s authorizedActor, write bool) (string, error) {
	if id := r.PathValue("metric_id"); id != "" && !catalogUUID.MatchString(id) {
		return "", analysis.Invalid("INVALID_METRIC_ID", "指标标识无效")
	}
	if v := r.PathValue("version"); v != "" {
		if _, e := metricVersion(v); e != nil {
			return "", e
		}
	}
	return a.API.catalogProject(r, s, write)
}

func (a *FormulaMetricsAPI) create(w http.ResponseWriter, r *http.Request, s authorizedActor) error {
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
	var in reporting.FormulaMetricCreate
	if e = a.API.Boundary.Decode(w, r, "FormulaMetricCreate", &in); e != nil {
		return e
	}
	out, e := a.Service.CreateFormulaMetric(r.Context(), s.ID, p, key, in)
	if e != nil {
		return e
	}
	return a.draftResponse(w, 201, out)
}

func (a *FormulaMetricsAPI) get(w http.ResponseWriter, r *http.Request, s authorizedActor) error {
	p, e := a.scope(r, s, false)
	if e != nil {
		return e
	}
	if _, e = savedQuery(r, false, false); e != nil {
		return e
	}
	out, e := a.Service.GetFormulaMetric(r.Context(), s.ID, p, r.PathValue("metric_id"))
	if e != nil {
		return e
	}
	return a.API.Boundary.JSON(w, 200, "FormulaMetricRecord", out)
}

func (a *FormulaMetricsAPI) draft(w http.ResponseWriter, r *http.Request, s authorizedActor) error {
	p, e := a.scope(r, s, false)
	if e != nil {
		return e
	}
	if _, e = savedQuery(r, false, false); e != nil {
		return e
	}
	out, e := a.Service.GetFormulaMetricDraft(r.Context(), s.ID, p, r.PathValue("metric_id"))
	if e != nil {
		return e
	}
	return a.draftResponse(w, 200, out)
}

func (a *FormulaMetricsAPI) draftResponse(w http.ResponseWriter, status int, out reporting.FormulaMetricDraft) error {
	w.Header().Set("ETag", strconv.Quote(strconv.FormatInt(out.DraftRevision, 10)))
	return a.API.Boundary.JSON(w, status, "FormulaMetricDraft", out)
}

func (a *FormulaMetricsAPI) save(w http.ResponseWriter, r *http.Request, s authorizedActor) error {
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
	var in reporting.FormulaMetricUpdate
	if e = a.API.Boundary.Decode(w, r, "FormulaMetricUpdate", &in); e != nil {
		return e
	}
	out, e := a.Service.SaveFormulaMetricDraft(r.Context(), s.ID, p, r.PathValue("metric_id"), revision, in.Definition)
	if e != nil {
		return e
	}
	return a.draftResponse(w, 200, out)
}

func (a *FormulaMetricsAPI) validate(w http.ResponseWriter, r *http.Request, s authorizedActor) error {
	p, e := a.scope(r, s, true)
	if e != nil {
		return e
	}
	if _, e = savedQuery(r, false, false); e != nil {
		return e
	}
	var in reporting.FormulaMetricValidate
	if e = a.API.Boundary.Decode(w, r, "FormulaMetricValidate", &in); e != nil {
		return e
	}
	out, e := a.Service.ValidateFormulaMetric(r.Context(), s.ID, p, r.PathValue("metric_id"), in)
	if e != nil {
		return e
	}
	return a.API.Boundary.JSON(w, 200, "FormulaMetricValidation", out)
}

func (a *FormulaMetricsAPI) publish(w http.ResponseWriter, r *http.Request, s authorizedActor) error {
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
	var in reporting.FormulaMetricPublish
	if e = a.API.Boundary.Decode(w, r, "FormulaMetricPublish", &in); e != nil {
		return e
	}
	out, e := a.Service.PublishFormulaMetric(r.Context(), s.ID, p, r.PathValue("metric_id"), key, in)
	if e != nil {
		return e
	}
	return a.API.Boundary.JSON(w, 201, "FormulaMetricVersion", out)
}

func (a *FormulaMetricsAPI) version(w http.ResponseWriter, r *http.Request, s authorizedActor) error {
	p, e := a.scope(r, s, false)
	if e != nil {
		return e
	}
	if _, e = savedQuery(r, false, false); e != nil {
		return e
	}
	v, _ := metricVersion(r.PathValue("version"))
	out, e := a.Service.GetFormulaMetricVersion(r.Context(), s.ID, p, r.PathValue("metric_id"), v)
	if e != nil {
		return e
	}
	return a.API.Boundary.JSON(w, 200, "FormulaMetricVersion", out)
}

func (a *FormulaMetricsAPI) list(w http.ResponseWriter, r *http.Request, s authorizedActor) error {
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
	scope := strings.Join([]string{"formula_metrics", s.ID, p, q.Get("environment")}, ":")
	after, e := a.API.Cursor.Decode(scope, q.Get("cursor"))
	if e != nil || after != "" && !catalogUUID.MatchString(after) {
		return analysis.Invalid("INVALID_CURSOR", "分页游标已失效，请重新加载", "/cursor")
	}
	items, e := a.Service.ListFormulaMetrics(r.Context(), s.ID, p, q.Get("environment"), after, limit)
	if e != nil {
		return e
	}
	out := struct {
		Items      []reporting.FormulaMetricRecord `json:"items"`
		NextCursor *string                         `json:"next_cursor"`
	}{Items: []reporting.FormulaMetricRecord{}}
	if len(items) > limit {
		next := a.API.Cursor.Encode(scope, items[limit-1].ID)
		out.NextCursor = &next
		items = items[:limit]
	}
	out.Items = append(out.Items, items...)
	return a.API.Boundary.JSON(w, 200, "FormulaMetricList", out)
}

func (a *FormulaMetricsAPI) versions(w http.ResponseWriter, r *http.Request, s authorizedActor) error {
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
	id := r.PathValue("metric_id")
	scope := strings.Join([]string{"formula_metric_versions", s.ID, p, id}, ":")
	position, e := a.API.Cursor.Decode(scope, q.Get("cursor"))
	if e != nil {
		return analysis.Invalid("INVALID_CURSOR", "分页游标已失效，请重新加载", "/cursor")
	}
	var after int64
	if position != "" {
		after, e = metricVersion(position)
		if e != nil {
			return analysis.Invalid("INVALID_CURSOR", "分页游标已失效，请重新加载", "/cursor")
		}
	}
	items, e := a.Service.ListFormulaMetricVersions(r.Context(), s.ID, p, id, after, limit)
	if e != nil {
		return e
	}
	out := struct {
		Items      []reporting.FormulaMetricVersion `json:"items"`
		NextCursor *string                          `json:"next_cursor"`
	}{Items: []reporting.FormulaMetricVersion{}}
	if len(items) > limit {
		next := a.API.Cursor.Encode(scope, strconv.FormatInt(items[limit-1].Version, 10))
		out.NextCursor = &next
		items = items[:limit]
	}
	out.Items = append(out.Items, items...)
	return a.API.Boundary.JSON(w, 200, "FormulaMetricVersionList", out)
}
