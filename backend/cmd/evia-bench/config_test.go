package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/howtomakeaname/Evia/backend/internal/platform/contract"
)

func TestPrivateConfigRejectsUnsafeScopeAndFile(t *testing.T) {
	c := fixtureConfig()
	path := filepath.Join(t.TempDir(), "private.json")
	raw, _ := json.Marshal(c)
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := readConfig(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := readConfig(path); err == nil {
		t.Fatal("accepted public credential file")
	}
	for _, url := range []string{"http://example.com", "http://localhost:8080", "http://127.0.0.1:8080/path", "http://user:secret@127.0.0.1:8080", "http://127.0.0.1:8080?secret=key", "https://127.0.0.1"} {
		copy := c
		copy.APIURL = url
		if copy.validate() == nil {
			t.Errorf("accepted unsafe endpoint %q", url)
		}
	}
	for _, o := range []Options{{Events: 0, BatchSize: 50, Concurrency: 1, Out: path}, {Events: 1, BatchSize: 51, Concurrency: 1, Out: path}, {Events: 1, BatchSize: 50, Concurrency: 129, Out: path}, {Events: 1, BatchSize: 50, Concurrency: 1, ReplayPercent: 101, Out: path}} {
		if o.validate() == nil {
			t.Errorf("accepted unsafe workload %+v", o)
		}
	}
}

func TestTargetRateDropsInsteadOfQueueingUnboundedWork(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(40 * time.Millisecond)
		var request struct {
			BatchID string `json:"batch_id"`
		}
		json.NewDecoder(r.Body).Decode(&request)
		w.WriteHeader(429)
	}))
	defer s.Close()
	c := fixtureConfig()
	c.APIURL = s.URL
	r := run(context.Background(), c, Options{Events: 1000, BatchSize: 50, Concurrency: 1, Rate: 1000000})
	if r.Unique.Dropped == 0 || r.Unique.Attempted+r.Unique.Dropped != 1000 || r.Complete || r.Mode != "open_loop_target_rate_drop_when_busy" {
		t.Fatalf("unbounded or inaccurate scheduler: %+v", r)
	}
}

func TestDeterministicDistributionMatchesExpectedFacts(t *testing.T) {
	c := fixtureConfig()
	o := Options{Events: 1000000}
	a := makeBatch(c, o, 100000, 2, false)
	b := makeBatch(c, o, 100000, 2, true)
	if string(a.body) != string(b.body) {
		t.Fatal("replay not byte stable")
	}
	first := a.request.Events[0]
	if *first.Identity.UID != "bench-"+c.RunID+"-0" || first.Properties["section"] != "section-0" || first.Properties["page"] != "/page/0" || first.Properties["post_id"] != "post-0" || first.Properties["value"] != 0 || first.Properties["duration_ms"] != 0 {
		t.Fatalf("ordinal distribution changed: %+v", first)
	}
	when, err := time.Parse(time.RFC3339Nano, first.OccurredAt)
	if err != nil || !when.Equal(c.StartedAt.Add(-27*time.Minute)) {
		t.Fatal("fixed timestamp distribution changed")
	}
	if !uuidPattern.MatchString(first.EventID) || !uuidPattern.MatchString(*first.Context.SessionID) {
		t.Fatal("invalid synthetic UUID")
	}
	v, err := contract.New()
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := contract.Decode(a.body)
	if err != nil {
		t.Fatal(err)
	}
	if err := v.Validate("CollectRequest", decoded); err != nil {
		t.Fatalf("generated payload violated authoritative contract: %v", err)
	}
	facts := expectedFacts(1000000, 100000)
	if facts.Count != 1000000 || facts.UIDCardinality != 100000 || facts.ValueSum != 49500000 || facts.DurationMSAverage != 499.5 || facts.SectionZeroCount != 100000 {
		t.Fatalf("incorrect expected facts: %+v", facts)
	}
}
