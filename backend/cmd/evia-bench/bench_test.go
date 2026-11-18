package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/howtomakeaname/Evia/backend/internal/gen/wire"
	"github.com/howtomakeaname/Evia/backend/internal/ingestion"
)

func fixtureConfig() Config {
	return Config{ContractVersion: 1, APIURL: "http://127.0.0.1:8081", ProjectID: "11111111-1111-4111-8111-111111111111", RunID: "22222222-2222-4222-8222-222222222222", Environment: "development", SourceKind: ingestion.SourceKindServer, WriteKey: "never-report-this-write-key", ServerSecret: "never-report-this-secret", EventKey: "benchmark_page_view", SchemaVersion: 1, ReleaseRevision: 1, StartedAt: time.Now().UTC(), UIDCardinality: 10}
}

func accepted(w http.ResponseWriter, r wire.CollectRequest) {
	items := []map[string]any{}
	for i, e := range r.Events {
		items = append(items, map[string]any{"index": i, "event_id": e.EventID, "status": "accepted"})
	}
	w.WriteHeader(202)
	json.NewEncoder(w).Encode(map[string]any{"batch_id": r.BatchID, "receipt_id": "33333333-3333-4333-8333-333333333333", "results": items})
}

func TestConcurrencyExactReplayAndSafeAtomicEvidence(t *testing.T) {
	c := fixtureConfig()
	var active, peak atomic.Int32
	var mu sync.Mutex
	originals := map[string][]byte{}
	visits := map[string]int{}
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/collect" || r.Header.Get("Authorization") != "Bearer "+c.ServerSecret {
			t.Error("incorrect public API request")
		}
		n := active.Add(1)
		defer active.Add(-1)
		for old := peak.Load(); n > old && !peak.CompareAndSwap(old, n); old = peak.Load() {
		}
		raw, _ := io.ReadAll(r.Body)
		var b wire.CollectRequest
		if json.Unmarshal(raw, &b) != nil {
			t.Error("invalid generated JSON")
		}
		mu.Lock()
		if old, ok := originals[b.BatchID]; ok && !bytes.Equal(old, raw) {
			t.Error("replay changed payload")
		}
		originals[b.BatchID] = raw
		visits[b.BatchID]++
		mu.Unlock()
		time.Sleep(2 * time.Millisecond)
		accepted(w, b)
	}))
	defer s.Close()
	c.APIURL = s.URL
	o := Options{Events: 103, BatchSize: 20, Concurrency: 3, ReplayPercent: 25, Out: filepath.Join(t.TempDir(), "report.json")}
	r := run(context.Background(), c, o)
	if !r.Complete || r.Unique.Accepted != 103 || r.Replay.Accepted != 25 || r.Requests != 8 || peak.Load() > 3 || peak.Load() < 2 {
		t.Fatalf("bad accounting or concurrency: %+v peak=%d", r, peak.Load())
	}
	replayed := 0
	for _, n := range visits {
		if n == 2 {
			replayed++
		}
	}
	if replayed != 2 {
		t.Fatalf("replayed %d batches", replayed)
	}
	if err := writeReport(o.Out, r); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(o.Out)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(raw, []byte(c.WriteKey)) || bytes.Contains(raw, []byte(c.ServerSecret)) || bytes.Contains(raw, []byte(c.APIURL)) {
		t.Fatal("evidence leaked credentials or connection configuration")
	}
	st, _ := os.Stat(o.Out)
	if st.Mode().Perm() != 0600 {
		t.Fatal("evidence permissions")
	}
}

func TestReceiptMustMatchEveryEventAndRejectAmbiguousJSON(t *testing.T) {
	c := fixtureConfig()
	b := makeBatch(c, Options{Events: 2}, 0, 2, false)
	r := httptest.NewRecorder()
	accepted(r, b.request)
	good := r.Body.Bytes()
	if _, ok := parseReceipt(good, b); !ok {
		t.Fatal("valid receipt rejected")
	}
	for name, raw := range map[string][]byte{
		"wrong batch":     bytes.Replace(good, []byte(b.request.BatchID), []byte(c.ProjectID), 1),
		"wrong event":     bytes.Replace(good, []byte(b.request.Events[0].EventID), []byte(c.ProjectID), 1),
		"duplicate index": bytes.Replace(good, []byte(`"index":1`), []byte(`"index":0`), 1),
		"missing result":  bytes.Replace(good, []byte(`"results":[`), []byte(`"results":[],"ignored":[`), 1),
		"duplicate key":   bytes.Replace(good, []byte(`"status":"accepted"`), []byte(`"status":"accepted","status":"accepted"`), 1),
		"trailing":        append(append([]byte{}, good...), []byte(`{}`)...),
	} {
		t.Run(name, func(t *testing.T) {
			if _, ok := parseReceipt(raw, b); ok {
				t.Fatal("invalid receipt counted as admission")
			}
		})
	}
}

func TestHTTPFailuresRemainVisibleWithoutRetries(t *testing.T) {
	for _, status := range []int{202, 302, 400, 429, 503} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			var calls atomic.Int32
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				w.Header().Set("Retry-After", "1")
				w.Header().Set("Location", "http://192.0.2.1")
				w.WriteHeader(status)
				io.WriteString(w, `{"message":"never-report-this-secret"}`)
			}))
			defer s.Close()
			c := fixtureConfig()
			c.APIURL = s.URL
			r := run(context.Background(), c, Options{Events: 4, BatchSize: 4, Concurrency: 1})
			if calls.Load() != 1 || r.Complete || r.Unique.Accepted != 0 {
				t.Fatalf("hidden retries or false admission: %+v", r)
			}
			if status == 429 && r.Unique.RateLimited != 4 || status == 503 && r.Unique.ServerErrors != 4 || status == 400 && r.Unique.PermanentRejected != 4 || (status == 202 || status == 302) && r.Unique.Unexpected != 4 {
				t.Fatalf("wrong failure accounting: %+v", r.Unique)
			}
			raw, _ := json.Marshal(r)
			if strings.Contains(string(raw), "never-report-this-secret") {
				t.Fatal("response body escaped into evidence")
			}
		})
	}
}

func TestCancellationProducesIncompleteReport(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	r := run(ctx, fixtureConfig(), Options{Events: 100, BatchSize: 50, Concurrency: 2})
	if r.Complete || r.Requests != 0 {
		t.Fatalf("canceled run succeeded: %+v", r)
	}
}
