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

	"github.com/howtomakeaname/Evia/backend/internal/analysis"
	"github.com/howtomakeaname/Evia/backend/internal/platform/contract"
	"github.com/howtomakeaname/Evia/backend/internal/platform/cursor"
	"github.com/howtomakeaname/Evia/backend/internal/platform/fault"
	"github.com/howtomakeaname/Evia/backend/internal/platform/httpapi"
	"github.com/howtomakeaname/Evia/backend/internal/reporting"
)

const savedID = "55555555-5555-4555-8555-555555555555"
const savedBase = "/api/v1/projects/" + projectID + "/saved-analyses"
const savedItem = savedBase + "/" + savedID
const savedDefinition = `{"title":"Daily opens","description":"","query":{"kind":"trend","event_versions":[{"event_key":"page_opened","schema_version":1}],"metrics":[{"op":"count"}]},"visualization":{"kind":"metric"}}`
const savedCreateBody = `{"environment":"development","definition":` + savedDefinition + `}`
const savedValidateBody = `{"draft_revision":1,"expected_published_version":0}`
const savedExecuteBody = `{"time_range":{"from":"2026-11-01T00:00:00Z","to":"2026-11-02T00:00:00Z"},"timezone":"UTC"}`

// This service double verifies transport behavior only. Persistence, atomicity,
// idempotency replay and real query execution require the separate database tests.
type savedHTTPService struct {
	reporting.SavedAnalysisService
	calls                       int
	err                         error
	after, environment, key, id string
	revision, version           int64
	limit                       int
	items                       []reporting.SavedAnalysis
	versions                    []reporting.SavedAnalysisVersion
}

