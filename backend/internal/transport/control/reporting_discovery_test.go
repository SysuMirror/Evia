package control

import (
	"context"
	"encoding/json"
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

const discoveryBase = "/api/v1/projects/" + projectID

type discoveryHTTPStore struct {
	calls                 int
	environment, afterKey string
	afterVersion          int64
	err                   error
}

func (f *discoveryHTTPStore) call(actor, project string) error {
	if actor != userID || project != projectID {
		panic("discovery escaped authenticated scope")
	}
	f.calls++
	return f.err
}
func (f *discoveryHTTPStore) AuthorizeAnalysisDiscovery(_ context.Context, a, p string) error {
	return f.call(a, p)
}
func (f *discoveryHTTPStore) ListAnalysisCatalog(_ context.Context, a, p, environment, afterKey string, afterVersion int64, limit int) (reporting.CatalogSnapshot, error) {
	f.environment, f.afterKey, f.afterVersion = environment, afterKey, afterVersion
	if limit != 2 {
		panic("catalog must request pagination lookahead")
	}
	out := reporting.CatalogSnapshot{Revision: 2, Items: []reporting.PublishedDefinition{}}
	for _, version := range []int64{1, 2} {
		if version <= afterVersion {
			continue
		}
		out.Items = append(out.Items, reporting.PublishedDefinition{Event: reporting.CatalogEvent{EventKey: "page_opened", SchemaVersion: version, DisplayName: "Synthetic page", DefinitionHash: strings.Repeat("a", 64), CreatedAt: time.Date(2026, 11, 7, 0, 0, 0, 0, time.UTC), Published: true}, Definition: map[string]any{"schema": map[string]any{"properties": map[string]any{"enabled": map[string]any{"type": "boolean"}}}}})
	}
	return out, f.call(a, p)
}
func (f *discoveryHTTPStore) ValidateAnalysisQuery(_ context.Context, a, p string, r analysis.Request) (reporting.ResolvedValidation, error) {
	return reporting.ResolvedValidation{Request: r, Dependencies: []reporting.Dependency{}}, f.call(a, p)
}
func discoveryHTTP(t *testing.T, store *discoveryHTTPStore, access *catalogAccess) (http.Handler, *cursor.Codec) {
	t.Helper()
	v, e := contract.New()
	if e != nil {
		t.Fatal(e)
	}
	codec, e := cursor.New([]byte(strings.Repeat("s", 32)))
	if e != nil {
		t.Fatal(e)
	}
	a := &API{Boundary: &httpapi.Boundary{Validator: v, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))}, Access: access, Cursor: codec, Origin: "https://evia.example"}
	mux := http.NewServeMux()
	(&DiscoveryAPI{API: a, Service: reporting.NewDiscovery(store, v, false)}).Register(mux)
	return a.Boundary.Middleware(mux), codec
}
func TestDiscoveryHTTPContractsAndScopedCursor(t *testing.T) {
	f := &discoveryHTTPStore{}
	h, codec := discoveryHTTP(t, f, &catalogAccess{fakeAccess: &fakeAccess{}})
	capabilities := catalogStatus(t, catalogRequest(h, "GET", discoveryBase+"/analysis-capabilities", "", nil), 200)
	if capabilities["execution_available"] != false {
		t.Fatal(capabilities)
	}
	page := catalogStatus(t, catalogRequest(h, "GET", discoveryBase+"/analysis-catalog?environment=development&limit=1", "", nil), 200)
	if len(page["items"].([]any)) != 1 || page["catalog_revision"] != float64(2) {
		t.Fatal(page)
	}
	next, ok := page["next_cursor"].(string)
	if !ok || next == "" {
		t.Fatal("missing scoped continuation", page)
	}
	second := catalogStatus(t, catalogRequest(h, "GET", discoveryBase+"/analysis-catalog?environment=development&limit=1&cursor="+url.QueryEscape(next), "", nil), 200)
	if f.environment != "development" || f.afterKey != "page_opened" || f.afterVersion != 1 || second["next_cursor"] != nil {
		t.Fatal("stable numeric event version continuation was lost", second)
	}
	validation := catalogStatus(t, catalogRequest(h, "POST", discoveryBase+"/query-validations", validQueryBody, nil), 200)
	if validation["valid"] != true || validation["execution_performed"] != false || validation["execution_available"] != false {
		t.Fatal(validation)
	}
	for _, scope := range []string{"analysis_catalog:" + userID + ":" + projectID + ":production", "analysis_catalog:other-actor:" + projectID + ":development", "analysis_catalog:" + userID + ":other-project:development"} {
		bad := codec.Encode(scope, `["page_opened",1]`)
		calls := f.calls
		out := catalogStatus(t, catalogRequest(h, "GET", discoveryBase+"/analysis-catalog?environment=development&limit=1&cursor="+url.QueryEscape(bad), "", nil), 400)
		if out["code"] != "INVALID_CURSOR" || f.calls != calls {
			t.Fatal("cross-scope cursor reached persistence", out)
		}
	}
}
func TestDiscoveryHTTPAuthenticationCSRFInvalidInputAndSafeErrors(t *testing.T) {
	for _, tc := range []struct {
		name, method, path, body string
		headers                  map[string]string
		status                   int
	}{
		{"missing environment", "GET", "/analysis-catalog?limit=1", "", nil, 400},
		{"invalid environment", "GET", "/analysis-catalog?environment=invalid&limit=1", "", nil, 400},
		{"duplicate environment", "GET", "/analysis-catalog?environment=development&environment=production&limit=1", "", nil, 400},
		{"unknown query parameter", "GET", "/analysis-capabilities?environment=development", "", nil, 400},
		{"page budget", "GET", "/analysis-catalog?environment=development&limit=201", "", nil, 400},
		{"CSRF", "POST", "/query-validations", validQueryBody, map[string]string{"X-CSRF-Token": ""}, 403},
		{"origin", "POST", "/query-validations", validQueryBody, map[string]string{"Origin": "https://invalid.example"}, 403},
		{"duplicate JSON", "POST", "/query-validations", `{"kind":"trend","kind":"events"}`, nil, 400},
		{"untrusted type", "POST", "/query-validations", strings.TrimSuffix(validQueryBody, "}") + `,"filter":{"op":"eq","field":{"kind":"system","key":"uid","resolved_type":"string"},"value":"x"}}`, nil, 400},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := &discoveryHTTPStore{}
			h, _ := discoveryHTTP(t, f, &catalogAccess{fakeAccess: &fakeAccess{}})
			catalogStatus(t, catalogRequest(h, tc.method, discoveryBase+tc.path, tc.body, tc.headers), tc.status)
			if f.calls != 0 {
				t.Fatal("rejected input reached persistence")
			}
		})
	}
	f := &discoveryHTTPStore{}
	h, _ := discoveryHTTP(t, f, &catalogAccess{fakeAccess: &fakeAccess{}})
	r := httptest.NewRequest("GET", discoveryBase+"/analysis-capabilities", nil)
	r.Header.Set("Authorization", "Bearer synthetic-source-key")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	catalogStatus(t, w, 401)
	if f.calls != 0 {
		t.Fatal("source bearer authorized management discovery")
	}
	deny := &catalogAccess{fakeAccess: &fakeAccess{}, deny: fault.New(fault.NotFound, "PROJECT_NOT_FOUND", "项目不可见")}
	h, _ = discoveryHTTP(t, f, deny)
	catalogStatus(t, catalogRequest(h, "GET", discoveryBase+"/analysis-capabilities", "", nil), 404)
	if f.calls != 0 {
		t.Fatal("unauthorized project reached persistence")
	}
	f.err = io.ErrUnexpectedEOF
	h, _ = discoveryHTTP(t, f, &catalogAccess{fakeAccess: &fakeAccess{}})
	out := catalogStatus(t, catalogRequest(h, "POST", discoveryBase+"/query-validations", validQueryBody, nil), 500)
	b, _ := json.Marshal(out)
	if strings.Contains(string(b), "EOF") {
		t.Fatal("persistence failure escaped HTTP boundary")
	}
}
