package control

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/howtomakeaname/Evia/backend/internal/analysis"
	"github.com/howtomakeaname/Evia/backend/internal/platform/contract"
	"github.com/howtomakeaname/Evia/backend/internal/platform/cursor"
	"github.com/howtomakeaname/Evia/backend/internal/platform/httpapi"
	"github.com/howtomakeaname/Evia/backend/internal/reporting"
)

const formulaID = "66666666-6666-4666-8666-666666666666"
const formulaBase = "/api/v1/projects/" + projectID + "/formula-metrics"
const formulaItem = formulaBase + "/" + formulaID
const formulaExpression = `{"op":"metric","metric":{"metric_id":"` + metricID + `","version":1}}`
const formulaDefinition = `{"title":"Lesson ratio","description":"","formula":{"expression":` + formulaExpression + `,"unit":"event"}}`
const formulaCreateBody = `{"environment":"development","definition":` + formulaDefinition + `}`
const formulaValidateBody = `{"draft_revision":1,"expected_published_version":0}`
const formulaExecuteBody = `{"time_range":{"from":"2026-11-01T00:00:00Z","to":"2026-11-02T00:00:00Z"},"timezone":"UTC","interval":"day"}`

type formulaHTTPService struct {
	reporting.FormulaMetricService
	calls                       int
	err                         error
	after, environment, key, id string
	revision, version           int64
	limit                       int
	archivedOnly                bool
	search                      string
	items                       []reporting.FormulaMetricRecord
	versions                    []reporting.FormulaMetricVersion
}

func (f *formulaHTTPService) call(actor, project string) error {
	if actor == "" || project != projectID {
		panic("unscoped formula metric service")
	}
	f.calls++
	return f.err
}

func formulaRecord() reporting.FormulaMetricRecord {
	return reporting.FormulaMetricRecord{ID: formulaID, ProjectID: projectID, Environment: "development", Title: "Lesson ratio", DraftRevision: 1, CreatedAt: time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC), UpdatedAt: time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)}
}

func formulaDraft() reporting.FormulaMetricDraft {
	var def reporting.FormulaMetricDefinition
	if e := json.Unmarshal([]byte(formulaDefinition), &def); e != nil {
		panic(e)
	}
	return reporting.FormulaMetricDraft{FormulaMetricRecord: formulaRecord(), Definition: def}
}

func formulaVersionFixture(v int64) reporting.FormulaMetricVersion {
	return reporting.FormulaMetricVersion{MetricID: formulaID, ProjectID: projectID, Environment: "development", Version: v, DraftRevision: 1, Definition: formulaDraft().Definition, Dependencies: reporting.FormulaDependencyManifest{ProjectID: projectID, Environment: "development", Metrics: []reporting.MetricDependency{{MetricID: metricID, Version: 1, DefinitionHash: strings.Repeat("a", 64)}}}, DefinitionHash: strings.Repeat("b", 64), DependencyHash: strings.Repeat("c", 64), ValidationHash: strings.Repeat("d", 64), CreatedAt: formulaRecord().CreatedAt, CreatedBy: userID}
}

