package analysis

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/howtomakeaname/Evia/backend/internal/platform/fault"
)

func TestClickHouseFailureClassifiesResourceLimitsByNamedException(t *testing.T) {
	for _, tc := range []struct {
		name, body, code string
		kind             fault.Kind
	}{
		{"actual byte budget", "Code: 307. DB::Exception: Limit for bytes to read exceeded, max bytes: 256.00 MiB, current bytes: 256.12 MiB. (TOO_MANY_BYTES)", "QUERY_COST_EXCEEDED", fault.Invalid},
		{"row budget", "Code: 158. DB::Exception: Limit for rows to read exceeded. (TOO_MANY_ROWS)", "QUERY_COST_EXCEEDED", fault.Invalid},
		{"result budget", "Code: 396. DB::Exception: Result limit exceeded. (TOO_MANY_ROWS_OR_BYTES)", "QUERY_COST_EXCEEDED", fault.Invalid},
		{"memory budget", "Code: 241. DB::Exception: Memory limit exceeded. (MEMORY_LIMIT_EXCEEDED)", "QUERY_COST_EXCEEDED", fault.Invalid},
		{"timeout", "Code: 159. DB::Exception: Timeout exceeded. (TIMEOUT_EXCEEDED)", "QUERY_TIMEOUT", fault.Unavailable},
		{"slow execution", "Code: 160. DB::Exception: Query is too slow. (TOO_SLOW)", "QUERY_TIMEOUT", fault.Unavailable},
		{"unknown byte code", "Code: 307. DB::Exception: Unrecognized storage failure. (UNKNOWN_EXCEPTION)", "QUERY_EXECUTION_FAILED", fault.Unavailable},
		{"unknown row code", "Code: 158. DB::Exception: Unrecognized storage failure. (UNKNOWN_EXCEPTION)", "QUERY_EXECUTION_FAILED", fault.Unavailable},
		{"unknown failure", "Code: 62. DB::Exception: Invalid query. (SYNTAX_ERROR)", "QUERY_EXECUTION_FAILED", fault.Unavailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			problem := fault.Public(clickHouseFailure([]byte(tc.body)))
			if problem.Code != tc.code || problem.Kind != tc.kind {
				t.Fatal("incorrect query failure classification", problem)
			}
		})
	}
}

func TestClickHouseRefusesRedirectsForDefaultAndCustomClients(t *testing.T) {
	var disclosed atomic.Int32
	destination := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { disclosed.Add(1); w.WriteHeader(200) }))
	defer destination.Close()
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" {
			t.Error("query was not POST")
		}
		http.Redirect(w, r, destination.URL, http.StatusTemporaryRedirect)
	}))
	defer redirect.Close()
	var customFollowed atomic.Bool
	for _, client := range []*http.Client{nil, {Timeout: time.Minute, CheckRedirect: func(*http.Request, []*http.Request) error { customFollowed.Store(true); return nil }}} {
		adapter, e := NewClickHouse(ClickHouseConfig{URL: redirect.URL, Username: "synthetic-user", Password: "synthetic-secret", Database: "testdb", Client: client})
		if e != nil {
			t.Fatal(e)
		}
		r := request(t, baseRequest)
		if _, e = adapter.Execute(context.Background(), Scope{ProjectID: "private-project", Environment: r.Environment}, r); e == nil {
			t.Fatal("redirect accepted as a successful query")
		}
	}
	if disclosed.Load() != 0 || customFollowed.Load() {
		t.Fatal("redirect leaked query body or invoked custom redirect policy", disclosed.Load(), customFollowed.Load())
	}
}
func TestClickHouseRejectsEndpointPathsAndEmbeddedAuthority(t *testing.T) {
	for _, endpoint := range []string{"http://example.invalid/query", "http://example.invalid/%2f", "http://user:secret@example.invalid/", "http://example.invalid/?query=SELECT", "http://example.invalid/#fragment", "file:///tmp/query"} {
		if _, e := NewClickHouse(ClickHouseConfig{URL: endpoint, Database: "testdb"}); e == nil {
			t.Fatal("unsafe endpoint accepted", endpoint)
		}
	}
}
