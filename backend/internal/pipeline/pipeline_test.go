package pipeline

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/howtomakeaname/Evia/backend/internal/ingestion"
	"github.com/twmb/franz-go/pkg/kgo"
)

const testProject = "11111111-1111-4111-8111-111111111111"
const testSource = "22222222-2222-4222-8222-222222222222"
const testEvent = "33333333-3333-4333-8333-333333333333"

func testRecord() ingestion.Record {
	now := time.Date(2026, 11, 7, 10, 11, 12, 123456789, time.UTC)
	return ingestion.Record{SourceKind: "browser", ProjectID: testProject, SourceID: testSource, Environment: "development", EventID: testEvent, EventKey: "page_opened", SchemaVersion: 1, ReleaseRevision: 1, OccurredAt: now, ReceivedAt: now, UID: "synthetic-user", ContextJSON: json.RawMessage(`{}`), PropertiesJSON: json.RawMessage(`{"count":0,"enabled":false,"title":"Home"}`), SDKName: "test", SDKVersion: "1.0.0"}
}
func testMessage(t *testing.T) Message {
	t.Helper()
	value, e := EncodeRecord(testRecord())
	if e != nil {
		t.Fatal(e)
	}
	return Message{Topic: "test.events", Partition: 1, Offset: 12, Value: value}
}
func TestRecordValidationCanonicalizationAndScope(t *testing.T) {
	r := testRecord()
	b, e := EncodeRecord(r)
	if e != nil {
		t.Fatal(e)
	}
	decoded, e := DecodeRecord(b)
	if e != nil || decoded.ProjectID != testProject || decoded.SourceID != testSource || decoded.Environment != "development" || decoded.EventID != testEvent {
		t.Fatal(decoded, e)
	}
	row, e := rowFor(decoded)
	if e != nil {
		t.Fatal(e)
	}
	replay := r
	replay.ReceivedAt = r.ReceivedAt.Add(time.Minute)
	replay.PropertiesJSON = json.RawMessage(`{"title":"Home","enabled":false,"count":0}`)
	replayRow, e := rowFor(replay)
	if e != nil {
		t.Fatal(e)
	}
	if row.CanonicalHash != replayRow.CanonicalHash {
		t.Fatal("receipt time or JSON object order changed content identity")
	}
	conflict := r
	conflict.PropertiesJSON = json.RawMessage(`{"count":1}`)
	conflictRow, e := rowFor(conflict)
	if e != nil || row.CanonicalHash == conflictRow.CanonicalHash {
		t.Fatal("different content has same identity", e)
	}
	if row.ReceivedAt != "2026-11-07 10:11:12.123456789" || row.EventTime != "2026-11-07 10:11:12.123456789" {
		t.Fatal("timestamp precision lost", row)
	}
	r.ContextJSON = json.RawMessage(`{"route_name":""}`)
	row, e = rowFor(r)
	if e != nil || row.RouteName == nil || *row.RouteName != "" || row.RouteTemplate != nil {
		t.Fatal("empty and absent context conflated", row, e)
	}
	for _, tc := range []struct {
		name string
		edit func(*ingestion.Record)
	}{
		{"scope", func(r *ingestion.Record) { r.ProjectID = "forged" }},
		{"missing identity", func(r *ingestion.Record) { r.UID = "" }},
		{"unknown environment", func(r *ingestion.Record) { r.Environment = "preview" }},
		{"unsafe schema", func(r *ingestion.Record) { r.SchemaVersion = maxInteger + 1 }},
		{"context mismatch", func(r *ingestion.Record) { r.ContextJSON = json.RawMessage(`{"operation_id":"other"}`) }},
		{"context unknown", func(r *ingestion.Record) { r.ContextJSON = json.RawMessage(`{"secret":"never"}`) }},
		{"context null", func(r *ingestion.Record) { r.ContextJSON = json.RawMessage(`{"route_name":null}`) }},
		{"properties null", func(r *ingestion.Record) { r.PropertiesJSON = json.RawMessage(`{"title":null}`) }},
		{"properties nested", func(r *ingestion.Record) { r.PropertiesJSON = json.RawMessage(`{"title":{"nested":1}}`) }},
		{"properties duplicate", func(r *ingestion.Record) { r.PropertiesJSON = json.RawMessage(`{"count":0,"count":1}`) }},
		{"properties unsafe", func(r *ingestion.Record) { r.PropertiesJSON = json.RawMessage(`{"count":9007199254740992}`) }},
		{"properties array limit", func(r *ingestion.Record) {
			r.PropertiesJSON = json.RawMessage(`{"values":[` + strings.Repeat(`0,`, 20) + `0]}`)
		}},
		{"invalid time", func(r *ingestion.Record) { r.OccurredAt = time.Time{} }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := testRecord()
			tc.edit(&r)
			if _, e := EncodeRecord(r); e == nil {
				t.Fatal("invalid record accepted")
			}
		})
	}
	for _, raw := range [][]byte{[]byte(`{"version":2,"record":{}}`), []byte(`{"version":1,"version":1,"record":{}}`), append(b, []byte(` {}`)...), []byte(strings.Repeat("x", MaxRecordBytes+1))} {
		if _, e := DecodeRecord(raw); e == nil {
			t.Fatal("poison record accepted")
		}
	}
}

