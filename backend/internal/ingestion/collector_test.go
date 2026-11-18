package ingestion

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/howtomakeaname/Evia/backend/internal/platform/contract"
	"github.com/howtomakeaname/Evia/backend/internal/platform/fault"
)

const testProject = "11111111-1111-4111-8111-111111111111"
const testSource = "22222222-2222-4222-8222-222222222222"
const testEvent = "33333333-3333-4333-8333-333333333333"

type sourceStub struct {
	value AdmissionSource
	err   error
	calls int
}

func (s *sourceStub) ResolveSource(context.Context, string) (AdmissionSource, error) {
	s.calls++
	return s.value, s.err
}

type planStub struct {
	value                                  Plan
	err                                    error
	calls                                  int
	requestedProject, requestedEnvironment string
}

func (s *planStub) ResolvePlan(_ context.Context, p, e string, r int64) (Plan, error) {
	s.calls++
	s.requestedProject, s.requestedEnvironment = p, e
	v := s.value
	v.Revision = r
	return v, s.err
}

type queueStub struct {
	records []Record
	err     error
	calls   int
}

func (q *queueStub) Publish(_ context.Context, records []Record) error {
	q.calls++
	q.records = append(q.records, records...)
	return q.err
}

type budgetStub struct {
	calls, count         int
	err                  error
	project, environment string
}

