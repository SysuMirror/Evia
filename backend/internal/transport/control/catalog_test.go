package control

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/howtomakeaname/Evia/backend/internal/access"
	"github.com/howtomakeaname/Evia/backend/internal/catalog"
	"github.com/howtomakeaname/Evia/backend/internal/platform/contract"
	"github.com/howtomakeaname/Evia/backend/internal/platform/cursor"
	"github.com/howtomakeaname/Evia/backend/internal/platform/fault"
	"github.com/howtomakeaname/Evia/backend/internal/platform/httpapi"
)

const catalogReleaseID = "33333333-3333-4333-8333-333333333333"
const catalogEventsURL = "/api/v1/projects/" + projectID + "/event-definitions"
const catalogReleasesURL = "/api/v1/projects/" + projectID + "/releases"
const catalogDraftBody = `{"schema":{"type":"object","properties":{"title":{"type":"string"}},"required":["title"],"additionalProperties":false}}`
const catalogReleaseBody = `{"environment":"development","expected_active_revision":0,"events":[{"event_key":"page_opened","draft_revision":1}]}`

type catalogAccess struct {
	*fakeAccess
	deny   error
	writes []bool
	actor  string
}

func (f *catalogAccess) Authenticate(ctx context.Context, token string) (access.Session, error) {
	s, e := f.fakeAccess.Authenticate(ctx, token)
	if f.actor != "" {
		s.UserID = f.actor
	}
	return s, e
}
func (f *catalogAccess) Authorize(_ context.Context, actor, project string, write bool) error {
	if actor == "" || project != projectID {
		panic("unscoped authorization")
	}
	f.writes = append(f.writes, write)
	return f.deny
}

type catalogStore struct {
	calls                           int
	err                             error
	events                          []catalog.Event
	releases                        []catalog.Release
	after, environment, idempotency string
	revision                        int64
	draft                           catalog.DraftInput
}

