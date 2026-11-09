package control

import (
	"context"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/howtomakeaname/Evia/backend/internal/delivery"
	"github.com/howtomakeaname/Evia/backend/internal/gen/wire"
	"github.com/howtomakeaname/Evia/backend/internal/platform/fault"
)

type DeliveryService interface {
	GetActivation(context.Context, string, string, string) (delivery.Activation, error)
	Activate(context.Context, string, string, string, delivery.ActivationInput) (delivery.Activation, error)
	PublicConfig(context.Context, string, delivery.Authentication, int64) (delivery.ConfigResult, error)
	AuthorizeConfig(context.Context, string, delivery.Authentication) (string, error)
}

func (a *API) registerDelivery(mux *http.ServeMux) {
	if a.Delivery == nil {
		return
	}
	mux.HandleFunc("GET /api/v1/projects/{project_id}/release-activations", a.projectAuth(false, a.getActivation))
	mux.HandleFunc("POST /api/v1/projects/{project_id}/release-activations", a.projectAuth(true, a.activateRelease))
	mux.HandleFunc("GET /v1/config", a.Boundary.Handler(a.publicConfig))
	mux.HandleFunc("OPTIONS /v1/config", a.Boundary.Handler(a.preflightConfig))
}
func (a *API) activateRelease(w http.ResponseWriter, r *http.Request, s authorizedActor) error {
	project, e := a.catalogProject(r, s, true)
	if e != nil {
		return e
	}
	if _, e = catalogQuery(r, false); e != nil {
		return e
	}
	keys := r.Header.Values("Idempotency-Key")
	if len(keys) != 1 {
		return delivery.Invalid("INVALID_IDEMPOTENCY_KEY", "必须提供单一幂等键")
	}
	var in wire.ActivationInput
	if e = a.Boundary.Decode(w, r, "ActivationInput", &in); e != nil {
		return e
	}
	out, e := a.Delivery.Activate(r.Context(), s.ID, project, keys[0], delivery.ActivationInput{Environment: in.Environment, ReleaseID: in.ReleaseID, ExpectedActiveRevision: in.ExpectedActiveRevision})
	if e != nil {
		return e
	}
	return a.Boundary.JSON(w, 200, "Activation", wire.Activation{Environment: out.Environment, ActiveReleaseRevision: out.ActiveReleaseRevision})
}
func configQuery(r *http.Request) (string, int64, error) {
	if len(r.URL.RawQuery) > 2048 {
		return "", 0, delivery.Invalid("INVALID_CONFIG_QUERY", "配置查询参数过长")
	}
	q, e := url.ParseQuery(r.URL.RawQuery)
	if e != nil {
		return "", 0, delivery.Invalid("INVALID_CONFIG_QUERY", "配置查询参数无效")
	}
	for key, values := range q {
		if (key != "write_key" && key != "current_release_revision") || len(values) != 1 {
			return "", 0, delivery.Invalid("INVALID_CONFIG_QUERY", "配置查询参数无效")
		}
	}
	key := q.Get("write_key")
	if len(key) < 1 || len(key) > 128 {
		return "", 0, delivery.Invalid("INVALID_CONFIG_QUERY", "请提供来源 write_key")
	}
	var revision int64
	if values, ok := q["current_release_revision"]; ok {
		value := values[0]
		if value == "" || strings.Trim(value, "0123456789") != "" || len(value) > 16 || (len(value) > 1 && value[0] == '0') {
			return "", 0, delivery.Invalid("INVALID_CONFIG_QUERY", "配置版本必须为安全的非负整数")
		}
		revision, e = strconv.ParseInt(value, 10, 64)
		if e != nil || revision > delivery.MaxRevision {
			return "", 0, delivery.Invalid("INVALID_CONFIG_QUERY", "配置版本无效")
		}
	}
	return key, revision, nil
}
func configAuthentication(r *http.Request) (delivery.Authentication, error) {
	var auth delivery.Authentication
	if len(r.Header.Values("Cookie")) > 0 {
		return auth, fault.New(fault.Forbidden, "COLLECTOR_COOKIE_FORBIDDEN", "公开来源接口不得接收网站或平台 Cookie")
	}
	if len(r.Header.Values("Origin")) > 1 || len(r.Header.Values("Authorization")) > 1 {
		return auth, delivery.Invalid("INVALID_AUTH_HEADERS", "来源验证请求头不可重复")
	}
	auth.Origin = r.Header.Get("Origin")
	if len(auth.Origin) > 2048 {
		return auth, delivery.Invalid("INVALID_AUTH_HEADERS", "来源验证请求头无效")
	}
	if value := r.Header.Get("Authorization"); value != "" {
		if !strings.HasPrefix(value, "Bearer ") || len(value) != 50 {
			return auth, fault.New(fault.Unauthenticated, "SOURCE_CREDENTIAL_INVALID", "来源凭据无效")
		}
		auth.Bearer = strings.TrimPrefix(value, "Bearer ")
	}
	return auth, nil
}
func configCORS(w http.ResponseWriter, origin string) {
	w.Header().Add("Vary", "Origin")
	w.Header().Add("Vary", "Authorization")
	if origin != "" {
		w.Header().Set("Access-Control-Allow-Origin", origin)
		w.Header().Set("Access-Control-Expose-Headers", "ETag, X-Request-ID")
	}
}
func (a *API) publicConfig(w http.ResponseWriter, r *http.Request) error {
	key, current, e := configQuery(r)
	if e != nil {
		return e
	}
	auth, e := configAuthentication(r)
	if e != nil {
		return e
	}
	if len(r.Header.Values("If-None-Match")) > 1 {
		return delivery.Invalid("INVALID_ETAG", "配置 ETag 不可重复")
	}
	etag := r.Header.Get("If-None-Match")
	if etag != "" && (len(etag) != 66 || etag[0] != '"' || etag[65] != '"') {
		return delivery.Invalid("INVALID_ETAG", "请提供单个配置 ETag")
	}
	out, e := a.Delivery.PublicConfig(r.Context(), key, auth, current)
	configCORS(w, out.AllowedOrigin)
	if e != nil {
		return e
	}
	w.Header().Set("ETag", out.ETag)
	// Source status, credential expiry and exact Origin have already been checked
	// on this request. An ETag never bypasses revocation or compatibility policy.
	if etag != "" && etag == out.ETag {
		w.WriteHeader(http.StatusNotModified)
		return nil
	}
	resource := wire.PublicConfig{Format: out.Config.Format, ProjectID: out.Config.ProjectID, Environment: out.Config.Environment, ReleaseRevision: out.Config.Revision}
	resource.Events = make([]struct {
		EventKey      string         `json:"event_key"`
		SchemaVersion int64          `json:"schema_version"`
		Schema        map[string]any `json:"schema"`
	}, 0, len(out.Config.Events))
	for _, event := range out.Config.Events {
		resource.Events = append(resource.Events, struct {
			EventKey      string         `json:"event_key"`
			SchemaVersion int64          `json:"schema_version"`
			Schema        map[string]any `json:"schema"`
		}{EventKey: event.Key, SchemaVersion: event.Version, Schema: event.Schema})
	}
	return a.Boundary.JSON(w, 200, "PublicConfig", resource)
}
func (a *API) preflightConfig(w http.ResponseWriter, r *http.Request) error {
	key, _, e := configQuery(r)
	if e != nil {
		return e
	}
	auth, e := configAuthentication(r)
	if e != nil {
		return e
	}
	if auth.Origin == "" || r.Header.Get("Access-Control-Request-Method") != "GET" || len(r.Header.Values("Access-Control-Request-Method")) != 1 || len(r.Header.Values("Access-Control-Request-Headers")) > 1 {
		return delivery.Invalid("INVALID_PREFLIGHT", "预检请求无效")
	}
	if requested := r.Header.Get("Access-Control-Request-Headers"); requested != "" {
		for _, header := range strings.Split(requested, ",") {
			if strings.ToLower(strings.TrimSpace(header)) != "if-none-match" {
				return fault.New(fault.Forbidden, "CORS_HEADER_FORBIDDEN", "来源请求头不被允许")
			}
		}
	}
	origin, e := a.Delivery.AuthorizeConfig(r.Context(), key, auth)
	if e != nil {
		return e
	}
	configCORS(w, origin)
	w.Header().Set("Access-Control-Allow-Methods", "GET")
	w.Header().Set("Access-Control-Allow-Headers", "If-None-Match")
	w.Header().Set("Access-Control-Max-Age", "60")
	w.WriteHeader(http.StatusNoContent)
	return nil
}

func (a *API) getActivation(w http.ResponseWriter, r *http.Request, s authorizedActor) error {
	project := r.PathValue("project_id")
	if !catalogUUID.MatchString(project) {
		return delivery.Invalid("INVALID_PROJECT_ID", "项目标识格式不正确")
	}
	if len(r.URL.RawQuery) > 2048 {
		return delivery.Invalid("INVALID_QUERY", "查询参数过长")
	}
	q, e := url.ParseQuery(r.URL.RawQuery)
	if e != nil {
		return delivery.Invalid("INVALID_QUERY", "查询参数编码无效")
	}
	for key, values := range q {
		if key != "environment" || len(values) != 1 {
			return delivery.Invalid("INVALID_QUERY", "查询参数不符合接口定义")
		}
	}
	environment := q.Get("environment")
	if environment != "development" && environment != "staging" && environment != "production" {
		return delivery.Invalid("INVALID_ENVIRONMENT", "请提供有效环境")
	}
	out, e := a.Delivery.GetActivation(r.Context(), s.ID, project, environment)
	if e != nil {
		return e
	}
	return a.Boundary.JSON(w, 200, "Activation", wire.Activation{Environment: out.Environment, ActiveReleaseRevision: out.ActiveReleaseRevision})
}
