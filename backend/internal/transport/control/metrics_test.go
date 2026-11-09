package control

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/howtomakeaname/Evia/backend/internal/access"
	"github.com/howtomakeaname/Evia/backend/internal/analysis"
	"github.com/howtomakeaname/Evia/backend/internal/platform/contract"
	"github.com/howtomakeaname/Evia/backend/internal/platform/cursor"
	"github.com/howtomakeaname/Evia/backend/internal/platform/fault"
	"github.com/howtomakeaname/Evia/backend/internal/platform/httpapi"
	"github.com/howtomakeaname/Evia/backend/internal/reporting"
)

const metricID = "55555555-5555-4555-8555-555555555555"
const metricBase = "/api/v1/projects/" + projectID + "/metrics"
const metricItem = metricBase + "/" + metricID
const metricDefinition = `{"title":"Daily opens","description":"","event_versions":[{"event_key":"page_opened","schema_version":1}],"subject":"uid","aggregation":{"op":"count"},"unit":"event"}`
const metricCreateBody = `{"environment":"development","definition":` + metricDefinition + `}`
const metricValidateBody = `{"draft_revision":1,"expected_published_version":0}`
const metricExecuteBody = `{"time_range":{"from":"2026-11-01T00:00:00Z","to":"2026-11-02T00:00:00Z"},"timezone":"UTC","interval":"day"}`

// This service double verifies transport behavior only. Persistence, atomicity,
// idempotency replay and real query execution require the separate database tests.
type metricHTTPService struct {
	reporting.MetricService
	calls                       int
	err                         error
	after, environment, key, id string
	revision, version           int64
	limit                       int
	execution                   reporting.MetricExecute
	items                       []reporting.MetricRecord
	versions                    []reporting.MetricVersion
}