func (f *catalogStore) call(actor, project string) error {
	if actor == "" || project != projectID {
		panic("unscoped persistence")
	}
	f.calls++
	return f.err
}
func catalogEvent() catalog.Event {
	return catalog.Event{TypeID: 1, Key: "page_opened", DisplayName: "Page opened"}
}
func catalogRelease() catalog.Release {
	return catalog.Release{ID: catalogReleaseID, Revision: 1, Environment: "development", State: "published", IngestionReady: false, AccelerationState: "not_requested", ManifestHash: strings.Repeat("a", 64)}
}
func (f *catalogStore) CreateEvent(_ context.Context, actor, project string, in catalog.EventInput) (catalog.Event, error) {
	return catalog.Event{TypeID: 1, Key: in.Key, DisplayName: in.DisplayName}, f.call(actor, project)
}
func (f *catalogStore) ListEvents(_ context.Context, actor, project, env, after string, limit int) ([]catalog.Event, error) {
	f.after, f.environment = after, env
	if limit < 2 {
		panic("missing pagination lookahead")
	}
	return f.events, f.call(actor, project)
}
func (f *catalogStore) GetDraft(_ context.Context, actor, project, key string) (catalog.Draft, error) {
	return catalog.Draft{Revision: 1, Definition: catalog.DraftInput{Schema: map[string]any{"type": "object", "properties": map[string]any{}, "additionalProperties": false}}}, f.call(actor, project)
}
func (f *catalogStore) SaveDraft(_ context.Context, actor, project, key string, revision int64, in catalog.DraftInput) (catalog.Draft, error) {
	f.revision, f.draft = revision, in
	return catalog.Draft{Revision: revision + 1, Definition: in}, f.call(actor, project)
}
func (f *catalogStore) ValidationDraft(_ context.Context, actor, project, key string, revision int64) (catalog.Draft, *catalog.DraftInput, error) {
	f.revision = revision
	return catalog.Draft{Revision: revision, Definition: catalog.DraftInput{Schema: map[string]any{"type": "object", "properties": map[string]any{"title": map[string]any{"type": "string"}}, "required": []any{"title"}, "additionalProperties": false}}}, nil, f.call(actor, project)
}
func (f *catalogStore) CreateRelease(_ context.Context, actor, project, key string, in catalog.ReleaseInput) (catalog.Release, error) {
	f.idempotency, f.environment = key, in.Environment
	return catalogRelease(), f.call(actor, project)
}
func (f *catalogStore) GetRelease(_ context.Context, actor, project, id string) (catalog.Release, error) {
	return catalogRelease(), f.call(actor, project)
}
func (f *catalogStore) ListReleases(_ context.Context, actor, project, env, after string, limit int) ([]catalog.Release, error) {
	f.after, f.environment = after, env
	if limit < 2 {
		panic("missing pagination lookahead")
	}
	return f.releases, f.call(actor, project)
}
func catalogAPI(t *testing.T, f *catalogStore, access *catalogAccess) (http.Handler, *cursor.Codec) {
	t.Helper()
	v, e := contract.New()
	if e != nil {
		t.Fatal(e)
	}
	c, e := cursor.New([]byte(strings.Repeat("s", 32)))
	if e != nil {
		t.Fatal(e)
	}
	a := &API{Boundary: &httpapi.Boundary{Validator: v, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))}, Access: access, Catalog: catalog.New(f), Cursor: c, Origin: "https://evia.example"}
	return a.Handler(), c
}
func catalogRequest(h http.Handler, method, path, body string, headers map[string]string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.AddCookie(&http.Cookie{Name: "evia_session", Value: "valid"})
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("X-CSRF-Token", "csrf")
	for k, v := range headers {
		r.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}
func catalogStatus(t *testing.T, w *httptest.ResponseRecorder, status int) map[string]any {
	t.Helper()
	if w.Code != status {
		t.Fatalf("status %d, want %d: %s", w.Code, status, w.Body.String())
	}
	var out map[string]any
	if e := json.Unmarshal(w.Body.Bytes(), &out); e != nil {
		t.Fatal(e)
	}
	if w.Header().Get("X-Request-ID") == "" {
		t.Fatal("missing request ID")
	}
	if status >= 400 && out["request_id"] != w.Header().Get("X-Request-ID") {
		t.Fatal("unstructured error", out)
	}
	return out
}
func TestCatalogSuccessContracts(t *testing.T) {
	f := &catalogStore{}
	auth := &catalogAccess{fakeAccess: &fakeAccess{}}
	h, _ := catalogAPI(t, f, auth)
	out := catalogStatus(t, catalogRequest(h, "POST", catalogEventsURL, `{"event_key":"page_opened","display_name":"Page opened"}`, nil), 201)
	if out["event_key"] != "page_opened" || len(out["published_versions"].([]any)) != 0 {
		t.Fatal(out)
	}
	w := catalogRequest(h, "GET", catalogEventsURL+"/page_opened/draft", "", nil)
	catalogStatus(t, w, 200)
	if w.Header().Get("ETag") != `"1"` {
		t.Fatal("missing draft ETag")
	}
	w = catalogRequest(h, "PATCH", catalogEventsURL+"/page_opened/draft", catalogDraftBody, map[string]string{"If-Match": `"1"`})
	catalogStatus(t, w, 200)
	if w.Header().Get("ETag") != `"2"` || f.revision != 1 || f.draft.Schema["type"] != "object" {
		t.Fatal("draft revision not preserved")
	}
	for _, sample := range []struct {
		body  string
		valid bool
	}{{`{"draft_revision":1,"sample_properties":{"title":"Home"}}`, true}, {`{"draft_revision":1,"sample_properties":{"title":12}}`, false}} {
		out = catalogStatus(t, catalogRequest(h, "POST", catalogEventsURL+"/page_opened/validate", sample.body, nil), 200)
		if out["valid"] != sample.valid || out["compatibility"] != "initial" {
			t.Fatal(out)
		}
	}
	out = catalogStatus(t, catalogRequest(h, "POST", catalogReleasesURL, catalogReleaseBody, map[string]string{"Idempotency-Key": "release-1"}), 202)
	if out["ingestion_ready"] != false || out["state"] != "published" || f.idempotency != "release-1" {
		t.Fatal("release metadata incorrectly reports activation", out)
	}
	catalogStatus(t, catalogRequest(h, "GET", catalogReleasesURL+"/"+catalogReleaseID, "", nil), 200)
	for _, path := range []string{catalogEventsURL, catalogReleasesURL} {
		out = catalogStatus(t, catalogRequest(h, "GET", path, "", nil), 200)
		if out["next_cursor"] != nil || len(out["items"].([]any)) != 0 {
			t.Fatal("empty list contract", out)
		}
	}
	if len(auth.writes) != 9 {
		t.Fatal("missing authorization", auth.writes)
	}
}
func TestCatalogRejectsBeforePersistence(t *testing.T) {
	for _, tc := range []struct {
		name, method, path, body string
		headers                  map[string]string
		status                   int
	}{
		{name: "missing csrf", method: "POST", path: catalogEventsURL, body: `{"event_key":"x","display_name":"X"}`, headers: map[string]string{"X-CSRF-Token": ""}, status: 403},
		{name: "foreign origin", method: "POST", path: catalogEventsURL, body: `{}`, headers: map[string]string{"Origin": "https://foreign.example"}, status: 403},
		{name: "unknown body field", method: "POST", path: catalogEventsURL, body: `{"event_key":"x","display_name":"X","owner":"other"}`, status: 400},
		{name: "duplicate body field", method: "POST", path: catalogEventsURL, body: `{"event_key":"x","event_key":"y","display_name":"X"}`, status: 400},
		{name: "reserved event key", method: "POST", path: catalogEventsURL, body: `{"event_key":"sys.page_opened","display_name":"X"}`, status: 400},
		{name: "display name limit", method: "POST", path: catalogEventsURL, body: `{"event_key":"x","display_name":"` + strings.Repeat("x", 129) + `"}`, status: 400},
		{name: "missing revision", method: "PATCH", path: catalogEventsURL + "/x/draft", body: catalogDraftBody, status: 400},
		{name: "weak revision", method: "PATCH", path: catalogEventsURL + "/x/draft", body: catalogDraftBody, headers: map[string]string{"If-Match": `W/"1"`}, status: 400},
		{name: "unsafe revision", method: "PATCH", path: catalogEventsURL + "/x/draft", body: catalogDraftBody, headers: map[string]string{"If-Match": `"9007199254740992"`}, status: 400},
		{name: "noncanonical revision", method: "PATCH", path: catalogEventsURL + "/x/draft", body: catalogDraftBody, headers: map[string]string{"If-Match": `"+1"`}, status: 400},
		{name: "invalid schema", method: "PATCH", path: catalogEventsURL + "/x/draft", body: `{"schema":{"type":"array"}}`, headers: map[string]string{"If-Match": `"1"`}, status: 400},
		{name: "invalid validation revision", method: "POST", path: catalogEventsURL + "/x/validate", body: `{"draft_revision":0,"sample_properties":{}}`, status: 400},
		{name: "missing idempotency", method: "POST", path: catalogReleasesURL, body: catalogReleaseBody, status: 400},
		{name: "blank idempotency", method: "POST", path: catalogReleasesURL, body: catalogReleaseBody, headers: map[string]string{"Idempotency-Key": " "}, status: 400},
		{name: "long idempotency", method: "POST", path: catalogReleasesURL, body: catalogReleaseBody, headers: map[string]string{"Idempotency-Key": strings.Repeat("x", 129)}, status: 400},
		{name: "unsafe active revision", method: "POST", path: catalogReleasesURL, body: strings.Replace(catalogReleaseBody, `"expected_active_revision":0`, `"expected_active_revision":9007199254740992`, 1), headers: map[string]string{"Idempotency-Key": "one"}, status: 400},
		{name: "duplicate release event", method: "POST", path: catalogReleasesURL, body: `{"environment":"development","expected_active_revision":0,"events":[{"event_key":"x","draft_revision":1},{"event_key":"x","draft_revision":1}]}`, headers: map[string]string{"Idempotency-Key": "one"}, status: 400},
		{name: "invalid project", method: "GET", path: "/api/v1/projects/not-a-project/event-definitions", status: 400},
		{name: "invalid key", method: "GET", path: catalogEventsURL + "/sys.x/draft", status: 400},
		{name: "invalid release", method: "GET", path: catalogReleasesURL + "/invalid", status: 400},
		{name: "unknown query", method: "GET", path: catalogEventsURL + "?role=owner", status: 400},
		{name: "duplicate environment", method: "GET", path: catalogEventsURL + "?environment=development&environment=production", status: 400},
		{name: "empty environment", method: "GET", path: catalogEventsURL + "?environment=", status: 400},
		{name: "invalid environment", method: "GET", path: catalogReleasesURL + "?environment=preview", status: 400},
		{name: "bad query encoding", method: "GET", path: catalogEventsURL + "?cursor=%zz", status: 400},
		{name: "duplicate limit", method: "GET", path: catalogEventsURL + "?limit=1&limit=2", status: 400},
		{name: "empty limit", method: "GET", path: catalogEventsURL + "?limit=", status: 400},
		{name: "limit bound", method: "GET", path: catalogReleasesURL + "?limit=201", status: 400},
		{name: "invalid cursor", method: "GET", path: catalogEventsURL + "?cursor=forged", status: 400},
		{name: "query on detail", method: "GET", path: catalogEventsURL + "/x/draft?environment=development", status: 400},
		{name: "query on mutation", method: "POST", path: catalogEventsURL + "?environment=development", body: `{"event_key":"x","display_name":"X"}`, status: 400},
		{name: "unsupported media", method: "POST", path: catalogEventsURL, body: `{}`, headers: map[string]string{"Content-Type": "text/plain"}, status: 415},
		{name: "body bound", method: "POST", path: catalogEventsURL, body: `{"event_key":"x","display_name":"` + strings.Repeat("x", 256*1024) + `"}`, status: 413},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := &catalogStore{}
			h, _ := catalogAPI(t, f, &catalogAccess{fakeAccess: &fakeAccess{}})
			catalogStatus(t, catalogRequest(h, tc.method, tc.path, tc.body, tc.headers), tc.status)
			if f.calls != 0 {
				t.Fatal("invalid request reached persistence")
			}
		})
	}
}
func TestCatalogAuthenticationAndProjectAuthorization(t *testing.T) {
	for _, tc := range []struct {
		name, token, method, path string
		denied                    error
		status                    int
	}{
		{"anonymous", "", "GET", catalogEventsURL, nil, 401},
		{"expired", "bad", "GET", catalogReleasesURL, nil, 401},
		{"hidden project", "valid", "GET", catalogEventsURL, fault.New(fault.NotFound, "PROJECT_NOT_FOUND", "项目不存在"), 404},
		{"viewer write", "valid", "POST", catalogEventsURL, fault.New(fault.Forbidden, "PROJECT_WRITE_FORBIDDEN", "没有写入权限"), 403},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := &catalogStore{}
			auth := &catalogAccess{fakeAccess: &fakeAccess{}, deny: tc.denied}
			h, _ := catalogAPI(t, f, auth)
			r := httptest.NewRequest(tc.method, tc.path, strings.NewReader(`{"event_key":"x","display_name":"X"}`))
			if tc.token != "" {
				r.AddCookie(&http.Cookie{Name: "evia_session", Value: tc.token})
			}
			r.Header.Set("X-CSRF-Token", "csrf")
			r.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			catalogStatus(t, w, tc.status)
			if f.calls != 0 {
				t.Fatal("unauthorized persistence access")
			}
		})
	}
}
func TestCatalogDuplicateHeaders(t *testing.T) {
	for _, tc := range []struct{ method, path, body, header, value string }{
		{"PATCH", catalogEventsURL + "/x/draft", catalogDraftBody, "If-Match", `"1"`},
		{"POST", catalogReleasesURL, catalogReleaseBody, "Idempotency-Key", "one"},
	} {
		f := &catalogStore{}
		h, _ := catalogAPI(t, f, &catalogAccess{fakeAccess: &fakeAccess{}})
		r := httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.body))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("X-CSRF-Token", "csrf")
		r.AddCookie(&http.Cookie{Name: "evia_session", Value: "valid"})
		r.Header.Add(tc.header, tc.value)
		r.Header.Add(tc.header, tc.value)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		catalogStatus(t, w, 400)
		if f.calls != 0 {
			t.Fatal("duplicate header reached persistence")
		}
	}
}
func TestCatalogPaginationAndScope(t *testing.T) {
	for _, operation := range []string{"events", "releases"} {
		t.Run(operation, func(t *testing.T) {
			event2 := catalogEvent()
			event2.Key = "second"
			release2 := catalogRelease()
			release2.ID = userID
			f := &catalogStore{events: []catalog.Event{catalogEvent(), event2}, releases: []catalog.Release{catalogRelease(), release2}}
			auth := &catalogAccess{fakeAccess: &fakeAccess{}}
			h, c := catalogAPI(t, f, auth)
			base := catalogEventsURL
			if operation == "releases" {
				base = catalogReleasesURL
			}
			out := catalogStatus(t, catalogRequest(h, "GET", base+"?limit=1&environment=development", "", nil), 200)
			token, ok := out["next_cursor"].(string)
			if !ok || len(out["items"].([]any)) != 1 {
				t.Fatal(out)
			}
			f.events = nil
			f.releases = nil
			catalogStatus(t, catalogRequest(h, "GET", base+"?environment=development&cursor="+token, "", nil), 200)
			expected := "page_opened"
			if operation == "releases" {
				expected = catalogReleaseID
			}
			if f.after != expected || f.environment != "development" {
				t.Fatal("incorrect continuation", f.after, f.environment)
			}
			for _, path := range []string{base + "?cursor=" + token, base + "?environment=production&cursor=" + token} {
				calls := f.calls
				catalogStatus(t, catalogRequest(h, "GET", path, "", nil), 400)
				if f.calls != calls {
					t.Fatal("foreign cursor reached persistence")
				}
			}
			other := catalogReleasesURL
			if operation == "releases" {
				other = catalogEventsURL
			}
			catalogStatus(t, catalogRequest(h, "GET", other+"?environment=development&cursor="+token, "", nil), 400)
			auth.actor = userID + "-other"
			catalogStatus(t, catalogRequest(h, "GET", base+"?environment=development&cursor="+token, "", nil), 400)
			auth.actor = ""
			wrongScope := strings.Join([]string{"catalog", operation, userID, userID, "development"}, ":")
			catalogStatus(t, catalogRequest(h, "GET", base+"?environment=development&cursor="+c.Encode(wrongScope, expected), "", nil), 400)
		})
	}
}
func TestCatalogDomainFailures(t *testing.T) {
	for _, tc := range []struct {
		name, method, path, body string
		headers                  map[string]string
		err                      error
		status                   int
		code                     string
	}{
		{"stale draft", "PATCH", catalogEventsURL + "/x/draft", catalogDraftBody, map[string]string{"If-Match": `"1"`}, catalog.RevisionConflict(), 409, "DRAFT_REVISION_CONFLICT"},
		{"idempotency conflict", "POST", catalogReleasesURL, catalogReleaseBody, map[string]string{"Idempotency-Key": "one"}, fault.New(fault.Conflict, "IDEMPOTENCY_CONFLICT", "幂等键冲突"), 409, "IDEMPOTENCY_CONFLICT"},
		{"revoked during persistence", "GET", catalogEventsURL, "", nil, fault.New(fault.NotFound, "PROJECT_NOT_FOUND", "项目不存在"), 404, "PROJECT_NOT_FOUND"},
		{"missing draft", "GET", catalogEventsURL + "/x/draft", "", nil, fault.New(fault.NotFound, "EVENT_NOT_FOUND", "事件不存在"), 404, "EVENT_NOT_FOUND"},
		{"dependency failure", "GET", catalogReleasesURL, "", nil, errors.New("private database diagnostic"), 500, "INTERNAL_ERROR"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := &catalogStore{err: tc.err}
			h, _ := catalogAPI(t, f, &catalogAccess{fakeAccess: &fakeAccess{}})
			out := catalogStatus(t, catalogRequest(h, tc.method, tc.path, tc.body, tc.headers), tc.status)
			if out["code"] != tc.code || strings.Contains(out["message"].(string), "private") {
				t.Fatal(out)
			}
			if f.calls != 1 {
				t.Fatal("expected storage boundary", f.calls)
			}
		})
	}
}
func TestCatalogUnimplementedRoutesRemainHidden(t *testing.T) {
	f := &catalogStore{}
	h, _ := catalogAPI(t, f, &catalogAccess{fakeAccess: &fakeAccess{}})
	for _, tc := range []struct {
		method, path string
		status       int
	}{
		{"POST", catalogReleasesURL + "/" + catalogReleaseID + "/activate", 404},
		{"GET", catalogReleasesURL + "/" + catalogReleaseID + "/artifacts/typescript", 404},
		{"POST", "/v1/collect", 404},
		{"PUT", catalogEventsURL, 405},
	} {
		catalogStatus(t, catalogRequest(h, tc.method, tc.path, "", nil), tc.status)
	}
	if f.calls != 0 {
		t.Fatal("unsupported operation reached persistence")
	}
}