type memoryReader struct {
	messages       []Message
	events         *[]string
	polls, commits int
	failCommit     bool
	cancel         context.CancelFunc
	pollErr        error
}

func (r *memoryReader) Poll(ctx context.Context, _ int) ([]Message, error) {
	*r.events = append(*r.events, "poll")
	r.polls++
	if r.polls == 1 {
		return r.messages, r.pollErr
	}
	<-ctx.Done()
	return nil, ctx.Err()
}
func (r *memoryReader) Commit(_ context.Context, messages []Message) error {
	*r.events = append(*r.events, "commit")
	r.commits++
	if len(messages) != len(r.messages) || messages[0].Offset != r.messages[0].Offset {
		panic("incorrect offset boundary")
	}
	if r.failCommit && r.commits == 1 {
		return errors.New("retry commit")
	}
	r.cancel()
	return nil
}
func (r *memoryReader) AllowRebalance() { *r.events = append(*r.events, "rebalance") }
func (r *memoryReader) Close()          { *r.events = append(*r.events, "close") }

type memorySink struct {
	failAlways bool
	events     *[]string
	calls      int
	fail       bool
	cancel     context.CancelFunc
}

func (s *memorySink) Write(_ context.Context, records []ingestion.Record) error {
	*s.events = append(*s.events, "sink")
	s.calls++
	if records[0].ProjectID != testProject {
		panic("scope lost")
	}
	if s.cancel != nil {
		s.cancel()
		return errors.New("blocked sink")
	}
	if s.failAlways || s.fail && s.calls == 1 {
		return errors.New("transient sink")
	}
	return nil
}

type memoryQuarantine struct {
	events *[]string
	calls  int
	fail   bool
	cancel context.CancelFunc
}