func (f *metricHTTPService) call(actor, project string) error {
	if actor == "" || project != projectID {
		panic("unscoped metric service")
	}
	f.calls++
	return f.err
}
func metricResource() reporting.MetricRecord {
	return reporting.MetricRecord{ID: metricID, ProjectID: projectID, Environment: "development", Title: "Daily opens", DraftRevision: 1, CreatedAt: time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC), UpdatedAt: time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)}
}
func metricDraft() reporting.MetricDraft {
	var def reporting.MetricDefinition
	if e := json.Unmarshal([]byte(metricDefinition), &def); e != nil {
		panic(e)
	}
	return reporting.MetricDraft{MetricRecord: metricResource(), Definition: def}
}
func metricVersionFixture(v int64) reporting.MetricVersion {
	return reporting.MetricVersion{MetricID: metricID, ProjectID: projectID, Environment: "development", Version: v, DraftRevision: 1, Definition: metricDraft().Definition, Dependencies: reporting.DependencyManifest{ProjectID: projectID, Environment: "development", SelectionMode: "event_versions", UnfixedEventKeys: []string{}, EventVersions: []reporting.Dependency{{EventKey: "page_opened", SchemaVersion: 1, DefinitionHash: strings.Repeat("a", 64)}}}, DefinitionHash: strings.Repeat("b", 64), DependencyHash: strings.Repeat("c", 64), ValidationHash: strings.Repeat("d", 64), CreatedAt: metricResource().CreatedAt, CreatedBy: userID}
}
func (f *metricHTTPService) CreateMetric(_ context.Context, actor, project, key string, in reporting.MetricCreate) (reporting.MetricDraft, error) {
	f.key, f.environment = key, in.Environment
	return metricDraft(), f.call(actor, project)
}
func (f *metricHTTPService) ListMetrics(_ context.Context, actor, project, env, after string, limit int) ([]reporting.MetricRecord, error) {
	f.environment, f.after, f.limit = env, after, limit
	return f.items, f.call(actor, project)
}
func (f *metricHTTPService) GetMetric(_ context.Context, actor, project, id string) (reporting.MetricRecord, error) {
	f.id = id
	return metricResource(), f.call(actor, project)
}
func (f *metricHTTPService) GetMetricDraft(_ context.Context, actor, project, id string) (reporting.MetricDraft, error) {
	f.id = id
	return metricDraft(), f.call(actor, project)
}
func (f *metricHTTPService) SaveMetricDraft(_ context.Context, actor, project, id string, revision int64, def reporting.MetricDefinition) (reporting.MetricDraft, error) {
	f.id, f.revision = id, revision
	out := metricDraft()
	out.DraftRevision = revision + 1
	out.Definition = def
	return out, f.call(actor, project)
}
func (f *metricHTTPService) ValidateMetric(_ context.Context, actor, project, id string, in reporting.MetricValidate) (reporting.MetricValidation, error) {
	f.id, f.revision = id, in.DraftRevision
	v := metricVersionFixture(1)
	return reporting.MetricValidation{Valid: true, ExecutionPerformed: false, DraftRevision: in.DraftRevision, PublishedVersion: in.ExpectedPublishedVersion, NormalizedDefinition: v.Definition, Dependencies: v.Dependencies, DefinitionHash: v.DefinitionHash, DependencyHash: v.DependencyHash, ValidationHash: v.ValidationHash, Diff: []reporting.MetricChange{}}, f.call(actor, project)
}
func (f *metricHTTPService) PublishMetric(_ context.Context, actor, project, id, key string, in reporting.MetricPublish) (reporting.MetricVersion, error) {
	f.id, f.key, f.revision = id, key, in.DraftRevision
	return metricVersionFixture(1), f.call(actor, project)
}
func (f *metricHTTPService) ListMetricVersions(_ context.Context, actor, project, id string, after int64, limit int) ([]reporting.MetricVersion, error) {
	f.id, f.version, f.limit = id, after, limit
	return f.versions, f.call(actor, project)
}
func (f *metricHTTPService) GetMetricVersion(_ context.Context, actor, project, id string, version int64) (reporting.MetricVersion, error) {
	f.id, f.version = id, version
	return metricVersionFixture(version), f.call(actor, project)
}
func (f *metricHTTPService) EnqueueMetricVersion(_ context.Context, actor, project, id string, version int64, key string, in reporting.MetricExecute) (analysis.Job, error) {
	f.id, f.version, f.key, f.execution = id, version, key, in
	return analysis.Job{ID: metricID, State: "queued", CreatedAt: metricResource().CreatedAt}, f.call(actor, project)
}
func metricHTTP(t *testing.T, f *metricHTTPService, access AccessService, machines ...MachineService) (http.Handler, *cursor.Codec) {
	t.Helper()
	v, e := contract.New()
	if e != nil {
		t.Fatal(e)
	}
	codec, e := cursor.New([]byte(strings.Repeat("s", 32)))
	if e != nil {
		t.Fatal(e)
	}
	a := &API{Boundary: &httpapi.Boundary{Validator: v, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))}, Access: access, Cursor: codec, Origin: "https://evia.example", Metrics: f}
	if len(machines) > 0 {
		a.Machines = machines[0]
	}
	return a.Handler(), codec
}
func TestMetricHTTPRoutesContractsAndRevisions(t *testing.T) {
	f := &metricHTTPService{}
	access := &catalogAccess{fakeAccess: &fakeAccess{}}
	h, _ := metricHTTP(t, f, access)
	for _, tc := range []struct {
		method, path, body string
		headers            map[string]string
		status             int
		write              bool
	}{
		{"POST", metricBase, metricCreateBody, map[string]string{"Idempotency-Key": "create-key"}, 201, true},
		{"GET", metricItem, "", nil, 200, false},
		{"GET", metricItem + "/draft", "", nil, 200, false},
		{"PATCH", metricItem + "/draft", `{"definition":` + metricDefinition + `}`, map[string]string{"If-Match": `"1"`}, 200, true},
		{"POST", metricItem + "/validations", metricValidateBody, nil, 200, true},
		{"POST", metricItem + "/versions", `{"draft_revision":1,"expected_published_version":0,"validation_hash":"` + strings.Repeat("d", 64) + `"}`, map[string]string{"Idempotency-Key": "publish-key"}, 201, true},
		{"GET", metricItem + "/versions/1", "", nil, 200, false},
		{"POST", metricItem + "/versions/1/queries", metricExecuteBody, map[string]string{"Idempotency-Key": "execute-key"}, 202, false},
	} {
		t.Run(tc.method+tc.path, func(t *testing.T) {
			before := f.calls
			w := catalogRequest(h, tc.method, tc.path, tc.body, tc.headers)
			catalogStatus(t, w, tc.status)
			if f.calls != before+1 {
				t.Fatal("missing service dispatch")
			}
			if access.writes[len(access.writes)-1] != tc.write {
				t.Fatal("incorrect role policy")
			}
			if strings.HasSuffix(tc.path, "/draft") && w.Header().Get("ETag") == "" {
				t.Fatal("missing draft revision ETag")
			}
		})
	}
	if f.id != metricID || f.version != 1 || f.key != "execute-key" || f.revision != 1 || f.execution.Timezone != "UTC" || f.execution.Interval != "day" {
		t.Fatal("scope or precondition not forwarded", f)
	}
}
func TestMetricHTTPStrictInputs(t *testing.T) {
	for _, tc := range []struct {
		name, method, path, body string
		headers                  map[string]string
		status                   int
	}{
		{"missing env", "GET", metricBase, "", nil, 400},
		{"unknown env", "GET", metricBase + "?environment=unknown", "", nil, 400},
		{"duplicate env", "GET", metricBase + "?environment=development&environment=production", "", nil, 400},
		{"overlong query", "GET", metricBase + "?environment=development&cursor=" + strings.Repeat("x", 8192), "", nil, 400},
		{"unknown parameter", "GET", metricBase + "?environment=development&search=x", "", nil, 400},
		{"invalid query encoding", "GET", metricBase + "?environment=%xx", "", nil, 400},
		{"analysis limit", "GET", metricBase + "?environment=development&limit=101", "", nil, 400},
		{"version limit", "GET", metricItem + "/versions?limit=51", "", nil, 400},
		{"duplicate limit", "GET", metricItem + "/versions?limit=1&limit=2", "", nil, 400},
		{"item env override", "GET", metricItem + "?environment=production", "", nil, 400},
		{"versions env override", "GET", metricItem + "/versions?environment=production", "", nil, 400},
		{"invalid UUID", "GET", metricBase + "/wrong", "", nil, 400},
		{"invalid project UUID", "GET", "/api/v1/projects/wrong/metrics?environment=development", "", nil, 400},
		{"noncanonical version", "GET", metricItem + "/versions/01", "", nil, 400},
		{"negative version", "GET", metricItem + "/versions/-1", "", nil, 400},
		{"unsafe version", "GET", metricItem + "/versions/9007199254740992", "", nil, 400},
		{"create needs key", "POST", metricBase, metricCreateBody, nil, 400},
		{"execute needs key", "POST", metricItem + "/versions/1/queries", metricExecuteBody, nil, 400},
		{"invalid key", "POST", metricBase, metricCreateBody, map[string]string{"Idempotency-Key": "contains space"}, 400},
		{"PATCH needs revision", "PATCH", metricItem + "/draft", `{"definition":` + metricDefinition + `}`, nil, 400},
		{"noncanonical revision", "PATCH", metricItem + "/draft", `{"definition":` + metricDefinition + `}`, map[string]string{"If-Match": `"01"`}, 400},
		{"wildcard revision", "PATCH", metricItem + "/draft", `{"definition":` + metricDefinition + `}`, map[string]string{"If-Match": `*`}, 400},
		{"weak revision", "PATCH", metricItem + "/draft", `{"definition":` + metricDefinition + `}`, map[string]string{"If-Match": `W/"1"`}, 400},
		{"unsafe revision", "PATCH", metricItem + "/draft", `{"definition":` + metricDefinition + `}`, map[string]string{"If-Match": `"9007199254740992"`}, 400},
		{"unknown body field", "POST", metricItem + "/validations", `{"draft_revision":1,"expected_published_version":0,"extra":true}`, nil, 400},
		{"duplicate JSON", "POST", metricItem + "/validations", `{"draft_revision":1,"draft_revision":2,"expected_published_version":0}`, nil, 400},
		{"null definition", "POST", metricBase, `{"environment":"development","definition":null}`, map[string]string{"Idempotency-Key": "key"}, 400},
		{"trailing JSON", "POST", metricItem + "/validations", metricValidateBody + `{}`, nil, 400},
		{"invalid interval", "POST", metricItem + "/versions/1/queries", strings.Replace(metricExecuteBody, `"interval":"day"`, `"interval":"week"`, 1), map[string]string{"Idempotency-Key": "key"}, 400},
		{"definition runtime context", "POST", metricBase, strings.Replace(metricCreateBody, `"subject":"uid"`, `"subject":"uid","environment":"production"`, 1), map[string]string{"Idempotency-Key": "key"}, 400},
		{"execute env override body", "POST", metricItem + "/versions/1/queries", strings.TrimSuffix(metricExecuteBody, "}") + `,"environment":"production"}`, map[string]string{"Idempotency-Key": "key"}, 400},
		{"CSRF validation", "POST", metricItem + "/validations", metricValidateBody, map[string]string{"X-CSRF-Token": ""}, 403},
		{"CSRF execution", "POST", metricItem + "/versions/1/queries", metricExecuteBody, map[string]string{"X-CSRF-Token": "", "Idempotency-Key": "key"}, 403},
		{"origin", "POST", metricBase, metricCreateBody, map[string]string{"Origin": "https://invalid.example", "Idempotency-Key": "key"}, 403},
		{"wrong method", "DELETE", metricItem, "", nil, 405},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := &metricHTTPService{}
			h, _ := metricHTTP(t, f, &catalogAccess{fakeAccess: &fakeAccess{}})
			catalogStatus(t, catalogRequest(h, tc.method, tc.path, tc.body, tc.headers), tc.status)
			if f.calls != 0 {
				t.Fatal("invalid input reached service")
			}
		})
	}
}
func TestMetricHTTPCursorsBoundScopeAndPosition(t *testing.T) {
	f := &metricHTTPService{items: []reporting.MetricRecord{metricResource(), metricResource()}, versions: []reporting.MetricVersion{metricVersionFixture(1), metricVersionFixture(2)}}
	h, codec := metricHTTP(t, f, &catalogAccess{fakeAccess: &fakeAccess{}})
	for _, tc := range []struct {
		path, scope, position string
		versions              bool
	}{
		{metricBase + "?environment=development&limit=1", "metrics:" + userID + ":" + projectID + ":development", metricID, false},
		{metricItem + "/versions?limit=1", "metric_versions:" + userID + ":" + projectID + ":" + metricID, "1", true},
	} {
		page := catalogStatus(t, catalogRequest(h, "GET", tc.path, "", nil), 200)
		next, ok := page["next_cursor"].(string)
		if !ok || next == "" || len(page["items"].([]any)) != 1 {
			t.Fatal("missing bounded continuation", page)
		}
		decoded, e := codec.Decode(tc.scope, next)
		if e != nil || decoded != tc.position {
			t.Fatal("incorrect cursor position", decoded, e)
		}
		catalogStatus(t, catalogRequest(h, "GET", tc.path+"&cursor="+url.QueryEscape(next), "", nil), 200)
		if tc.versions && f.version != 1 || !tc.versions && f.after != metricID {
			t.Fatal("continuation not forwarded")
		}
		for _, scope := range []string{strings.Replace(tc.scope, userID, metricID, 1), strings.Replace(tc.scope, projectID, metricID, 1), tc.scope + ":other", strings.Replace(tc.scope, "development", "production", 1), strings.Replace(tc.scope, metricID, projectID, 1)} {
			if scope == tc.scope {
				continue
			}
			before := f.calls
			bad := codec.Encode(scope, tc.position)
			out := catalogStatus(t, catalogRequest(h, "GET", tc.path+"&cursor="+url.QueryEscape(bad), "", nil), 400)
			if out["code"] != "INVALID_CURSOR" || f.calls != before {
				t.Fatal("foreign cursor reached service", out)
			}
		}
		for _, position := range []string{"bad-position", "-1", "9007199254740992"} {
			before := f.calls
			bad := codec.Encode(tc.scope, position)
			catalogStatus(t, catalogRequest(h, "GET", tc.path+"&cursor="+url.QueryEscape(bad), "", nil), 400)
			if f.calls != before {
				t.Fatal("invalid signed position reached service")
			}
		}
	}
}
func TestMetricHTTPAuthRolesAndSafeFailures(t *testing.T) {
	f := &metricHTTPService{}
	h, _ := metricHTTP(t, f, &catalogAccess{fakeAccess: &fakeAccess{}})
	r := httptest.NewRequest("GET", metricItem, nil)
	r.Header.Set("Authorization", "Bearer source-key")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	catalogStatus(t, w, 401)
	if f.calls != 0 {
		t.Fatal("source key authorized management")
	}
	for _, tc := range []struct {
		err    error
		status int
		code   string
	}{
		{fault.New(fault.NotFound, "METRIC_NOT_FOUND", "指标不存在"), 404, "METRIC_NOT_FOUND"},
		{fault.New(fault.Conflict, "METRIC_REVISION_CONFLICT", "版本冲突"), 409, "METRIC_REVISION_CONFLICT"},
		{fault.New(fault.Conflict, "QUERY_REQUEST_EXPIRED", "请求已过期"), 409, "QUERY_REQUEST_EXPIRED"},
		{fault.New(fault.Unavailable, "QUERY_UNAVAILABLE", "执行暂不可用"), 503, "QUERY_UNAVAILABLE"},
		{errors.New("private database details"), 500, "INTERNAL_ERROR"},
	} {
		f.err = tc.err
		out := catalogStatus(t, catalogRequest(h, "POST", metricItem+"/versions/1/queries", metricExecuteBody, map[string]string{"Idempotency-Key": "key"}), tc.status)
		if out["code"] != tc.code || strings.Contains(out["message"].(string), "private") {
			t.Fatal("unsafe or incorrect error", out)
		}
	}
	deny := &catalogAccess{fakeAccess: &fakeAccess{}, deny: fault.New(fault.Forbidden, "PROJECT_FORBIDDEN", "无权访问")}
	h, _ = metricHTTP(t, f, deny)
	before := f.calls
	catalogStatus(t, catalogRequest(h, "GET", metricItem, "", nil), 403)
	if f.calls != before {
		t.Fatal("project denial reached service")
	}
}