func (f *formulaHTTPService) CreateFormulaMetric(_ context.Context, actor, project, key string, in reporting.FormulaMetricCreate) (reporting.FormulaMetricDraft, error) {
	f.key, f.environment = key, in.Environment
	return formulaDraft(), f.call(actor, project)
}
func (f *formulaHTTPService) ListFormulaMetrics(_ context.Context, actor, project, env, after string, limit int, archivedOnly bool, search string) ([]reporting.FormulaMetricRecord, error) {
	f.environment, f.after, f.limit, f.archivedOnly, f.search = env, after, limit, archivedOnly, search
	return f.items, f.call(actor, project)
}
func (f *formulaHTTPService) ArchiveFormulaMetric(_ context.Context, actor, project, id string) (reporting.FormulaMetricRecord, error) {
	f.id = id
	return formulaRecord(), f.call(actor, project)
}
func (f *formulaHTTPService) UnarchiveFormulaMetric(_ context.Context, actor, project, id string) (reporting.FormulaMetricRecord, error) {
	f.id = id
	return formulaRecord(), f.call(actor, project)
}
func (f *formulaHTTPService) GetFormulaMetric(_ context.Context, actor, project, id string) (reporting.FormulaMetricRecord, error) {
	f.id = id
	return formulaRecord(), f.call(actor, project)
}
func (f *formulaHTTPService) GetFormulaMetricDraft(_ context.Context, actor, project, id string) (reporting.FormulaMetricDraft, error) {
	f.id = id
	return formulaDraft(), f.call(actor, project)
}
func (f *formulaHTTPService) SaveFormulaMetricDraft(_ context.Context, actor, project, id string, revision int64, _ reporting.FormulaMetricDefinition) (reporting.FormulaMetricDraft, error) {
	f.id, f.revision = id, revision
	out := formulaDraft()
	out.DraftRevision = revision + 1
	return out, f.call(actor, project)
}
func (f *formulaHTTPService) ValidateFormulaMetric(_ context.Context, actor, project, id string, in reporting.FormulaMetricValidate) (reporting.FormulaMetricValidation, error) {
	f.id, f.revision = id, in.DraftRevision
	v := formulaVersionFixture(1)
	return reporting.FormulaMetricValidation{Valid: true, ExecutionPerformed: false, DraftRevision: in.DraftRevision, PublishedVersion: in.ExpectedPublishedVersion, NormalizedDefinition: v.Definition, Dependencies: v.Dependencies, DefinitionHash: v.DefinitionHash, DependencyHash: v.DependencyHash, ValidationHash: v.ValidationHash, Diff: []reporting.FormulaMetricChange{}}, f.call(actor, project)
}
func (f *formulaHTTPService) PublishFormulaMetric(_ context.Context, actor, project, id, key string, in reporting.FormulaMetricPublish) (reporting.FormulaMetricVersion, error) {
	f.id, f.key, f.revision = id, key, in.DraftRevision
	return formulaVersionFixture(1), f.call(actor, project)
}
func (f *formulaHTTPService) ListFormulaMetricVersions(_ context.Context, actor, project, id string, after int64, limit int) ([]reporting.FormulaMetricVersion, error) {
	f.id, f.version, f.limit = id, after, limit
	return f.versions, f.call(actor, project)
}
func (f *formulaHTTPService) GetFormulaMetricVersion(_ context.Context, actor, project, id string, version int64) (reporting.FormulaMetricVersion, error) {
	f.id, f.version = id, version
	return formulaVersionFixture(version), f.call(actor, project)
}
func (f *formulaHTTPService) EnqueueFormulaMetricVersion(_ context.Context, actor, project, id string, version int64, key string, in reporting.MetricExecute) (analysis.Job, error) {
	f.id, f.version, f.key = id, version, key
	return analysis.Job{ID: "77777777-7777-4777-8777-777777777777", State: analysis.JobStateQueued, CreatedAt: formulaRecord().CreatedAt}, f.call(actor, project)
}

func formulaHTTP(t *testing.T, f *formulaHTTPService, access AccessService) (http.Handler, *cursor.Codec) {
	t.Helper()
	v, e := contract.New()
	if e != nil {
		t.Fatal(e)
	}
	codec, e := cursor.New([]byte(strings.Repeat("s", 32)))
	if e != nil {
		t.Fatal(e)
	}
	a := &API{Boundary: &httpapi.Boundary{Validator: v, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))}, Access: access, Cursor: codec, Origin: "https://evia.example", FormulaMetrics: f}
	return a.Handler(), codec
}