func (q *memoryQuarantine) Quarantine(_ context.Context, records []Poison) error {
	*q.events = append(*q.events, "quarantine")
	q.calls++
	if records[0].Offset != 13 || records[0].Code != "INVALID_INTERNAL_EVENT" || string(records[0].Value) != "poison" {
		panic("lost poison diagnostic identity")
	}
	if q.cancel != nil {
		q.cancel()
		return errors.New("unavailable quarantine")
	}
	if q.fail && q.calls == 1 {
		return errors.New("transient quarantine")
	}
	return nil
}
func TestWorkerDurabilityRetryAndOffsetOrder(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	events := []string{}
	reader := &memoryReader{events: &events, messages: []Message{testMessage(t), {Topic: "test.events", Partition: 1, Offset: 13, Value: []byte("poison")}}, failCommit: true, cancel: cancel}
	sink := &memorySink{events: &events, fail: true}
	quarantine := &memoryQuarantine{events: &events, fail: true}
	worker := Worker{Reader: reader, Sink: sink, Quarantine: quarantine, RetryInitial: time.Millisecond, RetryMax: time.Millisecond}
	if e := worker.Run(ctx); !errors.Is(e, context.Canceled) {
		t.Fatal(e)
	}
	expected := []string{"poll", "sink", "sink", "quarantine", "quarantine", "commit", "commit", "rebalance", "poll", "rebalance", "close"}
	if !reflect.DeepEqual(events, expected) {
		t.Fatal("write/quarantine/commit ordering", events)
	}
	if sink.calls != 2 || quarantine.calls != 2 || reader.commits != 2 {
		t.Fatal("unbounded or repeated processing", events)
	}
}
func TestWorkerShutdownNeverCommitsUnpersistedData(t *testing.T) {
	for _, stage := range []string{"sink", "quarantine"} {
		t.Run(stage, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			events := []string{}
			reader := &memoryReader{events: &events, messages: []Message{testMessage(t), {Topic: "test.events", Partition: 1, Offset: 13, Value: []byte("poison")}}, cancel: cancel}
			sink := &memorySink{events: &events}
			q := &memoryQuarantine{events: &events}
			if stage == "sink" {
				sink.cancel = cancel
			} else {
				q.cancel = cancel
			}
			worker := Worker{Reader: reader, Sink: sink, Quarantine: q, RetryInitial: time.Millisecond, RetryMax: time.Millisecond}
			if e := worker.Run(ctx); !errors.Is(e, context.Canceled) {
				t.Fatal(e)
			}
			if reader.commits != 0 || reader.polls != 1 {
				t.Fatal("advanced beyond unfinished batch", events)
			}
			if events[len(events)-1] != "close" {
				t.Fatal("reader not closed", events)
			}
		})
	}
}
func TestWorkerStopsOnMixedPollError(t *testing.T) {
	events := []string{}
	reader := &memoryReader{messages: []Message{testMessage(t)}, events: &events, pollErr: errors.New("partition unavailable")}
	worker := Worker{Reader: reader, Sink: &memorySink{events: &events}, Quarantine: &memoryQuarantine{events: &events}}
	if e := worker.Run(context.Background()); e == nil {
		t.Fatal("poll error ignored")
	}
	if reader.commits != 0 || !reflect.DeepEqual(events, []string{"poll", "rebalance", "close"}) {
		t.Fatal(events)
	}
}

type acknowledgedProducer struct {
	mu          sync.Mutex
	entered     chan struct{}
	enteredOnce sync.Once
	deferAcks   bool
	fail        error
	results     kgo.ProduceResults
	records     []*kgo.Record
	callbacks   []func(*kgo.Record, error)
	closed      bool
}