// Role authorization is owned by the shared access layer. This double exercises
// that metric routes request write policy for draft/publication writes.
type metricViewerAccess struct{ *catalogAccess }

func (a *metricViewerAccess) Authorize(ctx context.Context, actor, project string, write bool) error {
	if e := a.catalogAccess.Authorize(ctx, actor, project, write); e != nil {
		return e
	}
	if write {
		return fault.New(fault.Forbidden, "PROJECT_FORBIDDEN", "项目为只读")
	}
	return nil
}
func TestMetricHTTPViewerAggregateAccess(t *testing.T) {
	f := &metricHTTPService{}
	a := &metricViewerAccess{&catalogAccess{fakeAccess: &fakeAccess{}}}
	h, _ := metricHTTP(t, f, a)
	for _, tc := range []struct {
		method, path, body string
		headers            map[string]string
		status             int
	}{
		{"GET", metricItem, "", nil, 200},
		{"POST", metricItem + "/validations", metricValidateBody, nil, 403},
		{"POST", metricItem + "/versions/1/queries", metricExecuteBody, map[string]string{"Idempotency-Key": "execute-key"}, 202},
		{"POST", metricBase, metricCreateBody, map[string]string{"Idempotency-Key": "create-key"}, 403},
		{"PATCH", metricItem + "/draft", `{"definition":` + metricDefinition + `}`, map[string]string{"If-Match": `"1"`}, 403},
		{"POST", metricItem + "/versions", `{"draft_revision":1,"expected_published_version":0,"validation_hash":"` + strings.Repeat("d", 64) + `"}`, map[string]string{"Idempotency-Key": "publish-key"}, 403},
	} {
		before := f.calls
		catalogStatus(t, catalogRequest(h, tc.method, tc.path, tc.body, tc.headers), tc.status)
		if tc.status == 403 && f.calls != before {
			t.Fatal("read-only mutation reached service")
		}
	}
}

