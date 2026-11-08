package control

import (
	"context"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"github.com/howtomakeaname/Evia/backend/internal/access"
	"github.com/howtomakeaname/Evia/backend/internal/catalog"
	"github.com/howtomakeaname/Evia/backend/internal/gen/wire"
	"github.com/howtomakeaname/Evia/backend/internal/platform/httpapi"
)

type CatalogService interface {
	CreateEvent(context.Context, string, string, catalog.EventInput) (catalog.Event, error)
	ListEvents(context.Context, string, string, string, string, int) ([]catalog.Event, error)
	GetEventVersion(context.Context, string, string, string, int64) (catalog.EventVersion, error)
	GetDraft(context.Context, string, string, string) (catalog.Draft, error)
	SaveDraft(context.Context, string, string, string, int64, catalog.DraftInput) (catalog.Draft, error)
	ValidateSample(context.Context, string, string, string, int64, map[string]any) (catalog.Validation, error)
	CreateRelease(context.Context, string, string, string, catalog.ReleaseInput) (catalog.Release, error)
	GetRelease(context.Context, string, string, string) (catalog.Release, error)
	ListReleases(context.Context, string, string, string, string, int) ([]catalog.Release, error)
}

var catalogUUID = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
var catalogEventKey = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)
var catalogETag = regexp.MustCompile(`^"[1-9][0-9]{0,15}"$`)

func (a *API) registerCatalog(mux *http.ServeMux) {
	if a.Catalog == nil {
		return
	}
	const events = "/api/v1/projects/{project_id}/event-definitions"
	const releases = "/api/v1/projects/{project_id}/releases"
	mux.HandleFunc("GET "+events, a.auth(false, a.listEvents))
	mux.HandleFunc("POST "+events, a.auth(true, a.createEvent))
	mux.HandleFunc("GET "+events+"/{event_key}/versions/{schema_version}", a.auth(false, a.getEventVersion))
	mux.HandleFunc("GET "+events+"/{event_key}/draft", a.auth(false, a.getDraft))
	mux.HandleFunc("PATCH "+events+"/{event_key}/draft", a.auth(true, a.saveDraft))
	mux.HandleFunc("POST "+events+"/{event_key}/validate", a.auth(true, a.validateSample))
	mux.HandleFunc("GET "+releases, a.auth(false, a.listReleases))
	mux.HandleFunc("POST "+releases, a.auth(true, a.createRelease))
	mux.HandleFunc("GET "+releases+"/{release_id}", a.auth(false, a.getRelease))
}