func (f *acknowledgedProducer) TryProduce(_ context.Context, record *kgo.Record, callback func(*kgo.Record, error)) {
	f.mu.Lock()
	index := len(f.records)
	f.records = append(f.records, record)
	if f.deferAcks {
		f.callbacks = append(f.callbacks, callback)
	}
	deferred := f.deferAcks
	err := f.fail
	if index < len(f.results) {
		err = f.results[index].Err
	}
	f.mu.Unlock()
	if f.entered != nil {
		f.enteredOnce.Do(func() { close(f.entered) })
	}
	if !deferred {
		callback(record, err)
	}
}
func (f *acknowledgedProducer) finish(err error) {
	f.mu.Lock()
	callbacks := f.callbacks
	f.callbacks = nil
	f.mu.Unlock()
	for _, callback := range callbacks {
		callback(nil, err)
	}
}
func (f *acknowledgedProducer) Ping(context.Context) error  { return nil }
func (f *acknowledgedProducer) Flush(context.Context) error { return f.fail }
func (f *acknowledgedProducer) Close()                      { f.closed = true }
func TestProducerWaitsForEveryDurableAcknowledgement(t *testing.T) {
	fake := &acknowledgedProducer{entered: make(chan struct{}), deferAcks: true}
	producer := &KafkaProducer{client: fake, topic: "events", deadLetter: "quarantine"}
	done := make(chan error, 1)
	go func() { done <- producer.Publish(context.Background(), []ingestion.Record{testRecord()}) }()
	<-fake.entered
	select {
	case e := <-done:
		t.Fatal("accepted before acknowledgement", e)
	default:
	}
	fake.finish(nil)
	if e := <-done; e != nil {
		t.Fatal(e)
	}
	if len(fake.records) != 1 || fake.records[0].Topic != "events" || len(fake.records[0].Key) != 32 {
		t.Fatal("missing deterministic subject partition", fake.records)
	}
	fake.deferAcks = false
	fake.records = nil
	fake.results = kgo.ProduceResults{{}, {Err: errors.New("ambiguous second broker acknowledgement")}}
	if e := producer.Publish(context.Background(), []ingestion.Record{testRecord(), testRecord()}); !errors.Is(e, ErrQueueUnavailable) {
		t.Fatal(e)
	}
}
func TestProducerTimeoutRetainsBoundedOutstandingAdmission(t *testing.T) {
	fake := &acknowledgedProducer{deferAcks: true}
	producer := &KafkaProducer{client: fake, topic: "events", deadLetter: "quarantine", slots: make(chan struct{}, 1), publishTimeout: 5 * time.Millisecond}
	start := time.Now()
	if e := producer.Publish(context.Background(), []ingestion.Record{testRecord()}); !errors.Is(e, ErrQueueUnavailable) {
		t.Fatal(e)
	}
	if time.Since(start) > time.Second {
		t.Fatal("caller pinned waiting for unknown broker outcome")
	}
	if len(producer.slots) != 1 {
		t.Fatal("unknown pending publication released its admission slot")
	}
	if e := producer.Publish(context.Background(), []ingestion.Record{testRecord()}); !errors.Is(e, ErrQueueUnavailable) || len(fake.records) != 1 {
		t.Fatal("overloaded producer admitted more work", e)
	}
	fake.finish(nil)
	if len(producer.slots) != 0 {
		t.Fatal("late acknowledgement did not free publication slot")
	}
	fake.deferAcks = false
	if e := producer.Publish(context.Background(), []ingestion.Record{testRecord()}); e != nil {
		t.Fatal("producer failed to recover after late acknowledgement", e)
	}
}
func TestProducerHonorsCallerCancellationWithoutAwaitingCallbacks(t *testing.T) {
	fake := &acknowledgedProducer{entered: make(chan struct{}), deferAcks: true}
	producer := &KafkaProducer{client: fake, topic: "events", deadLetter: "quarantine", publishTimeout: time.Hour}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- producer.Publish(ctx, []ingestion.Record{testRecord()}) }()
	<-fake.entered
	cancel()
	select {
	case e := <-done:
		if !errors.Is(e, ErrQueueUnavailable) {
			t.Fatal(e)
		}
	case <-time.After(time.Second):
		t.Fatal("canceled caller waited for unsafe idempotent record completion")
	}
	fake.finish(errors.New("late ambiguous error"))
}
func TestProducerPrevalidatesWholeBatchAndCloses(t *testing.T) {
	fake := &acknowledgedProducer{}
	producer := &KafkaProducer{client: fake, topic: "events", deadLetter: "quarantine"}
	invalid := testRecord()
	invalid.ProjectID = "foreign"
	if e := producer.Publish(context.Background(), []ingestion.Record{testRecord(), invalid}); e == nil || len(fake.records) != 0 {
		t.Fatal("partial local validation caused publication")
	}
	if e := producer.Close(context.Background()); e != nil || !fake.closed {
		t.Fatal(e)
	}
	if e := producer.Publish(context.Background(), []ingestion.Record{testRecord()}); !errors.Is(e, ErrQueueUnavailable) {
		t.Fatal(e)
	}
}
func TestClickHouseInsertIsBoundedScopedAndSynchronous(t *testing.T) {
	var observed factRow
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		query := r.URL.Query()
		if r.Method != "POST" || query.Get("query") != "INSERT INTO event_facts_raw FORMAT JSONEachRow" || query.Get("database") != "test_events" || query.Get("async_insert") != "0" || query.Get("wait_end_of_query") != "1" {
			t.Error("unsafe or asynchronous query", r.URL)
		}
		username, password, ok := r.BasicAuth()
		if !ok || username != "test" || password != "synthetic-only" {
			t.Error("missing database auth")
		}
		if e := json.NewDecoder(r.Body).Decode(&observed); e != nil {
			t.Error(e)
		}
		w.WriteHeader(200)
	}))
	defer server.Close()
	sink, e := NewClickHouse(ClickHouseConfig{URL: server.URL, Database: "test_events", Username: "test", Password: "synthetic-only"})
	if e != nil {
		t.Fatal(e)
	}
	record := testRecord()
	record.PropertiesJSON = json.RawMessage(`{"title":"'; DROP TABLE event_facts_raw; --","enabled":false,"count":0}`)
	if e = sink.Write(context.Background(), []ingestion.Record{record}); e != nil {
		t.Fatal(e)
	}
	if observed.ProjectID != testProject || observed.SourceID != testSource || observed.EventID != testEvent || observed.AnonymousID != nil || observed.Sequence != nil || !strings.Contains(observed.PropertiesJSON, "DROP TABLE") {
		t.Fatal("lost typed scope/value presence", observed)
	}
}
func TestClickHouseRejectsFailureLateErrorAndRedirect(t *testing.T) {
	for _, status := range []int{200, 307, 400, 503} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Location", "https://outside.invalid/")
				w.WriteHeader(status)
				_, _ = w.Write([]byte("private database diagnostic"))
			}))
			defer server.Close()
			sink, e := NewClickHouse(ClickHouseConfig{URL: server.URL, Database: "test_events"})
			if e != nil {
				t.Fatal(e)
			}
			if e = sink.Write(context.Background(), []ingestion.Record{testRecord()}); !errors.Is(e, ErrSinkUnavailable) || strings.Contains(e.Error(), "private") {
				t.Fatal("unsafe success or leaked error", e)
			}
		})
	}
}

