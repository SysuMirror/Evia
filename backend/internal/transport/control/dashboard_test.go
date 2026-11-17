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
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/howtomakeaname/Evia/backend/internal/analysis"
	"github.com/howtomakeaname/Evia/backend/internal/platform/contract"
	"github.com/howtomakeaname/Evia/backend/internal/platform/cursor"
	"github.com/howtomakeaname/Evia/backend/internal/platform/fault"
	"github.com/howtomakeaname/Evia/backend/internal/platform/httpapi"
	"github.com/howtomakeaname/Evia/backend/internal/reporting"
)

const dashboardID = "55555555-5555-4555-8555-555555555555"
const dashboardWidgetOne = "11111111-1111-4111-8111-111111111111"
const dashboardBase = "/api/v1/projects/" + projectID + "/dashboards"
const dashboardItem = dashboardBase + "/" + dashboardID
const dashboardDefinition = `{"title":"Daily opens","description":"","widgets":[{"widget_id":"11111111-1111-4111-8111-111111111111","analysis_id":"33333333-3333-4333-8333-333333333333","analysis_version":1,"title":"Daily opens","reading_order":0,"layout":{"x":0,"y":0,"width":12,"height":4}}]}`
const dashboardCreateBody = `{"environment":"development","definition":` + dashboardDefinition + `}`
const dashboardValidateBody = `{"draft_revision":1,"expected_published_version":0}`
const dashboardExecuteBody = `{"time_range":{"from":"2026-11-01T00:00:00Z","to":"2026-11-02T00:00:00Z"},"timezone":"UTC"}`

// This service double verifies transport behavior only. Persistence, atomicity,
// idempotency replay and real query execution require the separate database tests.
type dashboardHTTPService struct {
	reporting.DashboardService
	calls                       int
	err                         error
	after, environment, key, id string
	revision, version           int64
	limit                       int
	archivedOnly                bool
	folderScope, folderID       string
	search                      string
	targetEnvironment           string
	rebindings                  []reporting.DashboardRebinding
	items                       []reporting.Dashboard
	versions                    []reporting.DashboardVersion
}