// Project authorization precedes resource access. Stores also recheck membership
// in their transactions so revoked privileges cannot survive this HTTP check.
func (a *API) catalogProject(r *http.Request, s access.Session, write bool) (string, error) {
	p := r.PathValue("project_id")
	if !catalogUUID.MatchString(p) {
		return "", catalog.Invalid("INVALID_PROJECT_ID", "项目标识格式不正确")
	}
	if k := r.PathValue("event_key"); k != "" && !catalogEventKey.MatchString(k) {
		return "", catalog.Invalid("INVALID_EVENT_KEY", "事件标识格式不正确")
	}
	if id := r.PathValue("release_id"); id != "" && !catalogUUID.MatchString(id) {
		return "", catalog.Invalid("INVALID_RELEASE_ID", "发布标识格式不正确")
	}
	if e := a.Access.Authorize(r.Context(), s.UserID, p, write); e != nil {
		return "", e
	}
	return p, nil
}
func catalogQuery(r *http.Request, list bool) (url.Values, error) {
	if len(r.URL.RawQuery) > 8192 {
		return nil, catalog.Invalid("INVALID_QUERY", "查询参数过长")
	}
	q, e := url.ParseQuery(r.URL.RawQuery)
	if e != nil {
		return nil, catalog.Invalid("INVALID_QUERY", "查询参数不符合接口定义")
	}
	for k, values := range q {
		if !list || (k != "cursor" && k != "limit" && k != "environment") || len(values) != 1 || (k == "limit" && values[0] == "") {
			return nil, catalog.Invalid("INVALID_QUERY", "查询参数不符合接口定义")
		}
	}
	if values, ok := q["environment"]; ok && !catalog.ValidEnvironment(values[0]) {
		return nil, catalog.Invalid("INVALID_ENVIRONMENT", "环境无效", "/environment")
	}
	return q, nil
}
func (a *API) catalogPage(r *http.Request, s access.Session, project, operation string) (string, string, string, int, error) {
	q, e := catalogQuery(r, true)
	if e != nil {
		return "", "", "", 0, e
	}
	limit, e := httpapi.Limit(r)
	if e != nil {
		return "", "", "", 0, e
	}
	env := q.Get("environment")
	scope := strings.Join([]string{"catalog", operation, s.UserID, project, env}, ":")
	after, e := a.Cursor.Decode(scope, q.Get("cursor"))
	if e != nil || (after != "" && ((operation == "events" && !catalogEventKey.MatchString(after)) || (operation == "releases" && !catalogUUID.MatchString(after)))) {
		return "", "", "", 0, catalog.Invalid("INVALID_CURSOR", "分页游标已失效，请重新加载")
	}
	return env, scope, after, limit, nil
}
func (a *API) createEvent(w http.ResponseWriter, r *http.Request, s access.Session) error {
	p, e := a.catalogProject(r, s, true)
	if e != nil {
		return e
	}
	if _, e = catalogQuery(r, false); e != nil {
		return e
	}
	var in wire.EventInput
	if e = a.Boundary.Decode(w, r, "EventInput", &in); e != nil {
		return e
	}
	event, e := a.Catalog.CreateEvent(r.Context(), s.UserID, p, catalog.EventInput{Key: in.EventKey, DisplayName: in.DisplayName, Description: optionalValue(in.Description)})
	if e != nil {
		return e
	}
	return a.Boundary.JSON(w, 201, "EventDefinition", eventWire(event))
}
func (a *API) listEvents(w http.ResponseWriter, r *http.Request, s access.Session) error {
	p, e := a.catalogProject(r, s, false)
	if e != nil {
		return e
	}
	env, scope, after, limit, e := a.catalogPage(r, s, p, "events")
	if e != nil {
		return e
	}
	events, e := a.Catalog.ListEvents(r.Context(), s.UserID, p, env, after, limit)
	if e != nil {
		return e
	}
	out := wire.EventDefinitionList{Items: []wire.EventDefinition{}}
	if len(events) > limit {
		next := a.Cursor.Encode(scope, events[limit-1].Key)
		out.NextCursor = &next
		events = events[:limit]
	}
	for _, event := range events {
		out.Items = append(out.Items, eventWire(event))
	}
	return a.Boundary.JSON(w, 200, "EventDefinitionList", out)
}
func (a *API) getDraft(w http.ResponseWriter, r *http.Request, s access.Session) error {
	p, e := a.catalogProject(r, s, false)
	if e != nil {
		return e
	}
	if _, e = catalogQuery(r, false); e != nil {
		return e
	}
	draft, e := a.Catalog.GetDraft(r.Context(), s.UserID, p, r.PathValue("event_key"))
	if e != nil {
		return e
	}
	return a.draftResponse(w, draft)
}
func (a *API) saveDraft(w http.ResponseWriter, r *http.Request, s access.Session) error {
	p, e := a.catalogProject(r, s, true)
	if e != nil {
		return e
	}
	if _, e = catalogQuery(r, false); e != nil {
		return e
	}
	if len(r.Header.Values("If-Match")) != 1 || !catalogETag.MatchString(r.Header.Get("If-Match")) {
		return catalog.Invalid("REVISION_REQUIRED", "请提供单个带双引号的 If-Match 版本")
	}
	revision, e := httpapi.IfMatch(r)
	if e != nil {
		return e
	}
	var in wire.DraftInput
	if e = a.Boundary.Decode(w, r, "DraftInput", &in); e != nil {
		return e
	}
	draft, e := a.Catalog.SaveDraft(r.Context(), s.UserID, p, r.PathValue("event_key"), revision, draftInput(in))
	if e != nil {
		return e
	}
	return a.draftResponse(w, draft)
}
func (a *API) draftResponse(w http.ResponseWriter, draft catalog.Draft) error {
	w.Header().Set("ETag", strconv.Quote(strconv.FormatInt(draft.Revision, 10)))
	return a.Boundary.JSON(w, 200, "Draft", wire.Draft{Revision: draft.Revision, Definition: draftWire(draft.Definition)})
}
func (a *API) validateSample(w http.ResponseWriter, r *http.Request, s access.Session) error {
	p, e := a.catalogProject(r, s, true)
	if e != nil {
		return e
	}
	if _, e = catalogQuery(r, false); e != nil {
		return e
	}
	var in wire.ValidationInput
	if e = a.Boundary.Decode(w, r, "ValidationInput", &in); e != nil {
		return e
	}
	result, e := a.Catalog.ValidateSample(r.Context(), s.UserID, p, r.PathValue("event_key"), in.DraftRevision, in.SampleProperties)
	if e != nil {
		return e
	}
	out := wire.ValidationResult{Valid: result.Valid, Compatibility: result.Compatibility}
	out.Issues = make([]struct {
		Code    string `json:"code"`
		Path    string `json:"path"`
		Message string `json:"message"`
	}, 0, len(result.Issues))
	for _, issue := range result.Issues {
		out.Issues = append(out.Issues, struct {
			Code    string `json:"code"`
			Path    string `json:"path"`
			Message string `json:"message"`
		}{issue.Code, issue.Path, issue.Message})
	}
	return a.Boundary.JSON(w, 200, "ValidationResult", out)
}
func (a *API) createRelease(w http.ResponseWriter, r *http.Request, s access.Session) error {
	p, e := a.catalogProject(r, s, true)
	if e != nil {
		return e
	}
	if _, e = catalogQuery(r, false); e != nil {
		return e
	}
	keys := r.Header.Values("Idempotency-Key")
	if len(keys) != 1 || len(keys[0]) < 1 || len(keys[0]) > 128 || strings.IndexFunc(keys[0], func(c rune) bool { return c < 33 || c > 126 }) >= 0 {
		return catalog.Invalid("INVALID_IDEMPOTENCY_KEY", "请提供单个 1 到 128 个可见 ASCII 字符的幂等键")
	}
	var in wire.ReleaseInput
	if e = a.Boundary.Decode(w, r, "ReleaseInput", &in); e != nil {
		return e
	}
	input := catalog.ReleaseInput{Environment: in.Environment, ExpectedActiveRevision: in.ExpectedActiveRevision, Events: make([]catalog.ReleaseEvent, 0, len(in.Events))}
	for _, event := range in.Events {
		input.Events = append(input.Events, catalog.ReleaseEvent{Key: event.EventKey, DraftRevision: event.DraftRevision})
	}
	release, e := a.Catalog.CreateRelease(r.Context(), s.UserID, p, keys[0], input)
	if e != nil {
		return e
	}
	return a.Boundary.JSON(w, 202, "Release", releaseWire(release))
}
func (a *API) getRelease(w http.ResponseWriter, r *http.Request, s access.Session) error {
	p, e := a.catalogProject(r, s, false)
	if e != nil {
		return e
	}
	if _, e = catalogQuery(r, false); e != nil {
		return e
	}
	release, e := a.Catalog.GetRelease(r.Context(), s.UserID, p, r.PathValue("release_id"))
	if e != nil {
		return e
	}
	return a.Boundary.JSON(w, 200, "Release", releaseWire(release))
}
func (a *API) listReleases(w http.ResponseWriter, r *http.Request, s access.Session) error {
	p, e := a.catalogProject(r, s, false)
	if e != nil {
		return e
	}
	env, scope, after, limit, e := a.catalogPage(r, s, p, "releases")
	if e != nil {
		return e
	}
	releases, e := a.Catalog.ListReleases(r.Context(), s.UserID, p, env, after, limit)
	if e != nil {
		return e
	}
	out := wire.ReleaseList{Items: []wire.Release{}}
	if len(releases) > limit {
		next := a.Cursor.Encode(scope, releases[limit-1].ID)
		out.NextCursor = &next
		releases = releases[:limit]
	}
	for _, release := range releases {
		out.Items = append(out.Items, releaseWire(release))
	}
	return a.Boundary.JSON(w, 200, "ReleaseList", out)
}
func optionalValue(v *string) string {
	if v == nil {
		return ""
	}
	return *v
}
func optionalString(v string) *string {
	if v == "" {
		return nil
	}
	return &v
}
func eventWire(v catalog.Event) wire.EventDefinition {
	versions := v.PublishedVersions
	if versions == nil {
		versions = []int64{}
	}
	return wire.EventDefinition{EventTypeID: v.TypeID, EventKey: v.Key, DisplayName: v.DisplayName, PublishedVersions: versions}
}
func draftInput(v wire.DraftInput) catalog.DraftInput {
	return catalog.DraftInput{DisplayName: v.DisplayName, Schema: v.Schema, FieldMetadata: v.FieldMetadata, Trigger: v.Trigger, AllowedSourceKinds: v.AllowedSourceKinds, FactKind: v.FactKind, ExampleProperties: v.ExampleProperties}
}
func draftWire(v catalog.DraftInput) wire.DraftInput {
	return wire.DraftInput{DisplayName: v.DisplayName, Schema: v.Schema, FieldMetadata: v.FieldMetadata, Trigger: v.Trigger, AllowedSourceKinds: v.AllowedSourceKinds, FactKind: v.FactKind, ExampleProperties: v.ExampleProperties}
}
func releaseWire(v catalog.Release) wire.Release {
	return wire.Release{ReleaseID: v.ID, Revision: v.Revision, Environment: v.Environment, State: v.State, IngestionReady: v.IngestionReady, AccelerationState: v.AccelerationState, ManifestHash: optionalString(v.ManifestHash)}
}

var _ CatalogService = (*catalog.Service)(nil)

func (a *API) getEventVersion(w http.ResponseWriter, r *http.Request, s access.Session) error {
	project, e := a.catalogProject(r, s, false)
	if e != nil {
		return e
	}
	if _, e = catalogQuery(r, false); e != nil {
		return e
	}
	value := r.PathValue("schema_version")
	version, e := strconv.ParseInt(value, 10, 64)
	if e != nil || version < 1 || version > catalog.MaxInteger || strconv.FormatInt(version, 10) != value {
		return catalog.Invalid("INVALID_SCHEMA_VERSION", "事件版本必须为安全的正整数")
	}
	out, e := a.Catalog.GetEventVersion(r.Context(), s.UserID, project, r.PathValue("event_key"), version)
	if e != nil {
		return e
	}
	return a.Boundary.JSON(w, 200, "EventVersion", out)
}