func TestMetricHTTPRejectsAmbiguousHeaders(t *testing.T) {
	for _, tc := range []struct{ method, path, body, header, value string }{
		{"POST", metricBase, metricCreateBody, "Idempotency-Key", "key"},
		{"PATCH", metricItem + "/draft", `{"definition":` + metricDefinition + `}`, "If-Match", `"1"`},
		{"POST", metricItem + "/versions/1/queries", metricExecuteBody, "Idempotency-Key", "key"},
	} {
		f := &metricHTTPService{}
		h, _ := metricHTTP(t, f, &catalogAccess{fakeAccess: &fakeAccess{}})
		r := httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.body))
		r.AddCookie(&http.Cookie{Name: "evia_session", Value: "valid"})
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("X-CSRF-Token", "csrf")
		r.Header.Add(tc.header, tc.value)
		r.Header.Add(tc.header, tc.value)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		catalogStatus(t, w, 400)
		if f.calls != 0 {
			t.Fatal("ambiguous header reached service")
		}
	}
}

func TestMetricHTTPListDefaultsBoundsAndResponseContract(t *testing.T) {
	f := &metricHTTPService{}
	h, _ := metricHTTP(t, f, &catalogAccess{fakeAccess: &fakeAccess{}})
	for _, path := range []string{metricBase + "?environment=development", metricItem + "/versions"} {
		out := catalogStatus(t, catalogRequest(h, "GET", path, "", nil), 200)
		if f.limit != 20 || out["next_cursor"] != nil || len(out["items"].([]any)) != 0 {
			t.Fatal("incorrect default or empty page", out, f.limit)
		}
	}
	for _, tc := range []struct {
		path  string
		limit int
	}{{metricBase + "?environment=development&limit=100", 100}, {metricItem + "/versions?limit=50", 50}} {
		catalogStatus(t, catalogRequest(h, "GET", tc.path, "", nil), 200)
		if f.limit != tc.limit {
			t.Fatal("page bound not forwarded")
		}
	}
	// A successful application return still has to satisfy the response schema.
	invalid := metricResource()
	invalid.ID = "private-invalid-response"
	f.items = []reporting.MetricRecord{invalid}
	out := catalogStatus(t, catalogRequest(h, "GET", metricBase+"?environment=development", "", nil), 500)
	raw, _ := json.Marshal(out)
	if strings.Contains(string(raw), "private-invalid-response") {
		t.Fatal("invalid resource leaked through response boundary")
	}
}