func TestFormulaMetricHTTPRoutesContractsAndRevisions(t *testing.T) {
	f := &formulaHTTPService{}
	access := &catalogAccess{fakeAccess: &fakeAccess{}}
	h, _ := formulaHTTP(t, f, access)
	for _, tc := range []struct {
		method, path, body string
		headers            map[string]string
		status             int
		write              bool
	}{
		{"POST", formulaBase, formulaCreateBody, map[string]string{"Idempotency-Key": "create-key"}, 201, true},
		{"GET", formulaBase + "?environment=development", "", nil, 200, false},
		{"GET", formulaItem, "", nil, 200, false},
		{"GET", formulaItem + "/draft", "", nil, 200, false},
		{"PATCH", formulaItem + "/draft", `{"definition":` + formulaDefinition + `}`, map[string]string{"If-Match": `"1"`}, 200, true},
		{"POST", formulaItem + "/validations", formulaValidateBody, nil, 200, true},
		{"POST", formulaItem + "/versions", `{"draft_revision":1,"expected_published_version":0,"validation_hash":"` + strings.Repeat("d", 64) + `"}`, map[string]string{"Idempotency-Key": "publish-key"}, 201, true},
		{"GET", formulaItem + "/versions", "", nil, 200, false},
		{"GET", formulaItem + "/versions/1", "", nil, 200, false},
		{"POST", formulaItem + "/versions/1/queries", formulaExecuteBody, map[string]string{"Idempotency-Key": "execute-key"}, 202, false},
		{"POST", formulaItem + "/archive", "", nil, 200, true},
		{"POST", formulaItem + "/unarchive", "", nil, 200, true},
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
	if f.id != formulaID || f.version != 1 || f.key != "execute-key" || f.revision != 1 {
		t.Fatal("scope or precondition not forwarded", f)
	}
}

func TestFormulaMetricHTTPStrictInputs(t *testing.T) {
	for _, tc := range []struct {
		name, method, path, body string
		headers                  map[string]string
		status                   int
	}{
		{"missing env", "GET", formulaBase, "", nil, 400},
		{"unknown env", "GET", formulaBase + "?environment=unknown", "", nil, 400},
		{"unknown parameter", "GET", formulaBase + "?environment=development&search=x", "", nil, 400},
		{"unknown archive view", "GET", formulaBase + "?environment=development&view=bogus", "", nil, 400},
		{"duplicate archive view", "GET", formulaBase + "?environment=development&view=archived&view=active", "", nil, 400},
		{"item route rejects view", "GET", formulaItem + "?view=archived", "", nil, 400},
		{"list limit", "GET", formulaBase + "?environment=development&limit=101", "", nil, 400},
		{"version limit", "GET", formulaItem + "/versions?limit=51", "", nil, 400},
		{"invalid UUID", "GET", formulaBase + "/wrong", "", nil, 400},
		{"noncanonical version", "GET", formulaItem + "/versions/01", "", nil, 400},
		{"unsafe version", "GET", formulaItem + "/versions/9007199254740992", "", nil, 400},
		{"create needs key", "POST", formulaBase, formulaCreateBody, nil, 400},
		{"invalid key", "POST", formulaBase, formulaCreateBody, map[string]string{"Idempotency-Key": "contains space"}, 400},
		{"PATCH needs revision", "PATCH", formulaItem + "/draft", `{"definition":` + formulaDefinition + `}`, nil, 400},
		{"wildcard revision", "PATCH", formulaItem + "/draft", `{"definition":` + formulaDefinition + `}`, map[string]string{"If-Match": `*`}, 400},
		{"unknown body field", "POST", formulaItem + "/validations", `{"draft_revision":1,"expected_published_version":0,"extra":true}`, nil, 400},
		{"null definition", "POST", formulaBase, `{"environment":"development","definition":null}`, map[string]string{"Idempotency-Key": "key"}, 400},
		{"formula unknown op", "POST", formulaBase, `{"environment":"development","definition":{"title":"x","description":"","formula":{"expression":{"op":"pow"},"unit":"event"}}}`, map[string]string{"Idempotency-Key": "key"}, 400},
		{"atomic field rejected", "POST", formulaBase, `{"environment":"development","definition":{"title":"x","description":"","formula":{"expression":` + formulaExpression + `,"unit":"event"},"aggregation":{"op":"count"}}}`, map[string]string{"Idempotency-Key": "key"}, 400},
		{"execute needs key", "POST", formulaItem + "/versions/1/queries", formulaExecuteBody, nil, 400},
		{"execute invalid interval", "POST", formulaItem + "/versions/1/queries", strings.Replace(formulaExecuteBody, `"interval":"day"`, `"interval":"month"`, 1), map[string]string{"Idempotency-Key": "key"}, 400},
		{"execute invalid version", "POST", formulaItem + "/versions/0/queries", formulaExecuteBody, map[string]string{"Idempotency-Key": "key"}, 400},
		{"CSRF execution", "POST", formulaItem + "/versions/1/queries", formulaExecuteBody, map[string]string{"X-CSRF-Token": "", "Idempotency-Key": "key"}, 403},
		{"CSRF validation", "POST", formulaItem + "/validations", formulaValidateBody, map[string]string{"X-CSRF-Token": ""}, 403},
		{"origin", "POST", formulaBase, formulaCreateBody, map[string]string{"Origin": "https://invalid.example", "Idempotency-Key": "key"}, 403},
		{"wrong method", "DELETE", formulaItem, "", nil, 405},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := &formulaHTTPService{}
			h, _ := formulaHTTP(t, f, &catalogAccess{fakeAccess: &fakeAccess{}})
			catalogStatus(t, catalogRequest(h, tc.method, tc.path, tc.body, tc.headers), tc.status)
			if f.calls != 0 {
				t.Fatal("invalid input reached service")
			}
		})
	}
}