func (f *dashboardHTTPService) call(actor, project string) error {
	if actor == "" || project != projectID {
		panic("unscoped dashboard analysis service")
	}
	f.calls++
	return f.err
}
func dashboardResource() reporting.Dashboard {
	return reporting.Dashboard{ID: dashboardID, ProjectID: projectID, Environment: "development", Title: "Daily opens", DraftRevision: 1, CreatedAt: time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC), UpdatedAt: time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)}
}
func dashboardDraft() reporting.DashboardDraft {
	var def reporting.DashboardDefinition
	if e := json.Unmarshal([]byte(dashboardDefinition), &def); e != nil {
		panic(e)
	}
	return reporting.DashboardDraft{Dashboard: dashboardResource(), Definition: def}
}
func dashboardDependencies() reporting.DashboardDependencies {
	return reporting.DashboardDependencies{ProjectID: projectID, Environment: "development", Analyses: []reporting.DashboardDependency{{WidgetID: "11111111-1111-4111-8111-111111111111", AnalysisID: "33333333-3333-4333-8333-333333333333", AnalysisVersion: 1, DefinitionHash: strings.Repeat("a", 64), DependencyHash: strings.Repeat("b", 64), ValidationHash: strings.Repeat("c", 64)}}}
}
func dashboardVersionFixture(v int64) reporting.DashboardVersion {
	return reporting.DashboardVersion{DashboardID: dashboardID, ProjectID: projectID, Environment: "development", Version: v, DraftRevision: 1, Definition: dashboardDraft().Definition, Dependencies: dashboardDependencies(), DefinitionHash: strings.Repeat("b", 64), DependencyHash: strings.Repeat("c", 64), ValidationHash: strings.Repeat("d", 64), CreatedAt: dashboardResource().CreatedAt, CreatedBy: userID}
}
func dashboardRunFixture() reporting.DashboardRun {
	return reporting.DashboardRun{ID: dashboardID, DashboardID: dashboardID, ProjectID: projectID, Environment: "development", Version: 1, CreatedBy: userID, TimeRange: analysis.TimeRange{From: dashboardResource().CreatedAt, To: dashboardResource().CreatedAt.Add(time.Hour)}, Timezone: "UTC", State: "queued", CreatedAt: dashboardResource().CreatedAt, ExpiresAt: dashboardResource().CreatedAt.Add(time.Hour), Widgets: []reporting.DashboardWidgetRun{{WidgetID: "11111111-1111-4111-8111-111111111111", AnalysisID: "33333333-3333-4333-8333-333333333333", AnalysisVersion: 1, Query: analysis.Job{ID: dashboardID, State: "queued", CreatedAt: dashboardResource().CreatedAt}}}}
}
func (f *dashboardHTTPService) CreateDashboard(_ context.Context, actor, project, key string, in reporting.DashboardCreate) (reporting.DashboardDraft, error) {
	f.key, f.environment = key, in.Environment
	return dashboardDraft(), f.call(actor, project)
}
func (f *dashboardHTTPService) ListDashboards(_ context.Context, actor, project, env, after string, limit int, archivedOnly bool, folderScope, search string) ([]reporting.Dashboard, error) {
	f.environment, f.after, f.limit, f.archivedOnly, f.folderScope, f.search = env, after, limit, archivedOnly, folderScope, search
	return f.items, f.call(actor, project)
}
func (f *dashboardHTTPService) GetDashboard(_ context.Context, actor, project, id string) (reporting.Dashboard, error) {
	f.id = id
	return dashboardResource(), f.call(actor, project)
}
func (f *dashboardHTTPService) GetDashboardDraft(_ context.Context, actor, project, id string) (reporting.DashboardDraft, error) {
	f.id = id
	return dashboardDraft(), f.call(actor, project)
}
func (f *dashboardHTTPService) SaveDashboardDraft(_ context.Context, actor, project, id string, revision int64, def reporting.DashboardDefinition, folderID string) (reporting.DashboardDraft, error) {
	f.id, f.revision, f.folderID = id, revision, folderID
	out := dashboardDraft()
	out.DraftRevision = revision + 1
	out.Definition = def
	return out, f.call(actor, project)
}
func (f *dashboardHTTPService) ValidateDashboard(_ context.Context, actor, project, id string, in reporting.DashboardValidate) (reporting.DashboardValidation, error) {
	f.id, f.revision = id, in.DraftRevision
	v := dashboardVersionFixture(1)
	return reporting.DashboardValidation{Valid: true, ExecutionPerformed: false, DraftRevision: in.DraftRevision, PublishedVersion: in.ExpectedPublishedVersion, NormalizedDefinition: v.Definition, Dependencies: v.Dependencies, DefinitionHash: v.DefinitionHash, DependencyHash: v.DependencyHash, ValidationHash: v.ValidationHash, Diff: []reporting.DashboardChange{}}, f.call(actor, project)
}
func (f *dashboardHTTPService) PublishDashboard(_ context.Context, actor, project, id, key string, in reporting.DashboardPublish) (reporting.DashboardVersion, error) {
	f.id, f.key, f.revision = id, key, in.DraftRevision
	return dashboardVersionFixture(1), f.call(actor, project)
}
func (f *dashboardHTTPService) ListDashboardVersions(_ context.Context, actor, project, id string, after int64, limit int) ([]reporting.DashboardVersion, error) {
	f.id, f.version, f.limit = id, after, limit
	return f.versions, f.call(actor, project)
}
func (f *dashboardHTTPService) GetDashboardVersion(_ context.Context, actor, project, id string, version int64) (reporting.DashboardVersion, error) {
	f.id, f.version = id, version
	return dashboardVersionFixture(version), f.call(actor, project)
}
func (f *dashboardHTTPService) EnqueueDashboardRun(_ context.Context, actor, project, id string, version int64, key string, in reporting.DashboardExecute) (reporting.DashboardRun, error) {
	f.id, f.version, f.key = id, version, key
	return dashboardRunFixture(), f.call(actor, project)
}
func (f *dashboardHTTPService) CopyDashboard(_ context.Context, actor, project, id, key string, in reporting.DashboardCopy) (reporting.DashboardDraft, error) {
	f.id, f.key = id, key
	f.targetEnvironment = in.TargetEnvironment
	f.rebindings = in.Rebindings
	out := dashboardDraft()
	out.Title = in.Title
	out.FolderID = in.FolderID
	return out, f.call(actor, project)
}
func dashboardHTTP(t *testing.T, f *dashboardHTTPService, access AccessService) (http.Handler, *cursor.Codec) {
	t.Helper()
	v, e := contract.New()
	if e != nil {
		t.Fatal(e)
	}
	codec, e := cursor.New([]byte(strings.Repeat("s", 32)))
	if e != nil {
		t.Fatal(e)
	}
	a := &API{Boundary: &httpapi.Boundary{Validator: v, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))}, Access: access, Cursor: codec, Origin: "https://evia.example", Dashboards: f}
	return a.Handler(), codec
}
func TestDashboardHTTPRoutesContractsAndRevisions(t *testing.T) {
	f := &dashboardHTTPService{}
	access := &catalogAccess{fakeAccess: &fakeAccess{}}
	h, _ := dashboardHTTP(t, f, access)
	for _, tc := range []struct {
		method, path, body string
		headers            map[string]string
		status             int
		write              bool
	}{
		{"POST", dashboardBase, dashboardCreateBody, map[string]string{"Idempotency-Key": "create-key"}, 201, true},
		{"GET", dashboardItem, "", nil, 200, false},
		{"GET", dashboardItem + "/draft", "", nil, 200, false},
		{"PATCH", dashboardItem + "/draft", `{"definition":` + dashboardDefinition + `}`, map[string]string{"If-Match": `"1"`}, 200, true},
		{"POST", dashboardItem + "/validations", dashboardValidateBody, nil, 200, false},
		{"POST", dashboardItem + "/versions", `{"draft_revision":1,"expected_published_version":0,"validation_hash":"` + strings.Repeat("d", 64) + `"}`, map[string]string{"Idempotency-Key": "publish-key"}, 201, true},
		{"GET", dashboardItem + "/versions/1", "", nil, 200, false},
		{"POST", dashboardItem + "/versions/1/runs", dashboardExecuteBody, map[string]string{"Idempotency-Key": "execute-key"}, 202, false},
		{"POST", dashboardItem + "/copy", `{"title":"Copied dashboard"}`, map[string]string{"Idempotency-Key": "copy-key"}, 201, true},
		{"POST", dashboardItem + "/archive", "", nil, 200, true},
		{"POST", dashboardItem + "/unarchive", "", nil, 200, true},
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
	if f.id != dashboardID || f.version != 1 || f.key != "copy-key" || f.revision != 1 {
		t.Fatal("scope or precondition not forwarded", f)
	}
}

func TestDashboardHTTPCrossEnvironmentCopy(t *testing.T) {
	f := &dashboardHTTPService{}
	access := &catalogAccess{fakeAccess: &fakeAccess{}}
	h, _ := dashboardHTTP(t, f, access)
	body := `{"title":"Staging copy","target_environment":"staging","rebindings":[` +
		`{"widget_id":"11111111-1111-4111-8111-111111111111","analysis_id":"33333333-3333-4333-8333-333333333333","analysis_version":4},` +
		`{"widget_id":"22222222-2222-4222-8222-222222222222","metric_id":"55555555-5555-4555-8555-555555555555","metric_version":5}]}`
	w := catalogRequest(h, "POST", dashboardItem+"/copy", body, map[string]string{"Idempotency-Key": "cross-key"})
	catalogStatus(t, w, 201)
	if f.targetEnvironment != "staging" || len(f.rebindings) != 2 {
		t.Fatalf("cross-env copy not decoded to the service: env=%q rebindings=%d", f.targetEnvironment, len(f.rebindings))
	}
	if f.rebindings[0].AnalysisID != "33333333-3333-4333-8333-333333333333" || f.rebindings[0].AnalysisVersion != 4 {
		t.Fatalf("analysis rebinding not forwarded: %+v", f.rebindings[0])
	}
	if f.rebindings[1].MetricID != "55555555-5555-4555-8555-555555555555" || f.rebindings[1].MetricVersion != 5 {
		t.Fatalf("metric rebinding not forwarded: %+v", f.rebindings[1])
	}
}
func TestDashboardHTTPStrictInputs(t *testing.T) {
	for _, tc := range []struct {
		name, method, path, body string
		headers                  map[string]string
		status                   int
	}{
		{"missing env", "GET", dashboardBase, "", nil, 400},
		{"unknown env", "GET", dashboardBase + "?environment=unknown", "", nil, 400},
		{"duplicate env", "GET", dashboardBase + "?environment=development&environment=production", "", nil, 400},
		{"unknown parameter", "GET", dashboardBase + "?environment=development&search=x", "", nil, 400},
		{"invalid query encoding", "GET", dashboardBase + "?environment=%xx", "", nil, 400},
		{"analysis limit", "GET", dashboardBase + "?environment=development&limit=101", "", nil, 400},
		{"version limit", "GET", dashboardItem + "/versions?limit=51", "", nil, 400},
		{"duplicate limit", "GET", dashboardItem + "/versions?limit=1&limit=2", "", nil, 400},
		{"item env override", "GET", dashboardItem + "?environment=production", "", nil, 400},
		{"versions env override", "GET", dashboardItem + "/versions?environment=production", "", nil, 400},
		{"invalid UUID", "GET", dashboardBase + "/wrong", "", nil, 400},
		{"invalid project UUID", "GET", "/api/v1/projects/wrong/dashboards?environment=development", "", nil, 400},
		{"noncanonical version", "GET", dashboardItem + "/versions/01", "", nil, 400},
		{"negative version", "GET", dashboardItem + "/versions/-1", "", nil, 400},
		{"unsafe version", "GET", dashboardItem + "/versions/9007199254740992", "", nil, 400},
		{"create needs key", "POST", dashboardBase, dashboardCreateBody, nil, 400},
		{"execute needs key", "POST", dashboardItem + "/versions/1/runs", dashboardExecuteBody, nil, 400},
		{"copy needs key", "POST", dashboardItem + "/copy", `{"title":"Copied"}`, nil, 400},
		{"copy empty title", "POST", dashboardItem + "/copy", `{"title":""}`, map[string]string{"Idempotency-Key": "copy-bad"}, 400},
		{"copy unknown field", "POST", dashboardItem + "/copy", `{"title":"Copied","environment":"production"}`, map[string]string{"Idempotency-Key": "copy-bad"}, 400},
		{"copy bad target environment", "POST", dashboardItem + "/copy", `{"title":"Copied","target_environment":"sandbox"}`, map[string]string{"Idempotency-Key": "copy-bad"}, 400},
		{"copy rebinding unknown field", "POST", dashboardItem + "/copy", `{"title":"Copied","target_environment":"staging","rebindings":[{"widget_id":"11111111-1111-4111-8111-111111111111","analysis_id":"33333333-3333-4333-8333-333333333333","analysis_version":1,"extra":true}]}`, map[string]string{"Idempotency-Key": "copy-bad"}, 400},
		{"copy rebinding bad version", "POST", dashboardItem + "/copy", `{"title":"Copied","target_environment":"staging","rebindings":[{"widget_id":"11111111-1111-4111-8111-111111111111","analysis_id":"33333333-3333-4333-8333-333333333333","analysis_version":0}]}`, map[string]string{"Idempotency-Key": "copy-bad"}, 400},
		{"invalid key", "POST", dashboardBase, dashboardCreateBody, map[string]string{"Idempotency-Key": "contains space"}, 400},
		{"PATCH needs revision", "PATCH", dashboardItem + "/draft", `{"definition":` + dashboardDefinition + `}`, nil, 400},
		{"weak revision", "PATCH", dashboardItem + "/draft", `{"definition":` + dashboardDefinition + `}`, map[string]string{"If-Match": `W/"1"`}, 400},
		{"unsafe revision", "PATCH", dashboardItem + "/draft", `{"definition":` + dashboardDefinition + `}`, map[string]string{"If-Match": `"9007199254740992"`}, 400},
		{"unknown body field", "POST", dashboardItem + "/validations", `{"draft_revision":1,"expected_published_version":0,"extra":true}`, nil, 400},
		{"duplicate JSON", "POST", dashboardItem + "/validations", `{"draft_revision":1,"draft_revision":2,"expected_published_version":0}`, nil, 400},
		{"trailing JSON", "POST", dashboardItem + "/validations", dashboardValidateBody + `{}`, nil, 400},
		{"runtime context in definition", "POST", dashboardBase, strings.Replace(dashboardCreateBody, `"widgets":`, `"environment":"production","widgets":`, 1), map[string]string{"Idempotency-Key": "key"}, 400},
		{"result copy in definition", "POST", dashboardBase, strings.Replace(dashboardCreateBody, `"widgets":`, `"rows":[],"widgets":`, 1), map[string]string{"Idempotency-Key": "key"}, 400},
		{"executable widget options", "POST", dashboardBase, strings.Replace(dashboardCreateBody, `"reading_order":`, `"formatter":"code","reading_order":`, 1), map[string]string{"Idempotency-Key": "key"}, 400},
		{"execute env override body", "POST", dashboardItem + "/versions/1/runs", strings.TrimSuffix(dashboardExecuteBody, "}") + `,"environment":"production"}`, map[string]string{"Idempotency-Key": "key"}, 400},
		{"CSRF validation", "POST", dashboardItem + "/validations", dashboardValidateBody, map[string]string{"X-CSRF-Token": ""}, 403},
		{"CSRF execution", "POST", dashboardItem + "/versions/1/runs", dashboardExecuteBody, map[string]string{"X-CSRF-Token": "", "Idempotency-Key": "key"}, 403},
		{"CSRF copy", "POST", dashboardItem + "/copy", `{"title":"Copied"}`, map[string]string{"X-CSRF-Token": "", "Idempotency-Key": "key"}, 403},
		{"origin", "POST", dashboardBase, dashboardCreateBody, map[string]string{"Origin": "https://invalid.example", "Idempotency-Key": "key"}, 403},
		{"wrong method", "DELETE", dashboardItem, "", nil, 405},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := &dashboardHTTPService{}
			h, _ := dashboardHTTP(t, f, &catalogAccess{fakeAccess: &fakeAccess{}})
			catalogStatus(t, catalogRequest(h, tc.method, tc.path, tc.body, tc.headers), tc.status)
			if f.calls != 0 {
				t.Fatal("invalid input reached service")
			}
		})
	}
}
func TestDashboardHTTPCursorsBoundScopeAndPosition(t *testing.T) {
	f := &dashboardHTTPService{items: []reporting.Dashboard{dashboardResource(), dashboardResource()}, versions: []reporting.DashboardVersion{dashboardVersionFixture(1), dashboardVersionFixture(2)}}
	h, codec := dashboardHTTP(t, f, &catalogAccess{fakeAccess: &fakeAccess{}})
	for _, tc := range []struct {
		path, scope, position string
		versions              bool
	}{
		{dashboardBase + "?environment=development&limit=1", "dashboards:" + userID + ":" + projectID + ":development:active::", dashboardID, false},
		{dashboardItem + "/versions?limit=1", "dashboard_versions:" + userID + ":" + projectID + ":" + dashboardID, "1", true},
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
		if tc.versions && f.version != 1 || !tc.versions && f.after != dashboardID {
			t.Fatal("continuation not forwarded")
		}
		for _, scope := range []string{strings.Replace(tc.scope, userID, dashboardID, 1), strings.Replace(tc.scope, projectID, dashboardID, 1), tc.scope + ":other", strings.Replace(tc.scope, "development", "production", 1), strings.Replace(tc.scope, dashboardID, projectID, 1)} {
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
func TestDashboardHTTPAuthRolesAndSafeFailures(t *testing.T) {
	f := &dashboardHTTPService{}
	h, _ := dashboardHTTP(t, f, &catalogAccess{fakeAccess: &fakeAccess{}})
	r := httptest.NewRequest("GET", dashboardItem, nil)
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
		{fault.New(fault.NotFound, "SAVED_ANALYSIS_NOT_FOUND", "保存分析不存在"), 404, "SAVED_ANALYSIS_NOT_FOUND"},
		{fault.New(fault.Conflict, "SAVED_ANALYSIS_REVISION_CONFLICT", "版本冲突"), 409, "SAVED_ANALYSIS_REVISION_CONFLICT"},
		{fault.New(fault.Conflict, "QUERY_REQUEST_EXPIRED", "请求已过期"), 409, "QUERY_REQUEST_EXPIRED"},
		{fault.New(fault.Unavailable, "QUERY_UNAVAILABLE", "执行暂不可用"), 503, "QUERY_UNAVAILABLE"},
		{errors.New("private database details"), 500, "INTERNAL_ERROR"},
	} {
		f.err = tc.err
		out := catalogStatus(t, catalogRequest(h, "POST", dashboardItem+"/versions/1/runs", dashboardExecuteBody, map[string]string{"Idempotency-Key": "key"}), tc.status)
		if out["code"] != tc.code || strings.Contains(out["message"].(string), "private") {
			t.Fatal("unsafe or incorrect error", out)
		}
	}
	deny := &catalogAccess{fakeAccess: &fakeAccess{}, deny: fault.New(fault.Forbidden, "PROJECT_FORBIDDEN", "无权访问")}
	h, _ = dashboardHTTP(t, f, deny)
	before := f.calls
	catalogStatus(t, catalogRequest(h, "GET", dashboardItem, "", nil), 403)
	if f.calls != before {
		t.Fatal("project denial reached service")
	}
}

// Role authorization is owned by the shared access layer. This double exercises
// that dashboard routes request write policy for draft/publication writes.
type dashboardViewerAccess struct{ *catalogAccess }

func (a *dashboardViewerAccess) Authorize(ctx context.Context, actor, project string, write bool) error {
	if e := a.catalogAccess.Authorize(ctx, actor, project, write); e != nil {
		return e
	}
	if write {
		return fault.New(fault.Forbidden, "PROJECT_FORBIDDEN", "项目为只读")
	}
	return nil
}
func TestDashboardHTTPViewerAggregateAccess(t *testing.T) {
	f := &dashboardHTTPService{}
	a := &dashboardViewerAccess{&catalogAccess{fakeAccess: &fakeAccess{}}}
	h, _ := dashboardHTTP(t, f, a)
	for _, tc := range []struct {
		method, path, body string
		headers            map[string]string
		status             int
	}{
		{"GET", dashboardItem, "", nil, 200},
		{"POST", dashboardItem + "/validations", dashboardValidateBody, nil, 200},
		{"POST", dashboardItem + "/versions/1/runs", dashboardExecuteBody, map[string]string{"Idempotency-Key": "execute-key"}, 202},
		{"POST", dashboardBase, dashboardCreateBody, map[string]string{"Idempotency-Key": "create-key"}, 403},
		{"PATCH", dashboardItem + "/draft", `{"definition":` + dashboardDefinition + `}`, map[string]string{"If-Match": `"1"`}, 403},
		{"POST", dashboardItem + "/versions", `{"draft_revision":1,"expected_published_version":0,"validation_hash":"` + strings.Repeat("d", 64) + `"}`, map[string]string{"Idempotency-Key": "publish-key"}, 403},
	} {
		before := f.calls
		catalogStatus(t, catalogRequest(h, tc.method, tc.path, tc.body, tc.headers), tc.status)
		if tc.status == 403 && f.calls != before {
			t.Fatal("read-only mutation reached service")
		}
	}
}

func TestDashboardHTTPRejectsAmbiguousHeaders(t *testing.T) {
	for _, tc := range []struct{ method, path, body, header, value string }{
		{"POST", dashboardBase, dashboardCreateBody, "Idempotency-Key", "key"},
		{"PATCH", dashboardItem + "/draft", `{"definition":` + dashboardDefinition + `}`, "If-Match", `"1"`},
		{"POST", dashboardItem + "/versions/1/runs", dashboardExecuteBody, "Idempotency-Key", "key"},
	} {
		f := &dashboardHTTPService{}
		h, _ := dashboardHTTP(t, f, &catalogAccess{fakeAccess: &fakeAccess{}})
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

func TestDashboardHTTPListDefaultsBoundsAndResponseContract(t *testing.T) {
	f := &dashboardHTTPService{}
	h, _ := dashboardHTTP(t, f, &catalogAccess{fakeAccess: &fakeAccess{}})
	for _, path := range []string{dashboardBase + "?environment=development", dashboardItem + "/versions"} {
		out := catalogStatus(t, catalogRequest(h, "GET", path, "", nil), 200)
		if f.limit != 20 || out["next_cursor"] != nil || len(out["items"].([]any)) != 0 {
			t.Fatal("incorrect default or empty page", out, f.limit)
		}
	}
	for _, tc := range []struct {
		path  string
		limit int
	}{{dashboardBase + "?environment=development&limit=100", 100}, {dashboardItem + "/versions?limit=50", 50}} {
		catalogStatus(t, catalogRequest(h, "GET", tc.path, "", nil), 200)
		if f.limit != tc.limit {
			t.Fatal("page bound not forwarded")
		}
	}
	// A successful application return still has to satisfy the response schema.
	invalid := dashboardResource()
	invalid.ID = "private-invalid-response"
	f.items = []reporting.Dashboard{invalid}
	out := catalogStatus(t, catalogRequest(h, "GET", dashboardBase+"?environment=development", "", nil), 500)
	raw, _ := json.Marshal(out)
	if strings.Contains(string(raw), "private-invalid-response") {
		t.Fatal("invalid resource leaked through response boundary")
	}
}

func (f *dashboardHTTPService) GetDashboardRun(_ context.Context, actor, project, id string) (reporting.DashboardRun, error) {
	f.id = id
	return dashboardRunFixture(), f.call(actor, project)
}
func (f *dashboardHTTPService) CancelDashboardRun(_ context.Context, actor, project, id string) (reporting.DashboardRun, error) {
	f.id = id
	return dashboardRunFixture(), f.call(actor, project)
}
func TestDashboardHTTPArchiveViewSelector(t *testing.T) {
	f := &dashboardHTTPService{items: []reporting.Dashboard{dashboardResource(), dashboardResource()}}
	h, codec := dashboardHTTP(t, f, &catalogAccess{fakeAccess: &fakeAccess{}})
	for _, tc := range []struct {
		path string
		want bool
	}{
		{dashboardBase + "?environment=development", false},
		{dashboardBase + "?environment=development&view=active", false},
		{dashboardBase + "?environment=development&view=archived", true},
	} {
		f.archivedOnly = !tc.want
		catalogStatus(t, catalogRequest(h, "GET", tc.path, "", nil), 200)
		if f.archivedOnly != tc.want {
			t.Fatalf("archivedOnly not forwarded for %s: got %v", tc.path, f.archivedOnly)
		}
	}
	// Unknown views and duplicate selectors fail closed before reaching the service.
	for _, path := range []string{
		dashboardBase + "?environment=development&view=bogus",
		dashboardBase + "?environment=development&view=archived&view=active",
		dashboardItem + "?view=archived",
	} {
		before := f.calls
		out := catalogStatus(t, catalogRequest(h, "GET", path, "", nil), 400)
		if f.calls != before {
			t.Fatal("invalid view reached service")
		}
		if path == dashboardItem+"?view=archived" && out["code"] != "INVALID_QUERY" {
			t.Fatal("item route must keep the strict allowlist", out)
		}
		if strings.Contains(path, "bogus") && out["code"] != "INVALID_ARCHIVE_VIEW" {
			t.Fatal("closed-set view must fail with INVALID_ARCHIVE_VIEW", out)
		}
	}
	// Cursors are bound to the view stream: an archived cursor cannot page active rows.
	activeScope := "dashboards:" + userID + ":" + projectID + ":development:active::"
	archivedScope := "dashboards:" + userID + ":" + projectID + ":development:archived::"
	archivedPage := catalogStatus(
		t,
		catalogRequest(h, "GET", dashboardBase+"?environment=development&view=archived&limit=1", "", nil),
		200,
	)
	next, ok := archivedPage["next_cursor"].(string)
	if !ok || next == "" {
		t.Fatal("missing archived continuation")
	}
	if decoded, e := codec.Decode(archivedScope, next); e != nil || decoded != dashboardID {
		t.Fatal("archived cursor bound to wrong scope", decoded, e)
	}
	before := f.calls
	bad := catalogStatus(
		t,
		catalogRequest(h, "GET", dashboardBase+"?environment=development&cursor="+url.QueryEscape(next), "", nil),
		400,
	)
	if bad["code"] != "INVALID_CURSOR" || f.calls != before {
		t.Fatal("cross-view cursor reached service", bad)
	}
	if _, e := codec.Decode(activeScope, next); e == nil {
		t.Fatal("archived cursor decoded under the active scope")
	}
}

func TestDashboardHTTPListSearchTerm(t *testing.T) {
	f := &dashboardHTTPService{items: []reporting.Dashboard{dashboardResource(), dashboardResource()}}
	h, codec := dashboardHTTP(t, f, &catalogAccess{fakeAccess: &fakeAccess{}})
	// A bounded search term reaches the service and binds the cursor scope.
	page := catalogStatus(
		t,
		catalogRequest(h, "GET", dashboardBase+"?environment=development&limit=1&q=%20%E7%95%99%E5%AD%98%20", "", nil),
		200,
	)
	if f.search != "留存" {
		t.Fatalf("search term must be trimmed and forwarded, got %q", f.search)
	}
	next, ok := page["next_cursor"].(string)
	if !ok || next == "" {
		t.Fatal("missing continuation cursor", page)
	}
	searchScope := "dashboards:" + userID + ":" + projectID + ":development:active::留存"
	if decoded, e := codec.Decode(searchScope, next); e != nil || decoded != dashboardID {
		t.Fatal("cursor must be bound to the search scope", decoded, e)
	}
	// Unknown query parameters stay rejected even though q is now allowlisted.
	catalogStatus(t, catalogRequest(h, "GET", dashboardBase+"?environment=development&q=x&unexpected=1", "", nil), 400)
	// An overlong term is rejected at the boundary without dispatching.
	before := f.calls
	catalogStatus(t, catalogRequest(h, "GET", dashboardBase+"?environment=development&q="+url.QueryEscape(strings.Repeat("字", 129)), "", nil), 400)
	if f.calls != before {
		t.Fatal("overlong search term reached the service")
	}
}
func (f *dashboardHTTPService) ArchiveDashboard(_ context.Context, actor, project, id string) (reporting.Dashboard, error) {
	f.id = id
	if len(f.items) > 0 {
		return f.items[0], f.call(actor, project)
	}
	return dashboardResource(), f.call(actor, project)
}
func (f *dashboardHTTPService) UnarchiveDashboard(_ context.Context, actor, project, id string) (reporting.Dashboard, error) {
	f.id = id
	if len(f.items) > 0 {
		return f.items[0], f.call(actor, project)
	}
	return dashboardResource(), f.call(actor, project)
}
func TestDashboardHTTPRunReadAndCancel(t *testing.T) {
	f := &dashboardHTTPService{}
	h, _ := dashboardHTTP(t, f, &catalogAccess{fakeAccess: &fakeAccess{}})
	for _, method := range []string{"GET", "DELETE"} {
		w := catalogRequest(h, method, "/api/v1/projects/"+projectID+"/dashboard-runs/"+dashboardID, "", nil)
		catalogStatus(t, w, 200)
	}
	if f.calls != 2 || f.id != dashboardID {
		t.Fatal(f)
	}
}

const parameterizedDashboardDefinition = `{"title":"Daily opens","description":"","parameters":[{"parameter_id":"section","label":"板块","type":"string","operator":"eq","required":false}],"widgets":[{"widget_id":"11111111-1111-4111-8111-111111111111","analysis_id":"33333333-3333-4333-8333-333333333333","analysis_version":1,"title":"Daily opens","reading_order":0,"layout":{"x":0,"y":0,"width":12,"height":4},"parameter_bindings":[{"parameter_id":"section","field":{"kind":"property","key":"section_id","event_key":"page_opened","schema_version":1}}]}]}`

func TestDashboardHTTPTextCardContracts(t *testing.T) {
	textCardDefinition := func(content string) string {
		return `{"title":"文本卡","description":"","widgets":[{"widget_id":"` + dashboardWidgetOne + `","title":"说明","reading_order":0,"layout":{"x":0,"y":0,"width":12,"height":3},"text_card":{"content":` + strconv.Quote(content) + `}}]}`
	}
	createBody := func(content string) string {
		return `{"environment":"development","definition":` + textCardDefinition(content) + `}`
	}
	updateBody := func(content string) string {
		return `{"definition":` + textCardDefinition(content) + `}`
	}
	validMarkdown := "# 标题\n\n[文档](https://example.com)"
	// A valid bounded Markdown text card passes the boundary on both admission
	// routes (create and draft save) and reaches the service.
	validService := &dashboardHTTPService{}
	validH, _ := dashboardHTTP(t, validService, &catalogAccess{fakeAccess: &fakeAccess{}})
	catalogStatus(t, catalogRequest(validH, "POST", dashboardBase, createBody(validMarkdown), map[string]string{"Idempotency-Key": "text-card-key"}), 201)
	catalogStatus(t, catalogRequest(validH, "PATCH", dashboardItem+"/draft", updateBody(validMarkdown), map[string]string{"If-Match": `"1"`}), 200)
	if validService.calls != 2 {
		t.Fatal("valid text card must dispatch once per admission route")
	}

	// Unsafe payloads are rejected before service dispatch on both routes.
	blockedService := &dashboardHTTPService{}
	blockedH, _ := dashboardHTTP(t, blockedService, &catalogAccess{fakeAccess: &fakeAccess{}})
	for _, content := range []string{
		`<script>alert(1)</script>`,
		`[x](javascript:alert(1))`,
		"![img](https://example.com/a.png)",
	} {
		catalogStatus(t, catalogRequest(blockedH, "POST", dashboardBase, createBody(content), map[string]string{"Idempotency-Key": "text-card-bad"}), 400)
		catalogStatus(t, catalogRequest(blockedH, "PATCH", dashboardItem+"/draft", updateBody(content), map[string]string{"If-Match": `"1"`}), 400)
	}
	if blockedService.calls != 0 {
		t.Fatal("unsafe text card reached service")
	}
}

func TestDashboardHTTPParameterContracts(t *testing.T) {
	// Valid parameterized definition and run values pass the boundary.
	f := &dashboardHTTPService{}
	h, _ := dashboardHTTP(t, f, &catalogAccess{fakeAccess: &fakeAccess{}})
	catalogStatus(t, catalogRequest(h, "POST", dashboardBase, `{"environment":"development","definition":`+parameterizedDashboardDefinition+`}`, map[string]string{"Idempotency-Key": "parameter-create"}), 201)
	runBody := `{"time_range":{"from":"2026-11-01T00:00:00Z","to":"2026-11-02T00:00:00Z"},"timezone":"UTC","parameter_values":{"section":"forum","limit":3,"vip":true}}`
	catalogStatus(t, catalogRequest(h, "POST", dashboardItem+"/versions/1/runs", runBody, map[string]string{"Idempotency-Key": "parameter-run"}), 202)

	for _, tc := range []struct {
		name, method, path, body string
		headers                  map[string]string
	}{
		{"null parameter value", "POST", dashboardItem + "/versions/1/runs", `{"time_range":{"from":"2026-11-01T00:00:00Z","to":"2026-11-02T00:00:00Z"},"timezone":"UTC","parameter_values":{"section":null}}`, map[string]string{"Idempotency-Key": "k1"}},
		{"object parameter value", "POST", dashboardItem + "/versions/1/runs", `{"time_range":{"from":"2026-11-01T00:00:00Z","to":"2026-11-02T00:00:00Z"},"timezone":"UTC","parameter_values":{"section":{"nested":1}}}`, map[string]string{"Idempotency-Key": "k2"}},
		{"unknown declaration type", "POST", dashboardBase, `{"environment":"development","definition":` + strings.Replace(parameterizedDashboardDefinition, `"type":"string"`, `"type":"json"`, 1) + `}`, map[string]string{"Idempotency-Key": "k3"}},
		{"unknown binding field kind", "POST", dashboardBase, `{"environment":"development","definition":` + strings.Replace(parameterizedDashboardDefinition, `"kind":"property"`, `"kind":"json"`, 1) + `}`, map[string]string{"Idempotency-Key": "k4"}},
		{"unknown declaration property", "POST", dashboardBase, `{"environment":"development","definition":` + strings.Replace(parameterizedDashboardDefinition, `"required":false`, `"required":false,"extra":1`, 1) + `}`, map[string]string{"Idempotency-Key": "k5"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			bad := &dashboardHTTPService{}
			badH, _ := dashboardHTTP(t, bad, &catalogAccess{fakeAccess: &fakeAccess{}})
			catalogStatus(t, catalogRequest(badH, tc.method, tc.path, tc.body, tc.headers), 400)
			if bad.calls != 0 {
				t.Fatal("invalid parameter contract reached service")
			}
		})
	}
}