func TestMetricHTTPAuthenticationAndFailuresOnEveryRoute(t *testing.T) {
	for _, route := range []struct{ method, path, body string }{
		{"GET", metricBase + "?environment=development", ""},
		{"POST", metricBase, metricCreateBody},
		{"GET", metricItem, ""},
		{"GET", metricItem + "/draft", ""},
		{"PATCH", metricItem + "/draft", `{"definition":` + metricDefinition + `}`},
		{"POST", metricItem + "/validations", metricValidateBody},
		{"GET", metricItem + "/versions", ""},
		{"POST", metricItem + "/versions", `{"draft_revision":1,"expected_published_version":0,"validation_hash":"` + strings.Repeat("d", 64) + `"}`},
		{"GET", metricItem + "/versions/1", ""},
		{"POST", metricItem + "/versions/1/queries", metricExecuteBody},
	} {
		t.Run(route.method+route.path, func(t *testing.T) {
			f := &metricHTTPService{}
			h, _ := metricHTTP(t, f, &catalogAccess{fakeAccess: &fakeAccess{}})
			for _, mixed := range []bool{false, true} {
				r := httptest.NewRequest(route.method, route.path, strings.NewReader(route.body))
				if mixed {
					r.AddCookie(&http.Cookie{Name: "evia_session", Value: "valid"})
					r.Header.Set("Authorization", "Bearer synthetic-machine-token")
				}
				w := httptest.NewRecorder()
				h.ServeHTTP(w, r)
				catalogStatus(t, w, 401)
				if f.calls != 0 {
					t.Fatal("unauthenticated or mixed authentication reached the service")
				}
			}
			f.err = errors.New("private metrics storage error")
			out := catalogStatus(t, catalogRequest(h, route.method, route.path, route.body, map[string]string{"Idempotency-Key": "key", "If-Match": `"1"`}), 500)
			if out["code"] != "INTERNAL_ERROR" || f.calls != 1 || strings.Contains(out["message"].(string), "private") {
				t.Fatal("service failure was not safely mapped", out, f.calls)
			}
		})
	}
}

