package analysis

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

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
