package control

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/howtomakeaname/Evia/backend/internal/access"
	"github.com/howtomakeaname/Evia/backend/internal/analysis"
	"github.com/howtomakeaname/Evia/backend/internal/catalog"
	"github.com/howtomakeaname/Evia/backend/internal/platform/httpapi"
	"github.com/howtomakeaname/Evia/backend/internal/reporting"
)

// MetricsAPI shares the management authentication and contract boundary.
// The service obtains immutable environment scope from the stored resource.
type MetricsAPI struct {
	API     *API
	Service reporting.MetricService
}

func (a *MetricsAPI) Register(mux *http.ServeMux) {
	if a.Service == nil {
		return
	}
	const base = "/api/v1/projects/{project_id}/metrics"
	mux.HandleFunc("GET "+base, a.API.projectPermissionAuth(false, access.PermissionMetricsRead, a.list))
	mux.HandleFunc("POST "+base, a.API.projectPermissionAuth(true, access.PermissionMetricsWrite, a.create))
	mux.HandleFunc("GET "+base+"/{metric_id}", a.API.projectPermissionAuth(false, access.PermissionMetricsRead, a.get))
	mux.HandleFunc("GET "+base+"/{metric_id}/draft", a.API.projectPermissionAuth(false, access.PermissionMetricsRead, a.draft))
	mux.HandleFunc("PATCH "+base+"/{metric_id}/draft", a.API.projectPermissionAuth(true, access.PermissionMetricsWrite, a.save))
	mux.HandleFunc("POST "+base+"/{metric_id}/validations", a.API.projectPermissionAuth(true, access.PermissionMetricsWrite, a.validate))
	mux.HandleFunc("GET "+base+"/{metric_id}/versions", a.API.projectPermissionAuth(false, access.PermissionMetricsRead, a.versions))
	mux.HandleFunc("POST "+base+"/{metric_id}/versions", a.API.projectPermissionAuth(true, access.PermissionMetricsPublish, a.publish))
	mux.HandleFunc("GET "+base+"/{metric_id}/versions/{version}", a.API.projectPermissionAuth(false, access.PermissionMetricsRead, a.version))
	mux.HandleFunc("POST "+base+"/{metric_id}/versions/{version}/queries", a.API.projectPermissionAuth(true, access.PermissionMetricsRun, a.execute))
}

