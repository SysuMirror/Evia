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
	"time"

	"github.com/howtomakeaname/Evia/backend/internal/ingestion"
	"github.com/howtomakeaname/Evia/backend/internal/platform/contract"
	"github.com/howtomakeaname/Evia/backend/internal/platform/httpapi"
)

type collectionSources struct{}

func (collectionSources) ResolveSource(context.Context, string) (ingestion.AdmissionSource, error) {
	return ingestion.AdmissionSource{Source: ingestion.Source{ID: catalogReleaseID, ProjectID: projectID, Environment: "development", Kind: "browser", Status: "active", AllowedOrigins: []string{"https://website.example"}}}, nil
}

type collectionPlans struct{}

func (collectionPlans) ResolvePlan(_ context.Context, p, e string, r int64) (ingestion.Plan, error) {
	return ingestion.Plan{ProjectID: p, Environment: e, Revision: r, Ready: true, Active: true, Events: map[string]ingestion.EventRule{"page_opened": {Key: "page_opened", Version: 1, AllowedSourceKinds: []string{"browser"}, Schema: map[string]any{"type": "object", "properties": map[string]any{}, "additionalProperties": false}}}}, nil
}

type collectionBudget struct{}

func (collectionBudget) ReserveCollection(context.Context, string, string, int) error { return nil }

type collectionQueue struct {
	calls int
	err   error
}

func (q *collectionQueue) Publish(context.Context, []ingestion.Record) error { q.calls++; return q.err }
func collectionHandler(t *testing.T, q *collectionQueue) http.Handler {
	t.Helper()
	v, e := contract.New()
	if e != nil {
		t.Fatal(e)
	}
	c, e := ingestion.NewCollector(collectionSources{}, collectionPlans{}, q, collectionBudget{}, v)
	if e != nil {
		t.Fatal(e)
	}
	a, e := NewCollectionAPI(&httpapi.Boundary{Validator: v, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))}, c)
	if e != nil {
		t.Fatal(e)
	}
	return a.Handler()
}
func collectionEvent() map[string]any {
	return map[string]any{"event_id": catalogReleaseID, "event_key": "page_opened", "schema_version": 1, "release_revision": 1, "occurred_at": time.Now().UTC().Format(time.RFC3339Nano), "identity": map[string]any{"uid": "website-user"}, "context": map[string]any{}, "sdk": map[string]any{"name": "test", "version": "1"}, "properties": map[string]any{}}
}
func collectionBody(events ...any) string {
	b, _ := json.Marshal(map[string]any{"protocol_version": 1, "batch_id": projectID, "write_key": "public-key", "sent_at": time.Now().UTC().Format(time.RFC3339Nano), "events": events})
	return string(b)
}
func TestCollectorHTTPPartialResultsMediaCORSAndDurability(t *testing.T) {
	for _, media := range []string{"application/json", "text/plain;charset=UTF-8"} {
		t.Run(media, func(t *testing.T) {
			q := &collectionQueue{}
			r := httptest.NewRequest("POST", "/v1/collect", strings.NewReader(collectionBody(collectionEvent(), map[string]any{"event_id": "invalid"})))
			r.Header.Set("Content-Type", media)
			r.Header.Set("Origin", "https://website.example")
			w := httptest.NewRecorder()
			collectionHandler(t, q).ServeHTTP(w, r)
			if w.Code != 202 || q.calls != 1 || w.Header().Get("Access-Control-Allow-Origin") != "https://website.example" {
				t.Fatal(w.Code, w.Body.String())
			}
			var receipt ingestion.Receipt
			if e := json.Unmarshal(w.Body.Bytes(), &receipt); e != nil {
				t.Fatal(e)
			}
			if len(receipt.Results) != 2 || receipt.Results[0].Status != "accepted" || receipt.Results[1].Error == nil || receipt.Results[1].Error.Retryable {
				t.Fatal(receipt)
			}
		})
	}
	q := &collectionQueue{err: errors.New("broker acknowledgement lost")}
	r := httptest.NewRequest("POST", "/v1/collect", strings.NewReader(collectionBody(collectionEvent())))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Origin", "https://website.example")
	w := httptest.NewRecorder()
	collectionHandler(t, q).ServeHTTP(w, r)
	if w.Code != 503 || strings.Contains(w.Body.String(), `"accepted"`) || w.Header().Get("Access-Control-Allow-Origin") == "" {
		t.Fatal("ambiguous queue result acknowledged or hidden by CORS", w.Code, w.Body.String())
	}
}
func TestCollectorHTTPFailureBoundsAndPreflight(t *testing.T) {
	for _, tc := range []struct {
		name, body, media, origin, cookie, path string
		status                                  int
	}{
		{"all invalid", collectionBody(map[string]any{"event_id": "invalid"}), "application/json", "https://website.example", "", "/v1/collect", 422},
		{"media", collectionBody(collectionEvent()), "application/x-www-form-urlencoded", "https://website.example", "", "/v1/collect", 415},
		{"charset", collectionBody(collectionEvent()), "application/json;charset=iso-8859-1", "https://website.example", "", "/v1/collect", 415},
		{"body size", strings.Repeat("x", 256*1024+1), "application/json", "https://website.example", "", "/v1/collect", 413},
		{"bad JSON", "{} {}", "application/json", "https://website.example", "", "/v1/collect", 400},
		{"cookies", collectionBody(collectionEvent()), "application/json", "https://website.example", "website=session", "/v1/collect", 403},
		{"origin", collectionBody(collectionEvent()), "application/json", "https://foreign.example", "", "/v1/collect", 403},
		{"query", collectionBody(collectionEvent()), "application/json", "https://website.example", "", "/v1/collect?write_key=key", 400},
	} {
		t.Run(tc.name, func(t *testing.T) {
			q := &collectionQueue{}
			r := httptest.NewRequest("POST", tc.path, strings.NewReader(tc.body))
			r.Header.Set("Content-Type", tc.media)
			r.Header.Set("Origin", tc.origin)
			r.Header.Set("Cookie", tc.cookie)
			w := httptest.NewRecorder()
			collectionHandler(t, q).ServeHTTP(w, r)
			if w.Code != tc.status || q.calls != 0 {
				t.Fatal(w.Code, w.Body.String())
			}
		})
	}
	r := httptest.NewRequest("OPTIONS", "/v1/collect", nil)
	r.Header.Set("Origin", "https://website.example")
	r.Header.Set("Access-Control-Request-Method", "POST")
	r.Header.Set("Access-Control-Request-Headers", "content-type")
	w := httptest.NewRecorder()
	collectionHandler(t, &collectionQueue{}).ServeHTTP(w, r)
	if w.Code != 204 || w.Header().Get("Access-Control-Allow-Credentials") != "" {
		t.Fatal("unsafe preflight", w.Code, w.Header())
	}
	r.Header.Set("Access-Control-Request-Headers", "Authorization")
	w = httptest.NewRecorder()
	collectionHandler(t, &collectionQueue{}).ServeHTTP(w, r)
	if w.Code != 403 {
		t.Fatal("browser could forward website Authorization")
	}
}
