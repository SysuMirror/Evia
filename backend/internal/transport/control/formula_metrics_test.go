package control

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"time"

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

type formulaHTTPService struct {
	reporting.FormulaMetricService
	calls                       int
	err                         error
	after, environment, key, id string
	revision, version           int64
	limit                       int
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
func (f *formulaHTTPService) ListFormulaMetrics(_ context.Context, actor, project, env, after string, limit int) ([]reporting.FormulaMetricRecord, error) {
	f.environment, f.after, f.limit = env, after, limit
	return f.items, f.call(actor, project)
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
	if f.id != formulaID || f.version != 1 || f.key != "publish-key" || f.revision != 1 {
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