func TestWorkerBatchBudgetReleasesOwnershipWithoutCommit(t *testing.T) {
	// Virtual time advances at the blocked retry timers, so scheduler pauses
	// cannot consume the batch budget before a second persistence attempt.
	synctest.Test(t, func(t *testing.T) {
		events := []string{}
		reader := &memoryReader{events: &events, messages: []Message{testMessage(t)}}
		sink := &memorySink{events: &events, failAlways: true}
		worker := Worker{Reader: reader, Sink: sink, Quarantine: &memoryQuarantine{events: &events}, BatchTimeout: 5 * time.Millisecond, RetryInitial: time.Millisecond, RetryMax: time.Millisecond}
		if e := worker.Run(context.Background()); !errors.Is(e, context.DeadlineExceeded) {
			t.Fatal(e)
		}
		want := []string{"poll"}
		for range sink.calls {
			want = append(want, "sink")
		}
		want = append(want, "rebalance", "close")
		if reader.commits != 0 || reader.polls != 1 || sink.calls < 2 || !reflect.DeepEqual(events, want) {
			t.Fatal("unfinished work advanced or did not release consumer in order", events)
		}
	})
}
func TestKafkaAdapterConfigurationKeepsIdempotentProducer(t *testing.T) {
	config := KafkaConfig{Brokers: []string{"127.0.0.1:1"}, Topic: "events", DeadLetterTopic: "quarantine", Group: "synthetic-test"}
	producer, e := NewKafkaProducer(config)
	if e != nil {
		t.Fatal(e)
	}
	if e = producer.Close(context.Background()); e != nil {
		t.Fatal(e)
	}
	reader, e := NewKafkaReader(config)
	if e != nil {
		t.Fatal(e)
	}
	reader.Close()
	config.Username = "synthetic"
	config.Password = "synthetic-only"
	if _, e = NewKafkaProducer(config); e == nil {
		t.Fatal("plaintext password authentication allowed")
	}
	config.Username = ""
	config.Password = ""
	config.DeadLetterTopic = config.Topic
	if _, e = NewKafkaProducer(config); e == nil {
		t.Fatal("quarantine routed back into input topic")
	}
}

func TestRecordUUIDCaseNormalizesDedupIdentity(t *testing.T) {
	id := "abcdefab-cdef-4abc-8def-abcdefabcdef"
	lower := testRecord()
	lower.EventID = id
	lower.AnonymousID = id
	lower.Context.SessionID = id
	lower.ContextJSON = json.RawMessage(`{"session_id":"` + id + `"}`)
	upper := lower
	upper.EventID = strings.ToUpper(id)
	upper.AnonymousID = strings.ToUpper(id)
	upper.Context.SessionID = strings.ToUpper(id)
	upper.ContextJSON = json.RawMessage(`{"session_id":"` + strings.ToUpper(id) + `"}`)
	a, e := rowFor(lower)
	if e != nil {
		t.Fatal(e)
	}
	b, e := rowFor(upper)
	if e != nil {
		t.Fatal(e)
	}
	if a.CanonicalHash != b.CanonicalHash || b.EventID != id || b.AnonymousID == nil || *b.AnonymousID != id || b.SessionID == nil || *b.SessionID != id {
		t.Fatal("UUID case changed event identity", a, b)
	}
}
