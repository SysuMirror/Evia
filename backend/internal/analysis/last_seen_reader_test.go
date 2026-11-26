package analysis

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestClickHouseReadEventLastSeen(t *testing.T) {
	ts := time.Date(2026, 11, 26, 12, 30, 0, 0, time.UTC)
	var seenQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		buf := make([]byte, 4096)
		n, _ := r.Body.Read(buf)
		seenQuery = string(buf[:n])
		w.WriteHeader(http.StatusOK)
		fmt.Fprintf(w, "{\"event_key\":\"post_opened\",\"last_seen_ns\":%d}\n", ts.UnixNano())
	}))
	defer srv.Close()

	ch, err := NewClickHouse(ClickHouseConfig{URL: srv.URL, Database: "evia"})
	if err != nil {
		t.Fatal(err)
	}
	got, err := ch.ReadEventLastSeen(context.Background(), "proj-1", "development", []string{"post_opened", "page_opened"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(seenQuery, "environment={environment:String}") {
		t.Fatalf("expected environment filter in SQL, got %s", seenQuery)
	}
	if !got["post_opened"].Equal(ts) {
		t.Fatalf("expected %v, got %v", ts, got["post_opened"])
	}
	if _, ok := got["page_opened"]; ok {
		t.Fatalf("unexpected entry for unseen event")
	}
}