func (b *budgetStub) ReserveCollection(_ context.Context, p, e string, count int) error {
	b.calls++
	b.project, b.environment, b.count = p, e, count
	return b.err
}
func validEvent() map[string]any {
	return map[string]any{"event_id": testEvent, "event_key": "lesson_opened", "schema_version": 1, "release_revision": 1, "occurred_at": time.Now().UTC().Format(time.RFC3339Nano), "identity": map[string]any{"uid": "website-user"}, "context": map[string]any{"route_name": "", "tab_id": testSource, "sequence": 1}, "sdk": map[string]any{"name": "test-sdk", "version": "1"}, "properties": map[string]any{"lesson_id": 1}}
}
func eventJSON(v any) json.RawMessage { raw, _ := json.Marshal(v); return raw }
func collectorFixture(t *testing.T) (*Collector, *sourceStub, *planStub, *queueStub, *budgetStub) {
	t.Helper()
	v, e := contract.New()
	if e != nil {
		t.Fatal(e)
	}
	source := &sourceStub{value: AdmissionSource{Source: Source{ID: testSource, ProjectID: testProject, Kind: SourceKindBrowser, Environment: "development", Status: SourceStatusActive, AllowedOrigins: []string{"https://website.example"}}, Credential: Credential{ID: testSource}}}
	plans := &planStub{value: Plan{ProjectID: testProject, Environment: "development", Revision: 1, Ready: true, Active: true, ValidFor: time.Minute, Events: map[string]EventRule{"lesson_opened": {Key: "lesson_opened", Version: 1, AllowedSourceKinds: []string{SourceKindBrowser, SourceKindServer}, Schema: map[string]any{"type": "object", "properties": map[string]any{"lesson_id": map[string]any{"type": "integer", "minimum": 1}}, "required": []any{"lesson_id"}, "additionalProperties": false}}}}}
	queue, budget := &queueStub{}, &budgetStub{}
	c, e := NewCollector(source, plans, queue, budget, v)
	if e != nil {
		t.Fatal(e)
	}
	return c, source, plans, queue, budget
}
func batchOf(events ...json.RawMessage) Batch {
	return Batch{ID: testSource, WriteKey: "public-key", SentAt: time.Now().UTC(), Events: events}
}
func browserAuth() Authentication { return Authentication{Origin: "https://website.example"} }
func TestCollectionPartialAcknowledgementAndVerifiedScope(t *testing.T) {
	c, _, plans, queue, budget := collectorFixture(t)
	invalid := validEvent()
	invalid["properties"] = map[string]any{"lesson_id": "wrong"}
	out, status, e := c.Collect(context.Background(), batchOf(eventJSON(validEvent()), eventJSON(invalid), eventJSON(map[string]any{"event_id": "invalid"})), browserAuth())
	if e != nil || status != 202 || len(out.Results) != 3 || len(queue.records) != 1 || queue.calls != 1 {
		t.Fatal(status, out, e)
	}
	if out.Results[0].Status != "accepted" || out.Results[1].Error.Code != "PROPERTY_SCHEMA_MISMATCH" || out.Results[1].Error.Retryable || out.Results[2].Error.Code != "INVALID_EVENT" || out.Results[2].EventID != "" {
		t.Fatal("partial results lost invalid item semantics", out)
	}
	r := queue.records[0]
	if r.ProjectID != testProject || r.SourceID != testSource || r.SourceKind != SourceKindBrowser || r.Environment != "development" || r.UID != "website-user" || r.Context.Sequence == nil || *r.Context.Sequence != 1 || !strings.Contains(string(r.ContextJSON), `"route_name":""`) {
		t.Fatal("normalized scope/context lost", r)
	}
	if budget.calls != 1 || budget.count != 3 || budget.project != testProject || plans.calls != 1 || plans.requestedProject != testProject || plans.requestedEnvironment != "development" {
		t.Fatal("scope or batch work policy changed")
	}
	if _, _, e = c.Collect(context.Background(), batchOf(eventJSON(validEvent())), browserAuth()); e != nil || plans.calls != 1 {
		t.Fatal("compiled manifest cache not reused", e, plans.calls)
	}
}
func TestCollectionFailsClosedAndDoesNotAcknowledgeAmbiguousQueue(t *testing.T) {
	for _, tc := range []struct {
		name, code string
		change     func(*sourceStub, *planStub, *queueStub, *budgetStub)
		auth       Authentication
	}{
		{name: "disabled", code: "SOURCE_DISABLED", change: func(s *sourceStub, _ *planStub, _ *queueStub, _ *budgetStub) {
			s.value.Source.Status = SourceStatusDisabled
		}, auth: browserAuth()},
		{name: "origin", code: "SOURCE_ORIGIN_FORBIDDEN", auth: Authentication{Origin: "https://foreign.example"}},
		{name: "server public key alone", code: "SOURCE_CREDENTIAL_INVALID", change: func(s *sourceStub, _ *planStub, _ *queueStub, _ *budgetStub) {
			s.value.Source.Kind = SourceKindServer
			s.value.Credential.SecretDigest = Digest(strings.Repeat("s", 43))
		}},
		{name: "unready", code: "EVENT_PLAN_NOT_READY", change: func(_ *sourceStub, p *planStub, _ *queueStub, _ *budgetStub) { p.value.Ready = false }, auth: browserAuth()},
		{name: "missing eligibility horizon", code: "EVENT_PLAN_NOT_READY", change: func(_ *sourceStub, p *planStub, _ *queueStub, _ *budgetStub) { p.value.ValidFor = 0 }, auth: browserAuth()},
		{name: "unactivated", code: "EVENT_PLAN_NOT_READY", change: func(_ *sourceStub, p *planStub, _ *queueStub, _ *budgetStub) { p.value.Active = false }, auth: browserAuth()},
		{name: "cross project manifest", code: "EVENT_PLAN_NOT_READY", change: func(_ *sourceStub, p *planStub, _ *queueStub, _ *budgetStub) { p.value.ProjectID = testSource }, auth: browserAuth()},
		{name: "budget denial", code: "COLLECTION_BUDGET_EXCEEDED", change: func(_ *sourceStub, _ *planStub, _ *queueStub, b *budgetStub) {
			b.err = fault.New(fault.Limited, "COLLECTION_BUDGET_EXCEEDED", "budget exceeded")
		}, auth: browserAuth()},
		{name: "budget unavailable", code: "ADMISSION_BUDGET_UNAVAILABLE", change: func(_ *sourceStub, _ *planStub, _ *queueStub, b *budgetStub) { b.err = errors.New("Redis down") }, auth: browserAuth()},
		{name: "ambiguous broker acknowledgement", code: "DURABLE_QUEUE_UNAVAILABLE", change: func(_ *sourceStub, _ *planStub, q *queueStub, _ *budgetStub) {
			q.err = errors.New("broker response lost")
		}, auth: browserAuth()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, s, p, q, b := collectorFixture(t)
			if tc.change != nil {
				tc.change(s, p, q, b)
			}
			out, _, e := c.Collect(context.Background(), batchOf(eventJSON(validEvent())), tc.auth)
			if e == nil || fault.Public(e).Code != tc.code || len(out.Results) != 0 {
				t.Fatal("failure returned successful acknowledgement", out, e)
			}
		})
	}
}
func TestServerCredentialsAndPermanentErrors(t *testing.T) {
	c, source, _, queue, _ := collectorFixture(t)
	source.value.Source.Kind = SourceKindServer
	secret := strings.Repeat("s", 43)
	source.value.Credential.SecretDigest = Digest(secret)
	if _, status, e := c.Collect(context.Background(), batchOf(eventJSON(validEvent())), Authentication{Bearer: secret}); e != nil || status != 202 || queue.records[0].SourceKind != SourceKindServer {
		t.Fatal("server credential did not authorize its source", status, e)
	}
	c, _, plans, queue, _ := collectorFixture(t)
	plans.err = fault.New(fault.Invalid, "SCHEMA_NOT_PUBLISHED", "not published")
	out, status, e := c.Collect(context.Background(), batchOf(eventJSON(validEvent())), browserAuth())
	if e != nil || status != 422 || out.Results[0].Error.Code != "SCHEMA_NOT_PUBLISHED" || queue.calls != 0 {
		t.Fatal(status, out, e)
	}
}
func TestCollectionWorkAndSizeBounds(t *testing.T) {
	c, _, plans, queue, _ := collectorFixture(t)
	events := make([]json.RawMessage, 50)
	for i := range events {
		v := validEvent()
		v["release_revision"] = i + 1
		events[i] = eventJSON(v)
	}
	out, status, e := c.Collect(context.Background(), batchOf(events...), browserAuth())
	if e != nil || status != 202 || plans.calls > 8 || len(queue.records) != 8 || len(out.Results) != 50 || out.Results[49].Error == nil || !out.Results[49].Error.Retryable {
		t.Fatal("unbounded version-cache miss amplification", status, plans.calls, len(queue.records), e)
	}
	c, _, _, queue, _ = collectorFixture(t)
	oversize := validEvent()
	oversize["properties"] = map[string]any{"value": strings.Repeat("a", 9000)}
	out, status, e = c.Collect(context.Background(), batchOf(eventJSON(oversize)), browserAuth())
	if e != nil || status != 422 || out.Results[0].Error.Code != "EVENT_TOO_LARGE" || queue.calls != 0 {
		t.Fatal("oversize event was accepted", out, e)
	}
	old := validEvent()
	old["occurred_at"] = time.Now().Add(-2 * time.Hour).Format(time.RFC3339)
	out, status, e = c.Collect(context.Background(), batchOf(eventJSON(old)), browserAuth())
	if e != nil || status != 422 || out.Results[0].Error.Code != "EVENT_TOO_OLD" {
		t.Fatal(out, e)
	}
}
func TestExpiredSourceCacheFailsClosed(t *testing.T) {
	s := &sourceStub{value: AdmissionSource{Source: Source{ID: testSource, Status: SourceStatusActive}}}
	cache := NewCachedSources(s)
	if _, e := cache.ResolveSource(context.Background(), "key"); e != nil {
		t.Fatal(e)
	}
	s.err = errors.New("database unavailable")
	cache.mu.Lock()
	entry := cache.entries["key"]
	entry.expires = time.Now().Add(-time.Second)
	cache.entries["key"] = entry
	cache.mu.Unlock()
	if _, e := cache.ResolveSource(context.Background(), "key"); e == nil {
		t.Fatal("expired cache served stale authorization on dependency error")
	}
}
func TestSourceValidationAndEncryptionKeyBounds(t *testing.T) {
	if _, e := NewSourceService(nil, []byte("short"), nil); e == nil {
		t.Fatal("short encryption key accepted")
	}
	for _, origins := range [][]string{{"*"}, {"https://user:secret@example.com"}, {"https://example.com/path"}, {"null"}} {
		if _, e := NormalizeOrigins(origins); e == nil {
			t.Fatal("unsafe origin accepted", origins)
		}
	}
	origins, e := NormalizeOrigins([]string{"https://b.example", "https://a.example", "https://b.example"})
	if e != nil || len(origins) != 2 || origins[0] != "https://a.example" {
		t.Fatal(origins, e)
	}
}