func (f *savedHTTPService) call(actor, project string) error {
	if actor == "" || project != projectID {
		panic("unscoped saved analysis service")
	}
	f.calls++
	return f.err
}
func savedResource() reporting.SavedAnalysis {
	return reporting.SavedAnalysis{ID: savedID, ProjectID: projectID, Environment: "development", Title: "Daily opens", Kind: "trend", DraftRevision: 1, CreatedAt: time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC), UpdatedAt: time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)}
}
func savedDraft() reporting.SavedAnalysisDraft {
	var def reporting.SavedAnalysisDefinition
	if e := json.Unmarshal([]byte(savedDefinition), &def); e != nil {
		panic(e)
	}
	return reporting.SavedAnalysisDraft{SavedAnalysis: savedResource(), Definition: def}
}
func savedVersionFixture(v int64) reporting.SavedAnalysisVersion {
	return reporting.SavedAnalysisVersion{AnalysisID: savedID, ProjectID: projectID, Environment: "development", Version: v, DraftRevision: 1, Definition: savedDraft().Definition, Dependencies: reporting.DependencyManifest{ProjectID: projectID, Environment: "development", SelectionMode: "event_versions", UnfixedEventKeys: []string{}, EventVersions: []reporting.Dependency{{EventKey: "page_opened", SchemaVersion: 1, DefinitionHash: strings.Repeat("a", 64)}}}, DefinitionHash: strings.Repeat("b", 64), DependencyHash: strings.Repeat("c", 64), ValidationHash: strings.Repeat("d", 64), CreatedAt: savedResource().CreatedAt, CreatedBy: userID}
}
func (f *savedHTTPService) CreateSavedAnalysis(_ context.Context, actor, project, key string, in reporting.SavedAnalysisCreate) (reporting.SavedAnalysisDraft, error) {
	f.key, f.environment = key, in.Environment
	return savedDraft(), f.call(actor, project)
}
func (f *savedHTTPService) ListSavedAnalyses(_ context.Context, actor, project, env, after string, limit int) ([]reporting.SavedAnalysis, error) {
	f.environment, f.after, f.limit = env, after, limit
	return f.items, f.call(actor, project)
}
func (f *savedHTTPService) GetSavedAnalysis(_ context.Context, actor, project, id string) (reporting.SavedAnalysis, error) {
	f.id = id
	return savedResource(), f.call(actor, project)
}
func (f *savedHTTPService) GetSavedAnalysisDraft(_ context.Context, actor, project, id string) (reporting.SavedAnalysisDraft, error) {
	f.id = id
	return savedDraft(), f.call(actor, project)
}
func (f *savedHTTPService) SaveSavedAnalysisDraft(_ context.Context, actor, project, id string, revision int64, def reporting.SavedAnalysisDefinition) (reporting.SavedAnalysisDraft, error) {
	f.id, f.revision = id, revision
	out := savedDraft()
	out.DraftRevision = revision + 1
	out.Definition = def
	return out, f.call(actor, project)
}
func (f *savedHTTPService) ValidateSavedAnalysis(_ context.Context, actor, project, id string, in reporting.SavedAnalysisValidate) (reporting.SavedAnalysisValidation, error) {
	f.id, f.revision = id, in.DraftRevision
	v := savedVersionFixture(1)
	return reporting.SavedAnalysisValidation{Valid: true, ExecutionPerformed: false, DraftRevision: in.DraftRevision, PublishedVersion: in.ExpectedPublishedVersion, NormalizedDefinition: v.Definition, Dependencies: v.Dependencies, DefinitionHash: v.DefinitionHash, DependencyHash: v.DependencyHash, ValidationHash: v.ValidationHash, Diff: []reporting.SavedAnalysisChange{}}, f.call(actor, project)
}
func (f *savedHTTPService) PublishSavedAnalysis(_ context.Context, actor, project, id, key string, in reporting.SavedAnalysisPublish) (reporting.SavedAnalysisVersion, error) {
	f.id, f.key, f.revision = id, key, in.DraftRevision
	return savedVersionFixture(1), f.call(actor, project)
}
func (f *savedHTTPService) ListSavedAnalysisVersions(_ context.Context, actor, project, id string, after int64, limit int) ([]reporting.SavedAnalysisVersion, error) {
	f.id, f.version, f.limit = id, after, limit
	return f.versions, f.call(actor, project)
}
func (f *savedHTTPService) GetSavedAnalysisVersion(_ context.Context, actor, project, id string, version int64) (reporting.SavedAnalysisVersion, error) {
	f.id, f.version = id, version
	return savedVersionFixture(version), f.call(actor, project)
}
func (f *savedHTTPService) EnqueueSavedAnalysisVersion(_ context.Context, actor, project, id string, version int64, key string, in reporting.SavedAnalysisExecute) (analysis.Job, error) {
	f.id, f.version, f.key = id, version, key
	return analysis.Job{ID: savedID, State: "queued", CreatedAt: savedResource().CreatedAt}, f.call(actor, project)
}
func savedHTTP(t *testing.T, f *savedHTTPService, access AccessService) (http.Handler, *cursor.Codec) {
	t.Helper()
	v, e := contract.New()
	if e != nil {
		t.Fatal(e)
	}
	codec, e := cursor.New([]byte(strings.Repeat("s", 32)))
	if e != nil {
		t.Fatal(e)
	}
	a := &API{Boundary: &httpapi.Boundary{Validator: v, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))}, Access: access, Cursor: codec, Origin: "https://evia.example", SavedAnalyses: f}
	return a.Handler(), codec
}
func TestSavedAnalysisHTTPRoutesContractsAndRevisions(t *testing.T) {
	f := &savedHTTPService{}
	access := &catalogAccess{fakeAccess: &fakeAccess{}}
	h, _ := savedHTTP(t, f, access)
	for _, tc := range []struct {
		method, path, body string
		headers            map[string]string
		status             int
		write              bool
	}{
		{"POST", savedBase, savedCreateBody, map[string]string{"Idempotency-Key": "create-key"}, 201, true},
		{"GET", savedItem, "", nil, 200, false},
		{"GET", savedItem + "/draft", "", nil, 200, false},
		{"PATCH", savedItem + "/draft", `{"definition":` + savedDefinition + `}`, map[string]string{"If-Match": `"1"`}, 200, true},
		{"POST", savedItem + "/validations", savedValidateBody, nil, 200, false},
		{"POST", savedItem + "/versions", `{"draft_revision":1,"expected_published_version":0,"validation_hash":"` + strings.Repeat("d", 64) + `"}`, map[string]string{"Idempotency-Key": "publish-key"}, 201, true},
		{"GET", savedItem + "/versions/1", "", nil, 200, false},
		{"POST", savedItem + "/versions/1/queries", savedExecuteBody, map[string]string{"Idempotency-Key": "execute-key"}, 202, false},
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
	if f.id != savedID || f.version != 1 || f.key != "execute-key" || f.revision != 1 {
		t.Fatal("scope or precondition not forwarded", f)
	}
}
func TestSavedAnalysisHTTPStrictInputs(t *testing.T) {
	for _, tc := range []struct {
		name, method, path, body string
		headers                  map[string]string
		status                   int
	}{
		{"missing env", "GET", savedBase, "", nil, 400},
		{"unknown env", "GET", savedBase + "?environment=unknown", "", nil, 400},
		{"duplicate env", "GET", savedBase + "?environment=development&environment=production", "", nil, 400},
		{"unknown parameter", "GET", savedBase + "?environment=development&search=x", "", nil, 400},
		{"invalid query encoding", "GET", savedBase + "?environment=%xx", "", nil, 400},
		{"analysis limit", "GET", savedBase + "?environment=development&limit=101", "", nil, 400},
		{"version limit", "GET", savedItem + "/versions?limit=51", "", nil, 400},
		{"duplicate limit", "GET", savedItem + "/versions?limit=1&limit=2", "", nil, 400},
		{"item env override", "GET", savedItem + "?environment=production", "", nil, 400},
		{"versions env override", "GET", savedItem + "/versions?environment=production", "", nil, 400},
		{"invalid UUID", "GET", savedBase + "/wrong", "", nil, 400},
		{"invalid project UUID", "GET", "/api/v1/projects/wrong/saved-analyses?environment=development", "", nil, 400},
		{"noncanonical version", "GET", savedItem + "/versions/01", "", nil, 400},
		{"negative version", "GET", savedItem + "/versions/-1", "", nil, 400},
		{"unsafe version", "GET", savedItem + "/versions/9007199254740992", "", nil, 400},
		{"create needs key", "POST", savedBase, savedCreateBody, nil, 400},
		{"execute needs key", "POST", savedItem + "/versions/1/queries", savedExecuteBody, nil, 400},
		{"invalid key", "POST", savedBase, savedCreateBody, map[string]string{"Idempotency-Key": "contains space"}, 400},
		{"PATCH needs revision", "PATCH", savedItem + "/draft", `{"definition":` + savedDefinition + `}`, nil, 400},
		{"weak revision", "PATCH", savedItem + "/draft", `{"definition":` + savedDefinition + `}`, map[string]string{"If-Match": `W/"1"`}, 400},
		{"unsafe revision", "PATCH", savedItem + "/draft", `{"definition":` + savedDefinition + `}`, map[string]string{"If-Match": `"9007199254740992"`}, 400},
		{"unknown body field", "POST", savedItem + "/validations", `{"draft_revision":1,"expected_published_version":0,"extra":true}`, nil, 400},
		{"duplicate JSON", "POST", savedItem + "/validations", `{"draft_revision":1,"draft_revision":2,"expected_published_version":0}`, nil, 400},
		{"trailing JSON", "POST", savedItem + "/validations", savedValidateBody + `{}`, nil, 400},
		{"runtime context in template", "POST", savedBase, strings.Replace(savedCreateBody, `"kind":"trend"`, `"kind":"trend","environment":"production"`, 1), map[string]string{"Idempotency-Key": "key"}, 400},
		{"untrusted type in template", "POST", savedBase, strings.Replace(savedCreateBody, `"metrics":`, `"group_by":[{"kind":"system","key":"uid","resolved_type":"string"}],"metrics":`, 1), map[string]string{"Idempotency-Key": "key"}, 400},
		{"executable visualization options", "POST", savedBase, strings.Replace(savedCreateBody, `"kind":"metric"`, `"kind":"metric","formatter":"code"`, 1), map[string]string{"Idempotency-Key": "key"}, 400},
		{"execute env override body", "POST", savedItem + "/versions/1/queries", strings.TrimSuffix(savedExecuteBody, "}") + `,"environment":"production"}`, map[string]string{"Idempotency-Key": "key"}, 400},
		{"CSRF validation", "POST", savedItem + "/validations", savedValidateBody, map[string]string{"X-CSRF-Token": ""}, 403},
		{"CSRF execution", "POST", savedItem + "/versions/1/queries", savedExecuteBody, map[string]string{"X-CSRF-Token": "", "Idempotency-Key": "key"}, 403},
		{"origin", "POST", savedBase, savedCreateBody, map[string]string{"Origin": "https://invalid.example", "Idempotency-Key": "key"}, 403},
		{"wrong method", "DELETE", savedItem, "", nil, 405},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := &savedHTTPService{}
			h, _ := savedHTTP(t, f, &catalogAccess{fakeAccess: &fakeAccess{}})
			catalogStatus(t, catalogRequest(h, tc.method, tc.path, tc.body, tc.headers), tc.status)
			if f.calls != 0 {
				t.Fatal("invalid input reached service")
			}
		})
	}
}
func TestSavedAnalysisHTTPCursorsBoundScopeAndPosition(t *testing.T) {
	f := &savedHTTPService{items: []reporting.SavedAnalysis{savedResource(), savedResource()}, versions: []reporting.SavedAnalysisVersion{savedVersionFixture(1), savedVersionFixture(2)}}
	h, codec := savedHTTP(t, f, &catalogAccess{fakeAccess: &fakeAccess{}})
	for _, tc := range []struct {
		path, scope, position string
		versions              bool
	}{
		{savedBase + "?environment=development&limit=1", "saved_analyses:" + userID + ":" + projectID + ":development", savedID, false},
		{savedItem + "/versions?limit=1", "saved_analysis_versions:" + userID + ":" + projectID + ":" + savedID, "1", true},
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
		if tc.versions && f.version != 1 || !tc.versions && f.after != savedID {
			t.Fatal("continuation not forwarded")
		}
		for _, scope := range []string{strings.Replace(tc.scope, userID, savedID, 1), strings.Replace(tc.scope, projectID, savedID, 1), tc.scope + ":other", strings.Replace(tc.scope, "development", "production", 1), strings.Replace(tc.scope, savedID, projectID, 1)} {
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
func TestSavedAnalysisHTTPAuthRolesAndSafeFailures(t *testing.T) {
	f := &savedHTTPService{}
	h, _ := savedHTTP(t, f, &catalogAccess{fakeAccess: &fakeAccess{}})
	r := httptest.NewRequest("GET", savedItem, nil)
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
		out := catalogStatus(t, catalogRequest(h, "POST", savedItem+"/versions/1/queries", savedExecuteBody, map[string]string{"Idempotency-Key": "key"}), tc.status)
		if out["code"] != tc.code || strings.Contains(out["message"].(string), "private") {
			t.Fatal("unsafe or incorrect error", out)
		}
	}
	deny := &catalogAccess{fakeAccess: &fakeAccess{}, deny: fault.New(fault.Forbidden, "PROJECT_FORBIDDEN", "无权访问")}
	h, _ = savedHTTP(t, f, deny)
	before := f.calls
	catalogStatus(t, catalogRequest(h, "GET", savedItem, "", nil), 403)
	if f.calls != before {
		t.Fatal("project denial reached service")
	}
}

// Role authorization is owned by the shared access layer. This double exercises
// that saved routes request write policy for draft/publication writes.
type savedViewerAccess struct{ *catalogAccess }

func (a *savedViewerAccess) Authorize(ctx context.Context, actor, project string, write bool) error {
	if e := a.catalogAccess.Authorize(ctx, actor, project, write); e != nil {
		return e
	}
	if write {
		return fault.New(fault.Forbidden, "PROJECT_FORBIDDEN", "项目为只读")
	}
	return nil
}
func TestSavedAnalysisHTTPViewerAggregateAccess(t *testing.T) {
	f := &savedHTTPService{}
	a := &savedViewerAccess{&catalogAccess{fakeAccess: &fakeAccess{}}}
	h, _ := savedHTTP(t, f, a)
	for _, tc := range []struct {
		method, path, body string
		headers            map[string]string
		status             int
	}{
		{"GET", savedItem, "", nil, 200},
		{"POST", savedItem + "/validations", savedValidateBody, nil, 200},
		{"POST", savedItem + "/versions/1/queries", savedExecuteBody, map[string]string{"Idempotency-Key": "execute-key"}, 202},
		{"POST", savedBase, savedCreateBody, map[string]string{"Idempotency-Key": "create-key"}, 403},
		{"PATCH", savedItem + "/draft", `{"definition":` + savedDefinition + `}`, map[string]string{"If-Match": `"1"`}, 403},
		{"POST", savedItem + "/versions", `{"draft_revision":1,"expected_published_version":0,"validation_hash":"` + strings.Repeat("d", 64) + `"}`, map[string]string{"Idempotency-Key": "publish-key"}, 403},
	} {
		before := f.calls
		catalogStatus(t, catalogRequest(h, tc.method, tc.path, tc.body, tc.headers), tc.status)
		if tc.status == 403 && f.calls != before {
			t.Fatal("read-only mutation reached service")
		}
	}
}

func TestSavedAnalysisHTTPRejectsAmbiguousHeaders(t *testing.T) {
	for _, tc := range []struct{ method, path, body, header, value string }{
		{"POST", savedBase, savedCreateBody, "Idempotency-Key", "key"},
		{"PATCH", savedItem + "/draft", `{"definition":` + savedDefinition + `}`, "If-Match", `"1"`},
		{"POST", savedItem + "/versions/1/queries", savedExecuteBody, "Idempotency-Key", "key"},
	} {
		f := &savedHTTPService{}
		h, _ := savedHTTP(t, f, &catalogAccess{fakeAccess: &fakeAccess{}})
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

func TestSavedAnalysisHTTPListDefaultsBoundsAndResponseContract(t *testing.T) {
	f := &savedHTTPService{}
	h, _ := savedHTTP(t, f, &catalogAccess{fakeAccess: &fakeAccess{}})
	for _, path := range []string{savedBase + "?environment=development", savedItem + "/versions"} {
		out := catalogStatus(t, catalogRequest(h, "GET", path, "", nil), 200)
		if f.limit != 20 || out["next_cursor"] != nil || len(out["items"].([]any)) != 0 {
			t.Fatal("incorrect default or empty page", out, f.limit)
		}
	}
	for _, tc := range []struct {
		path  string
		limit int
	}{{savedBase + "?environment=development&limit=100", 100}, {savedItem + "/versions?limit=50", 50}} {
		catalogStatus(t, catalogRequest(h, "GET", tc.path, "", nil), 200)
		if f.limit != tc.limit {
			t.Fatal("page bound not forwarded")
		}
	}
	// A successful application return still has to satisfy the response schema.
	invalid := savedResource()
	invalid.ID = "private-invalid-response"
	f.items = []reporting.SavedAnalysis{invalid}
	out := catalogStatus(t, catalogRequest(h, "GET", savedBase+"?environment=development", "", nil), 500)
	raw, _ := json.Marshal(out)
	if strings.Contains(string(raw), "private-invalid-response") {
		t.Fatal("invalid resource leaked through response boundary")
	}
}

const metricRefID = "11111111-1111-4111-8111-111111111111"

func TestSavedAnalysisHTTPMetricReferenceBoundary(t *testing.T) {
	metricOnly := `{"environment":"development","definition":{"title":"Metric card","metric_ref":{"metric_id":"` + metricRefID + `","version":1},"visualization":{"kind":"metric"}}}`
	lineViz := `{"environment":"development","definition":{"title":"Metric card","metric_ref":{"metric_id":"` + metricRefID + `","version":1},"visualization":{"kind":"line"}}}`
	noSource := `{"environment":"development","definition":{"title":"No source","visualization":{"kind":"metric"}}}`

	// A metric-only reference is accepted and decoded into the domain model.
	f := &savedHTTPService{}
	h, _ := savedHTTP(t, f, &catalogAccess{fakeAccess: &fakeAccess{}})
	before := f.calls
	catalogStatus(t, catalogRequest(h, "POST", savedBase, metricOnly, map[string]string{"Idempotency-Key": "metric-create"}), 201)
	if f.calls != before+1 {
		t.Fatal("valid metric reference did not reach the service")
	}

	// Structural misuse is rejected at the boundary before the service runs.
	for _, body := range []string{lineViz, noSource} {
		reject := &savedHTTPService{}
		handler, _ := savedHTTP(t, reject, &catalogAccess{fakeAccess: &fakeAccess{}})
		catalogStatus(t, catalogRequest(handler, "POST", savedBase, body, map[string]string{"Idempotency-Key": "bad"}), 400)
		if reject.calls != 0 {
			t.Fatal("invalid metric reference reached the service")
		}
	}

	// A stored metric analysis carries both the pin and its derived query, and
	// that resolved representation must satisfy the response contract.
	validator, e := contract.New()
	if e != nil {
		t.Fatal(e)
	}
	metricVersion := reporting.SavedAnalysisVersion{
		AnalysisID: savedID, ProjectID: projectID, Environment: "development", Version: 1, DraftRevision: 1,
		Definition: reporting.SavedAnalysisDefinition{
			Title:         "Metric card",
			Query:         analysis.Template{Kind: "trend", EventVersions: []analysis.EventVersion{{EventKey: "page_opened", SchemaVersion: 1}}, Metrics: []analysis.Metric{{Op: "count", Accuracy: "exact"}}, Limit: 200},
			MetricRef:     &reporting.MetricReference{MetricID: metricRefID, Version: 1},
			Visualization: reporting.SavedAnalysisVisualization{Kind: "metric"},
		},
		Dependencies: reporting.DependencyManifest{
			ProjectID: projectID, Environment: "development", SelectionMode: "event_versions", UnfixedEventKeys: []string{},
			EventVersions: []reporting.Dependency{{EventKey: "page_opened", SchemaVersion: 1, DefinitionHash: strings.Repeat("a", 64)}},
			Metric:        &reporting.MetricDependency{MetricID: metricRefID, Version: 1, DefinitionHash: strings.Repeat("a", 64)},
		},
		DefinitionHash: strings.Repeat("b", 64), DependencyHash: strings.Repeat("c", 64), ValidationHash: strings.Repeat("d", 64),
		CreatedAt: savedResource().CreatedAt, CreatedBy: userID,
	}
	encoded, e := validator.Encode("SavedAnalysisVersion", metricVersion)
	if e != nil {
		t.Fatal("resolved metric analysis failed response contract", e)
	}
	if !strings.Contains(string(encoded), `"metric_ref":{"metric_id":"`+metricRefID+`","version":1}`) {
		t.Fatalf("response lost the metric pin: %s", encoded)
	}
	// Inline analyses remain valid and never serialize a metric reference.
	inline, e := validator.Encode("SavedAnalysisVersion", savedVersionFixture(1))
	if e != nil {
		t.Fatal("inline analysis response regressed", e)
	}
	// The create/get draft response uses the same stored definition shape.
	var liveDefinition reporting.SavedAnalysisDefinition
	liveJSON := `{"title":"调试指标引用","description":"","query":{"kind":"trend","event_versions":[{"event_key":"demo_page","schema_version":1}],"subject":"uid","metrics":[{"op":"count","accuracy":"exact"}],"limit":200},"metric_ref":{"metric_id":"4a3c2702-549f-4929-b936-a80f0a8c747a","version":1},"visualization":{"kind":"metric"}}`
	if e = json.Unmarshal([]byte(liveJSON), &liveDefinition); e != nil {
		t.Fatal(e)
	}
	if _, e = validator.Encode("SavedAnalysisDefinition", liveDefinition); e != nil {
		t.Fatal("live metric definition failed response contract", e)
	}
	metricDraft := reporting.SavedAnalysisDraft{
		SavedAnalysis: reporting.SavedAnalysis{
			ID: savedID, ProjectID: projectID, Environment: "development", Title: "Metric card", Kind: "trend",
			DraftRevision: 1, CreatedAt: savedResource().CreatedAt, UpdatedAt: savedResource().CreatedAt,
		},
		Definition: liveDefinition,
	}
	if _, e = validator.Encode("SavedAnalysisDraft", metricDraft); e != nil {
		t.Fatal("metric draft failed response contract", e)
	}
	if strings.Contains(string(inline), "metric_ref") {
		t.Fatalf("inline response must not contain metric_ref: %s", inline)
	}
}