func TestMetricHTTPBodyBoundaryAndCanonicalETags(t *testing.T) {
	f := &metricHTTPService{}
	h, _ := metricHTTP(t, f, &catalogAccess{fakeAccess: &fakeAccess{}})
	for _, tc := range []struct {
		method, path, body, revision, etag string
		status                             int
	}{
		{"POST", metricBase, metricCreateBody, "", `"1"`, 201},
		{"GET", metricItem + "/draft", "", "", `"1"`, 200},
		{"PATCH", metricItem + "/draft", `{"definition":` + metricDefinition + `}`, `"1"`, `"2"`, 200},
	} {
		w := catalogRequest(h, tc.method, tc.path, tc.body, map[string]string{"Idempotency-Key": "key", "If-Match": tc.revision})
		out := catalogStatus(t, w, tc.status)
		if w.Header().Get("ETag") != tc.etag || out["metric_id"] != metricID || out["analysis_id"] != nil {
			t.Fatal("metric response or revision does not match its wire contract", out, w.Header())
		}
	}
	before := f.calls
	for _, tc := range []struct {
		body, contentType, code string
		status                  int
	}{
		{strings.Repeat(" ", 256*1024+1), "application/json", "BODY_TOO_LARGE", 413},
		{metricCreateBody, "text/plain", "INVALID_CONTENT_TYPE", 415},
		{`{"environment":"development",`, "application/json", "INVALID_JSON", 400},
	} {
		out := catalogStatus(t, catalogRequest(h, "POST", metricBase, tc.body, map[string]string{"Idempotency-Key": "key", "Content-Type": tc.contentType}), tc.status)
		if out["code"] != tc.code || f.calls != before {
			t.Fatal("invalid body reached the metric service", out)
		}
	}
}