func TestCatalogDraftOptionalPresence(t *testing.T) {
	f := &catalogStore{}
	h, _ := catalogAPI(t, f, &catalogAccess{fakeAccess: &fakeAccess{}})
	body := `{"schema":{"type":"object","properties":{},"additionalProperties":false},"trigger":"","field_metadata":{},"allowed_source_kinds":[],"example_properties":{}}`
	out := catalogStatus(t, catalogRequest(h, "PATCH", catalogEventsURL+"/x/draft", body, map[string]string{"If-Match": `"1"`}), 200)
	definition := out["definition"].(map[string]any)
	for _, key := range []string{"trigger", "field_metadata", "allowed_source_kinds", "example_properties"} {
		if _, ok := definition[key]; !ok {
			t.Fatalf("explicit empty %s disappeared: %v", key, definition)
		}
	}
	if definition["trigger"] != "" || len(definition["field_metadata"].(map[string]any)) != 0 || len(definition["allowed_source_kinds"].([]any)) != 0 || len(definition["example_properties"].(map[string]any)) != 0 {
		t.Fatal("empty values changed", definition)
	}
	if f.draft.Trigger == nil || *f.draft.Trigger != "" || f.draft.FieldMetadata == nil || f.draft.AllowedSourceKinds == nil || f.draft.ExampleProperties == nil {
		t.Fatal("explicit empty values lost before persistence", f.draft)
	}
	body = `{"schema":{"type":"object","properties":{},"additionalProperties":false}}`
	out = catalogStatus(t, catalogRequest(h, "PATCH", catalogEventsURL+"/x/draft", body, map[string]string{"If-Match": `"1"`}), 200)
	definition = out["definition"].(map[string]any)
	for _, key := range []string{"display_name", "trigger", "fact_kind", "field_metadata", "allowed_source_kinds", "example_properties"} {
		if _, ok := definition[key]; ok {
			t.Fatalf("absent %s became present: %v", key, definition)
		}
	}
}