func TestFormulaMetricHTTPArchiveViewSelector(t *testing.T) {
	f := &formulaHTTPService{items: []reporting.FormulaMetricRecord{formulaRecord(), formulaRecord()}}
	h, codec := formulaHTTP(t, f, &catalogAccess{fakeAccess: &fakeAccess{}})
	for _, tc := range []struct {
		path string
		want bool
	}{
		{formulaBase + "?environment=development", false},
		{formulaBase + "?environment=development&view=active", false},
		{formulaBase + "?environment=development&view=archived", true},
	} {
		f.archivedOnly = !tc.want
		catalogStatus(t, catalogRequest(h, "GET", tc.path, "", nil), 200)
		if f.archivedOnly != tc.want {
			t.Fatalf("archivedOnly not forwarded for %s: got %v", tc.path, f.archivedOnly)
		}
	}
	for _, path := range []string{
		formulaBase + "?environment=development&view=bogus",
		formulaBase + "?environment=development&view=archived&view=active",
		formulaItem + "?view=archived",
	} {
		before := f.calls
		out := catalogStatus(t, catalogRequest(h, "GET", path, "", nil), 400)
		if f.calls != before {
			t.Fatal("invalid view reached service")
		}
		if path == formulaItem+"?view=archived" && out["code"] != "INVALID_QUERY" {
			t.Fatal("item route must keep the strict allowlist", out)
		}
		if strings.Contains(path, "bogus") && out["code"] != "INVALID_ARCHIVE_VIEW" {
			t.Fatal("closed-set view must fail with INVALID_ARCHIVE_VIEW", out)
		}
	}
	activeScope := "formula_metrics:" + userID + ":" + projectID + ":development:active:"
	archivedScope := "formula_metrics:" + userID + ":" + projectID + ":development:archived:"
	archivedPage := catalogStatus(t, catalogRequest(h, "GET", formulaBase+"?environment=development&view=archived&limit=1", "", nil), 200)
	next, ok := archivedPage["next_cursor"].(string)
	if !ok || next == "" {
		t.Fatal("missing archived continuation")
	}
	if decoded, e := codec.Decode(archivedScope, next); e != nil || decoded != formulaID {
		t.Fatal("archived cursor bound to wrong scope", decoded, e)
	}
	before := f.calls
	bad := catalogStatus(t, catalogRequest(h, "GET", formulaBase+"?environment=development&cursor="+url.QueryEscape(next), "", nil), 400)
	if bad["code"] != "INVALID_CURSOR" || f.calls != before {
		t.Fatal("cross-view cursor reached service", bad)
	}
	if _, e := codec.Decode(activeScope, next); e == nil {
		t.Fatal("archived cursor decoded under the active scope")
	}
}

func TestFormulaMetricHTTPListSearchTerm(t *testing.T) {
	f := &formulaHTTPService{items: []reporting.FormulaMetricRecord{formulaRecord(), formulaRecord()}}
	h, codec := formulaHTTP(t, f, &catalogAccess{fakeAccess: &fakeAccess{}})
	// A bounded search term is trimmed, forwarded and bound to the cursor scope.
	page := catalogStatus(
		t,
		catalogRequest(h, "GET", formulaBase+"?environment=development&limit=1&q=%20%E7%95%99%E5%AD%98%20", "", nil),
		200,
	)
	if f.search != "留存" {
		t.Fatalf("search term must be trimmed and forwarded, got %q", f.search)
	}
	next, ok := page["next_cursor"].(string)
	if !ok || next == "" {
		t.Fatal("missing continuation cursor", page)
	}
	searchScope := "formula_metrics:" + userID + ":" + projectID + ":development:active:留存"
	if decoded, e := codec.Decode(searchScope, next); e != nil || decoded != formulaID {
		t.Fatal("cursor must be bound to the search scope", decoded, e)
	}
	// Unknown query parameters stay rejected even though q is now allowlisted.
	catalogStatus(t, catalogRequest(h, "GET", formulaBase+"?environment=development&q=x&unexpected=1", "", nil), 400)
	// An overlong term is rejected at the boundary without dispatching.
	before := f.calls
	catalogStatus(t, catalogRequest(h, "GET", formulaBase+"?environment=development&q="+url.QueryEscape(strings.Repeat("字", 129)), "", nil), 400)
	if f.calls != before {
		t.Fatal("overlong search term reached the service")
	}
}