type metricMachines struct {
	MachineService
	permissions []string
}

func (m *metricMachines) AuthenticateMachine(context.Context, string) (access.Identity, error) {
	return access.Identity{PrincipalID: machinePrincipalID, CredentialID: machineCredentialID, ProjectID: projectID, Environment: "development", Permissions: m.permissions}, nil
}

func TestMetricHTTPRegisteredRoutesRequireExactMachinePermission(t *testing.T) {
	for _, route := range []struct {
		method, path, body, permission string
		status                         int
	}{
		{"GET", metricBase + "?environment=development", "", access.PermissionMetricsRead, 200},
		{"POST", metricBase, metricCreateBody, access.PermissionMetricsWrite, 201},
		{"GET", metricItem, "", access.PermissionMetricsRead, 200},
		{"GET", metricItem + "/draft", "", access.PermissionMetricsRead, 200},
		{"PATCH", metricItem + "/draft", `{"definition":` + metricDefinition + `}`, access.PermissionMetricsWrite, 200},
		{"POST", metricItem + "/validations", metricValidateBody, access.PermissionMetricsWrite, 200},
		{"GET", metricItem + "/versions", "", access.PermissionMetricsRead, 200},
		{"POST", metricItem + "/versions", `{"draft_revision":1,"expected_published_version":0,"validation_hash":"` + strings.Repeat("d", 64) + `"}`, access.PermissionMetricsPublish, 201},
		{"GET", metricItem + "/versions/1", "", access.PermissionMetricsRead, 200},
		{"POST", metricItem + "/versions/1/queries", metricExecuteBody, access.PermissionMetricsRun, 202},
	} {
		t.Run(route.method+route.path, func(t *testing.T) {
			f := &metricHTTPService{}
			machines := &metricMachines{}
			h, _ := metricHTTP(t, f, &catalogAccess{fakeAccess: &fakeAccess{}}, machines)
			for _, permission := range []string{route.permission, "", "analyses.read", "analyses.write", "analyses.publish", "analyses.run", "queries.read", "queries.write", access.PermissionMetricsRead, access.PermissionMetricsWrite, access.PermissionMetricsPublish, access.PermissionMetricsRun} {
				machines.permissions = []string{permission}
				r := httptest.NewRequest(route.method, route.path, strings.NewReader(route.body))
				r.Header.Set("Authorization", "Bearer "+machineToken)
				r.Header.Set("Content-Type", "application/json")
				r.Header.Set("Idempotency-Key", "key")
				r.Header.Set("If-Match", `"1"`)
				before := f.calls
				w := httptest.NewRecorder()
				h.ServeHTTP(w, r)
				if permission == route.permission {
					catalogStatus(t, w, route.status)
					if f.calls != before+1 {
						t.Fatal("exact machine grant failed to dispatch")
					}
				} else {
					out := catalogStatus(t, w, 403)
					if f.calls != before || out["code"] != "MACHINE_FORBIDDEN" {
						t.Fatal("unrelated permission authorized a metric operation", permission, out)
					}
				}
			}
		})
	}
}