func (a *MetricsAPI) scope(r *http.Request, s authorizedActor, write bool) (string, error) {
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
func metricVersion(value string) (int64, error) {
	version, e := strconv.ParseInt(value, 10, 64)
	if e != nil || version < 1 || version > catalog.MaxInteger || strconv.FormatInt(version, 10) != value {
		return 0, analysis.Invalid("INVALID_METRIC_VERSION", "指标版本必须为安全的正整数")
	}
	return version, nil
}
func (a *MetricsAPI) create(w http.ResponseWriter, r *http.Request, s authorizedActor) error {
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
	var in reporting.MetricCreate
	if e = a.API.Boundary.Decode(w, r, "MetricCreate", &in); e != nil {
		return e
	}
	out, e := a.Service.CreateMetric(r.Context(), s.ID, p, key, in)
	if e != nil {
		return e
	}
	return a.draftResponse(w, 201, out)
}
func (a *MetricsAPI) get(w http.ResponseWriter, r *http.Request, s authorizedActor) error {
	p, e := a.scope(r, s, false)
	if e != nil {
		return e
	}
	if _, e = savedQuery(r, false, false); e != nil {
		return e
	}
	out, e := a.Service.GetMetric(r.Context(), s.ID, p, r.PathValue("metric_id"))
	if e != nil {
		return e
	}
	return a.API.Boundary.JSON(w, 200, "MetricRecord", out)
}
func (a *MetricsAPI) draft(w http.ResponseWriter, r *http.Request, s authorizedActor) error {
	p, e := a.scope(r, s, false)
	if e != nil {
		return e
	}
	if _, e = savedQuery(r, false, false); e != nil {
		return e
	}
	out, e := a.Service.GetMetricDraft(r.Context(), s.ID, p, r.PathValue("metric_id"))
	if e != nil {
		return e
	}
	return a.draftResponse(w, 200, out)
}
func (a *MetricsAPI) draftResponse(w http.ResponseWriter, status int, out reporting.MetricDraft) error {
	w.Header().Set("ETag", strconv.Quote(strconv.FormatInt(out.DraftRevision, 10)))
	return a.API.Boundary.JSON(w, status, "MetricDraft", out)
}
func (a *MetricsAPI) save(w http.ResponseWriter, r *http.Request, s authorizedActor) error {
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
	var in reporting.MetricUpdate
	if e = a.API.Boundary.Decode(w, r, "MetricUpdate", &in); e != nil {
		return e
	}
	out, e := a.Service.SaveMetricDraft(r.Context(), s.ID, p, r.PathValue("metric_id"), revision, in.Definition)
	if e != nil {
		return e
	}
	return a.draftResponse(w, 200, out)
}
func (a *MetricsAPI) validate(w http.ResponseWriter, r *http.Request, s authorizedActor) error {
	p, e := a.scope(r, s, true)
	if e != nil {
		return e
	}
	if _, e = savedQuery(r, false, false); e != nil {
		return e
	}
	var in reporting.MetricValidate
	if e = a.API.Boundary.Decode(w, r, "MetricValidate", &in); e != nil {
		return e
	}
	out, e := a.Service.ValidateMetric(r.Context(), s.ID, p, r.PathValue("metric_id"), in)
	if e != nil {
		return e
	}
	return a.API.Boundary.JSON(w, 200, "MetricValidation", out)
}
func (a *MetricsAPI) publish(w http.ResponseWriter, r *http.Request, s authorizedActor) error {
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
	var in reporting.MetricPublish
	if e = a.API.Boundary.Decode(w, r, "MetricPublish", &in); e != nil {
		return e
	}
	out, e := a.Service.PublishMetric(r.Context(), s.ID, p, r.PathValue("metric_id"), key, in)
	if e != nil {
		return e
	}
	return a.API.Boundary.JSON(w, 201, "MetricVersion", out)
}
func (a *MetricsAPI) version(w http.ResponseWriter, r *http.Request, s authorizedActor) error {
	p, e := a.scope(r, s, false)
	if e != nil {
		return e
	}
	if _, e = savedQuery(r, false, false); e != nil {
		return e
	}
	version, _ := metricVersion(r.PathValue("version"))
	out, e := a.Service.GetMetricVersion(r.Context(), s.ID, p, r.PathValue("metric_id"), version)
	if e != nil {
		return e
	}
	return a.API.Boundary.JSON(w, 200, "MetricVersion", out)
}
func (a *MetricsAPI) execute(w http.ResponseWriter, r *http.Request, s authorizedActor) error {
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
	var in reporting.MetricExecute
	if e = a.API.Boundary.Decode(w, r, "MetricExecute", &in); e != nil {
		return e
	}
	version, _ := metricVersion(r.PathValue("version"))
	out, e := a.Service.EnqueueMetricVersion(r.Context(), s.ID, p, r.PathValue("metric_id"), version, key, in)
	if e != nil {
		return e
	}
	return a.API.Boundary.JSON(w, 202, "QueryJob", out)
}
func (a *MetricsAPI) list(w http.ResponseWriter, r *http.Request, s authorizedActor) error {
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
	scope := strings.Join([]string{"metrics", s.ID, p, q.Get("environment")}, ":")
	after, e := a.API.Cursor.Decode(scope, q.Get("cursor"))
	if e != nil || after != "" && !catalogUUID.MatchString(after) {
		return analysis.Invalid("INVALID_CURSOR", "分页游标已失效，请重新加载", "/cursor")
	}
	items, e := a.Service.ListMetrics(r.Context(), s.ID, p, q.Get("environment"), after, limit)
	if e != nil {
		return e
	}
	out := struct {
		Items      []reporting.MetricRecord `json:"items"`
		NextCursor *string                  `json:"next_cursor"`
	}{Items: []reporting.MetricRecord{}}
	if len(items) > limit {
		next := a.API.Cursor.Encode(scope, items[limit-1].ID)
		out.NextCursor = &next
		items = items[:limit]
	}
	out.Items = append(out.Items, items...)
	return a.API.Boundary.JSON(w, 200, "MetricList", out)
}
func (a *MetricsAPI) versions(w http.ResponseWriter, r *http.Request, s authorizedActor) error {
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
	scope := strings.Join([]string{"metric_versions", s.ID, p, id}, ":")
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
	items, e := a.Service.ListMetricVersions(r.Context(), s.ID, p, id, after, limit)
	if e != nil {
		return e
	}
	out := struct {
		Items      []reporting.MetricVersion `json:"items"`
		NextCursor *string                   `json:"next_cursor"`
	}{Items: []reporting.MetricVersion{}}
	if len(items) > limit {
		next := a.API.Cursor.Encode(scope, strconv.FormatInt(items[limit-1].Version, 10))
		out.NextCursor = &next
		items = items[:limit]
	}
	out.Items = append(out.Items, items...)
	return a.API.Boundary.JSON(w, 200, "MetricVersionList", out)
}