func (f *catalogStore) GetEventVersion(_ context.Context, actor, project, key string, version int64) (catalog.EventVersion, error) {
	return catalog.EventVersion{Key: key, SchemaVersion: version, DraftRevision: 1, Definition: catalog.DraftInput{Schema: map[string]any{"type": "object", "properties": map[string]any{"title": map[string]any{"type": "string"}}}}}, f.call(actor, project)
}
func TestGetEventVersionHTTPContractScopeAndStrictVersion(t *testing.T) {
	const base = catalogEventsURL + "/page_opened/versions/"
	for _, tc := range []struct {
		name, path    string
		err, deny     error
		status, calls int
	}{
		{name: "published", path: base + "1", status: 200, calls: 1},
		{name: "zero", path: base + "0", status: 400},
		{name: "leading zero", path: base + "01", status: 400},
		{name: "negative", path: base + "-1", status: 400},
		{name: "plus", path: base + "+1", status: 400},
		{name: "fraction", path: base + "1.5", status: 400},
		{name: "unsafe", path: base + "9007199254740992", status: 400},
		{name: "unknown query", path: base + "1?environment=development", status: 400},
		{name: "missing", path: base + "1", err: fault.New(fault.NotFound, "EVENT_VERSION_NOT_FOUND", "不存在"), status: 404, calls: 1},
		{name: "revoked", path: base + "1", err: fault.New(fault.NotFound, "PROJECT_NOT_FOUND", "不可见"), status: 404, calls: 1},
		{name: "forbidden", path: base + "1", deny: fault.New(fault.NotFound, "PROJECT_NOT_FOUND", "不可见"), status: 404},
		{name: "dependency", path: base + "1", err: errors.New("database failure"), status: 500, calls: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := &catalogStore{err: tc.err}
			h, _ := catalogAPI(t, f, &catalogAccess{fakeAccess: &fakeAccess{}, deny: tc.deny})
			out := catalogStatus(t, catalogRequest(h, "GET", tc.path, "", nil), tc.status)
			if f.calls != tc.calls {
				t.Fatal(f.calls)
			}
			if tc.status == 200 && (out["schema_version"] != float64(1) || out["event_key"] != "page_opened") {
				t.Fatal(out)
			}
		})
	}
	f := &catalogStore{}
	h, _ := catalogAPI(t, f, &catalogAccess{fakeAccess: &fakeAccess{}})
	catalogStatus(t, publicConfigRequest(h, "GET", base+"1", nil), 401)
	if f.calls != 0 {
		t.Fatal("unauthenticated version read")
	}
}
