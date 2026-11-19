//go:build integration

package pipeline

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/howtomakeaname/Evia/backend/internal/ingestion"
	"github.com/howtomakeaname/Evia/backend/internal/platform/contract"
	"github.com/howtomakeaname/Evia/backend/internal/platform/ids"
	"github.com/twmb/franz-go/pkg/kgo"
	"github.com/twmb/franz-go/pkg/kmsg"
)

// integrationIngestObserver records pipeline telemetry during the real
// Kafka/ClickHouse integration run.
type integrationIngestObserver struct {
	polled      int
	ingested    int
	quarantined int
	batchOK     int
	batchFailed int
}

func (o *integrationIngestObserver) MessagesPolled(count int) { o.polled += count }
func (o *integrationIngestObserver) BatchProcessed(succeeded bool, _ time.Duration) {
	if succeeded {
		o.batchOK++
	} else {
		o.batchFailed++
	}
}
func (o *integrationIngestObserver) RecordsIngested(count int)    { o.ingested += count }
func (o *integrationIngestObserver) RecordsQuarantined(count int) { o.quarantined += count }

func integrationConfig(t *testing.T) (KafkaConfig, ClickHouseConfig) {
	t.Helper()
	brokers := os.Getenv("EVIA_TEST_KAFKA_BROKERS")
	endpoint := os.Getenv("EVIA_TEST_CLICKHOUSE_URL")
	if brokers == "" || endpoint == "" {
		t.Fatal("integration requires EVIA_TEST_KAFKA_BROKERS and EVIA_TEST_CLICKHOUSE_URL")
	}
	suffix := strings.ReplaceAll(ids.New(), "-", "")
	return KafkaConfig{Brokers: strings.Split(brokers, ","), Topic: "evia.test.events." + suffix, DeadLetterTopic: "evia.test.quarantine." + suffix, Group: "evia-test-" + suffix, ClientID: "evia-integration"}, ClickHouseConfig{URL: endpoint, Database: "evia_pipeline_" + suffix, Username: os.Getenv("EVIA_TEST_CLICKHOUSE_USERNAME"), Password: os.Getenv("EVIA_TEST_CLICKHOUSE_PASSWORD"), Client: &http.Client{Timeout: 15 * time.Second}}
}
func clickhouseQuery(ctx context.Context, c ClickHouseConfig, sql string) ([]byte, error) {
	endpoint, e := url.Parse(c.URL)
	if e != nil {
		return nil, e
	}
	q := endpoint.Query()
	q.Set("query", sql)
	q.Set("wait_end_of_query", "1")
	if !strings.HasPrefix(strings.ToUpper(strings.TrimSpace(sql)), "CREATE DATABASE ") && !strings.HasPrefix(strings.ToUpper(strings.TrimSpace(sql)), "DROP DATABASE ") {
		q.Set("database", c.Database)
	}
	endpoint.RawQuery = q.Encode()
	request, e := http.NewRequestWithContext(ctx, http.MethodPost, endpoint.String(), nil)
	if e != nil {
		return nil, e
	}
	if c.Username != "" {
		request.SetBasicAuth(c.Username, c.Password)
	}
	response, e := c.Client.Do(request)
	if e != nil {
		return nil, e
	}
	defer response.Body.Close()
	body, e := io.ReadAll(io.LimitReader(response.Body, 1024*1024))
	if e != nil {
		return nil, e
	}
	if response.StatusCode != 200 {
		return nil, fmt.Errorf("ClickHouse test SQL failed HTTP %d: %s", response.StatusCode, body)
	}
	return body, nil
}
func preparePipelineDependencies(t *testing.T, ctx context.Context, k KafkaConfig, c ClickHouseConfig) *kgo.Client {
	t.Helper()
	client, e := kgo.NewClient(kgo.SeedBrokers(k.Brokers...), kgo.RequiredAcks(kgo.AllISRAcks()))
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(client.Close)
	request := kmsg.NewPtrCreateTopicsRequest()
	request.Topics = []kmsg.CreateTopicsRequestTopic{{Topic: k.Topic, NumPartitions: 1, ReplicationFactor: 1}, {Topic: k.DeadLetterTopic, NumPartitions: 1, ReplicationFactor: 1}}
	response, e := client.Request(ctx, request)
	if e != nil {
		t.Fatal("Kafka test topics:", e)
	}
	for _, topic := range response.(*kmsg.CreateTopicsResponse).Topics {
		if topic.ErrorCode != 0 {
			t.Fatal("Kafka topic create failed", topic.Topic, topic.ErrorCode)
		}
	}
	t.Cleanup(func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		request := kmsg.NewPtrDeleteTopicsRequest()
		for _, topic := range []string{k.Topic, k.DeadLetterTopic} {
			name := topic
			request.Topics = append(request.Topics, kmsg.DeleteTopicsRequestTopic{Topic: &name})
		}
		_, _ = client.Request(cleanup, request)
	})
	if _, e = clickhouseQuery(ctx, c, "CREATE DATABASE "+c.Database); e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		// SYNC makes the physical removal complete before the test returns so
		// back-to-back drills cannot accumulate deferred-delete residue and
		// exhaust a small tmpfs ClickHouse disk (NOT_ENOUGH_SPACE at insert).
		if _, e := clickhouseQuery(cleanup, c, "DROP DATABASE "+c.Database+" SYNC"); e != nil {
			t.Error(e)
		}
	})
	names, e := filepath.Glob("../../../deploy/clickhouse/*.sql")
	if e != nil {
		t.Fatal(e)
	}
	for _, name := range names {
		applyFactMigration(t, ctx, c, name)
	}
	return client
}
func integrationCount(t *testing.T, ctx context.Context, c ClickHouseConfig, from string) int {
	t.Helper()
	body, e := clickhouseQuery(ctx, c, "SELECT count() AS n FROM "+c.Database+"."+from+" FORMAT JSONEachRow")
	if e != nil {
		t.Fatal(e)
	}
	var out struct {
		N json.Number `json:"n"`
	}
	d := json.NewDecoder(bytes.NewReader(body))
	d.UseNumber()
	if e = d.Decode(&out); e != nil {
		t.Fatal(e, string(body))
	}
	n, e := out.N.Int64()
	if e != nil {
		t.Fatal(e)
	}
	return int(n)
}

type ambiguousSink struct {
	Sink
	once sync.Once
}

func (s *ambiguousSink) Write(ctx context.Context, records []ingestion.Record) error {
	if e := s.Sink.Write(ctx, records); e != nil {
		return e
	}
	lost := false
	s.once.Do(func() { lost = true })
	if lost {
		return ErrSinkUnavailable
	}
	return nil
}

type observingReader struct {
	Reader
	offset    int64
	committed chan struct{}
	once      sync.Once
}

func (r *observingReader) Commit(ctx context.Context, messages []Message) error {
	if e := r.Reader.Commit(ctx, messages); e != nil {
		return e
	}
	for _, message := range messages {
		if message.Offset >= r.offset {
			r.once.Do(func() { close(r.committed) })
		}
	}
	return nil
}
func TestKafkaClickHouseDurableReplayAndQuarantine(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	kafkaConfig, chConfig := integrationConfig(t)
	admin := preparePipelineDependencies(t, ctx, kafkaConfig, chConfig)
	producer, e := NewKafkaProducer(kafkaConfig)
	if e != nil {
		t.Fatal(e)
	}
	defer func() {
		closeCtx, stop := context.WithTimeout(context.Background(), 10*time.Second)
		defer stop()
		if e := producer.Close(closeCtx); e != nil {
			t.Error(e)
		}
	}()
	if e = producer.Ping(ctx); e != nil {
		t.Fatal(e)
	}
	sink, e := NewClickHouse(chConfig)
	if e != nil {
		t.Fatal(e)
	}
	first := testRecord()
	replay := first
	replay.ReceivedAt = first.ReceivedAt.Add(time.Second)
	conflict := replay
	conflict.EventKey = "poisoned_variant"
	conflict.PropertiesJSON = json.RawMessage(`{"count":99}`)
	conflict.ReceivedAt = first.ReceivedAt.Add(2 * time.Second)
	otherSource := first
	otherSource.SourceID = "44444444-4444-4444-8444-444444444444"
	otherProject := first
	otherProject.ProjectID = "55555555-5555-4555-8555-555555555555"
	if e = producer.Publish(ctx, []ingestion.Record{first, replay, conflict, otherSource, otherProject}); e != nil {
		t.Fatal("durable publication:", e)
	}
	poison := &kgo.Record{Topic: kafkaConfig.Topic, Value: []byte("poison")}
	if e = admin.ProduceSync(ctx, poison).FirstErr(); e != nil {
		t.Fatal(e)
	}
	reader, e := NewKafkaReader(kafkaConfig)
	if e != nil {
		t.Fatal(e)
	}
	observed := &observingReader{Reader: reader, offset: poison.Offset, committed: make(chan struct{})}
	runCtx, stop := context.WithCancel(ctx)
	done := make(chan error, 1)
	observer := &integrationIngestObserver{}
	worker := &Worker{Reader: observed, Sink: &ambiguousSink{Sink: sink}, Quarantine: producer, Observer: observer, BatchSize: 2, RetryInitial: time.Millisecond, RetryMax: 10 * time.Millisecond}
	go func() { done <- worker.Run(runCtx) }()
	select {
	case <-observed.committed:
	case <-ctx.Done():
		t.Fatal("worker did not persist/commit batch:", ctx.Err())
	}
	stop()
	if e = <-done; !errors.Is(e, context.Canceled) {
		t.Fatal(e)
	}
	// The observer must reflect the real durable batch mix: valid facts
	// ingested, the poison message quarantined, no failed batch.
	if observer.batchFailed != 0 || observer.batchOK == 0 {
		t.Fatalf("batch telemetry wrong: %+v", observer)
	}
	if observer.ingested == 0 || observer.quarantined == 0 {
		t.Fatalf("record telemetry must count both ingested facts and quarantine: %+v", observer)
	}
	if observer.polled == 0 {
		t.Fatalf("poll telemetry missing: %+v", observer)
	}
	rawCount := integrationCount(t, ctx, chConfig, "event_facts_raw")
	if rawCount <= 5 {
		t.Fatal("lost acknowledgement did not exercise durable replay", rawCount)
	}
	if n := integrationCount(t, ctx, chConfig, "event_facts"); n != 3 {
		t.Fatal("exact dedup or source/project scope failed", n)
	}
	scoped := "event_facts WHERE project_id = '" + testProject + "' AND source_id = '" + testSource + "' AND environment = 'development'"
	if n := integrationCount(t, ctx, chConfig, scoped); n != 1 {
		t.Fatal("scope count", n)
	}
	if n := integrationCount(t, ctx, chConfig, scoped+" AND JSONExtractInt(properties_json, 'count') = 99"); n != 0 {
		t.Fatal("property filtering occurred before dedup", n)
	}
	if n := integrationCount(t, ctx, chConfig, scoped+" AND event_key = 'poisoned_variant'"); n != 0 {
		t.Fatal("event filtering occurred before dedup", n)
	}
	if n := integrationCount(t, ctx, chConfig, scoped+" AND JSONHas(properties_json, 'count') AND JSONExtractInt(properties_json, 'count') = 0 AND JSONHas(properties_json, 'enabled') AND JSONExtractBool(properties_json, 'enabled') = 0"); n != 1 {
		t.Fatal("zero/false presence lost", n)
	}
	// The committed poison offset proves the DLQ write finished first. Verify the
	// private diagnostic retains the original source position and original bytes.
	dlqCtx, dlqCancel := context.WithTimeout(ctx, 10*time.Second)
	defer dlqCancel()
	dlq, e := kgo.NewClient(kgo.SeedBrokers(kafkaConfig.Brokers...), kgo.ConsumeTopics(kafkaConfig.DeadLetterTopic), kgo.ConsumeResetOffset(kgo.NewOffset().AtStart()))
	if e != nil {
		t.Fatal(e)
	}
	defer dlq.Close()
	fetches := dlq.PollRecords(dlqCtx, 1)
	if errs := fetches.Errors(); len(errs) > 0 {
		t.Fatal(errs)
	}
	records := fetches.Records()
	if len(records) != 1 {
		t.Fatal("missing durable quarantine record")
	}
	var diagnostic Poison
	if e = json.Unmarshal(records[0].Value, &diagnostic); e != nil || diagnostic.Topic != kafkaConfig.Topic || diagnostic.Offset != poison.Offset || string(diagnostic.Value) != "poison" {
		t.Fatal(diagnostic, e)
	}
	// A fresh client with the same group starts after the committed position.
	resumed, e := NewKafkaReader(kafkaConfig)
	if e != nil {
		t.Fatal(e)
	}
	resumeCtx, resumeCancel := context.WithTimeout(ctx, 3*time.Second)
	more, _ := resumed.Poll(resumeCtx, 10)
	resumeCancel()
	resumed.Close()
	if len(more) != 0 {
		t.Fatal("committed events unexpectedly replayed", len(more))
	}
	// Replaying an entire consumer group after a recovery also leaves exact facts
	// unchanged, even though the physical append-only row count grows.
	replayConfig := kafkaConfig
	replayConfig.Group += "-replay"
	replayReader, e := NewKafkaReader(replayConfig)
	if e != nil {
		t.Fatal(e)
	}
	replayObserved := &observingReader{Reader: replayReader, offset: poison.Offset, committed: make(chan struct{})}
	replayCtx, replayStop := context.WithCancel(ctx)
	replayDone := make(chan error, 1)
	go func() {
		replayDone <- (&Worker{Reader: replayObserved, Sink: sink, Quarantine: producer, BatchSize: 3}).Run(replayCtx)
	}()
	select {
	case <-replayObserved.committed:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	replayStop()
	if e = <-replayDone; !errors.Is(e, context.Canceled) {
		t.Fatal(e)
	}
	if n := integrationCount(t, ctx, chConfig, "event_facts"); n != 3 {
		t.Fatal("recovery replay inflated facts", n)
	}
	if n := integrationCount(t, ctx, chConfig, "event_facts_raw"); n <= rawCount {
		t.Fatal("full replay was not exercised", n, rawCount)
	}
}

type integrationSystemSource struct {
	source ingestion.AdmissionSource
}

func (r integrationSystemSource) ResolveSource(context.Context, string) (ingestion.AdmissionSource, error) {
	return r.source, nil
}

type integrationNoPlanResolver struct {
	t *testing.T
}

func (p integrationNoPlanResolver) ResolvePlan(context.Context, string, string, int64) (ingestion.Plan, error) {
	p.t.Fatal("sys.* events must never resolve a project release plan")
	return ingestion.Plan{}, nil
}

type integrationAllowBudget struct{}

func (integrationAllowBudget) ReserveCollection(context.Context, string, string, int) error {
	return nil
}

// TestSystemEventsFlowCollectorKafkaClickHouse drives two reserved sys.* events
// through the real admission collector, Kafka and ClickHouse materialiser,
// proving the embedded system catalog reaches queryable facts without any
// project release plan or catalog_events row.
func TestSystemEventsFlowCollectorKafkaClickHouse(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	kafkaConf, chConf := integrationConfig(t)
	admin := preparePipelineDependencies(t, ctx, kafkaConf, chConf)
	_ = admin

	producer, e := NewKafkaProducer(kafkaConf)
	if e != nil {
		t.Fatal(e)
	}
	defer func() {
		closeCtx, stop := context.WithTimeout(context.Background(), 10*time.Second)
		defer stop()
		if e := producer.Close(closeCtx); e != nil {
			t.Error(e)
		}
	}()
	if e = producer.Ping(ctx); e != nil {
		t.Fatal(e)
	}
	sink, e := NewClickHouse(chConf)
	if e != nil {
		t.Fatal(e)
	}

	validator, e := contract.New()
	if e != nil {
		t.Fatal(e)
	}
	catalog, e := ingestion.LoadSystemCatalog()
	if e != nil {
		t.Fatal(e)
	}
	resolver := integrationSystemSource{source: ingestion.AdmissionSource{
		Source: ingestion.Source{
			ID: testSource, ProjectID: testProject, Kind: ingestion.SourceKindBrowser, Environment: "development",
			Status: ingestion.SourceStatusActive, AllowedOrigins: []string{"https://site.example"},
		},
		Credential: ingestion.Credential{ID: testSource},
	}}
	collector, e := ingestion.NewCollector(resolver, integrationNoPlanResolver{t: t}, producer, integrationAllowBudget{}, validator)
	if e != nil {
		t.Fatal(e)
	}
	collector.WithSystemCatalog(catalog)

	now := time.Now().UTC()
	envelope := func(key string, version int64, properties map[string]any) json.RawMessage {
		raw, err := json.Marshal(map[string]any{
			"event_id":         ids.New(),
			"event_key":        key,
			"schema_version":   version,
			"release_revision": int64(7),
			"occurred_at":      now.Format(time.RFC3339Nano),
			"identity":         map[string]any{"anonymous_id": ids.New()},
			"context":          map[string]any{"tab_id": ids.New(), "sequence": 1, "page_view_id": ids.New()},
			"sdk":              map[string]any{"name": "evia-browser", "version": "0.1.0"},
			"properties":       properties,
		})
		if err != nil {
			t.Fatal(err)
		}
		return raw
	}
	batch := ingestion.Batch{
		ID:       ids.New(),
		WriteKey: "public-system-key",
		SentAt:   now,
		Events: []json.RawMessage{
			envelope("sys.page_view", 1, map[string]any{}),
			envelope("sys.page_scroll", 1, map[string]any{
				"from_ratio": 0, "to_ratio": 0.6, "max_ratio": 0.6,
				"direction": "down", "duration_ms": 3000,
			}),
		},
	}
	receipt, status, e := collector.Collect(ctx, batch, ingestion.Authentication{Origin: "https://site.example"})
	if e != nil || status != 202 {
		t.Fatalf("system event collection failed: status=%d err=%v", status, e)
	}
	for i, result := range receipt.Results {
		if result.Status != ingestion.ItemStatusAccepted {
			t.Fatalf("event %d not accepted: %+v", i, result)
		}
	}

	reader, e := NewKafkaReader(kafkaConf)
	if e != nil {
		t.Fatal(e)
	}
	observer := &integrationIngestObserver{}
	worker := &Worker{
		Reader: reader, Sink: sink, Quarantine: producer, Observer: observer,
		BatchSize: 2, RetryInitial: time.Millisecond, RetryMax: 10 * time.Millisecond,
	}
	runCtx, stop := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() { done <- worker.Run(runCtx) }()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if integrationCount(t, ctx, chConf, "event_facts_raw") == 2 {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	stop()
	if e = <-done; !errors.Is(e, context.Canceled) {
		t.Fatal(e)
	}
	if observer.quarantined != 0 || observer.ingested < 2 {
		t.Fatalf("worker telemetry wrong: %+v", observer)
	}
	if n := integrationCount(t, ctx, chConf, "event_facts_raw"); n != 2 {
		t.Fatalf("raw fact count = %d, want 2", n)
	}
	if n := integrationCount(t, ctx, chConf, "event_facts"); n != 2 {
		t.Fatalf("view fact count = %d, want 2", n)
	}
	rows, e := clickhouseQuery(ctx, chConf, "SELECT event_key, schema_version, release_revision FROM event_facts ORDER BY event_key FORMAT JSONEachRow")
	if e != nil {
		t.Fatal(e)
	}
	var facts []struct {
		EventKey        string `json:"event_key"`
		SchemaVersion   int64  `json:"schema_version"`
		ReleaseRevision int64  `json:"release_revision"`
	}
	for _, line := range strings.Split(strings.TrimSpace(string(rows)), "\n") {
		var fact struct {
			EventKey        string `json:"event_key"`
			SchemaVersion   int64  `json:"schema_version"`
			ReleaseRevision int64  `json:"release_revision"`
		}
		if e = json.Unmarshal([]byte(line), &fact); e != nil {
			t.Fatal(e, line)
		}
		facts = append(facts, fact)
	}
	if len(facts) != 2 || facts[0].EventKey != "sys.page_scroll" || facts[1].EventKey != "sys.page_view" {
		t.Fatalf("system facts missing or misordered: %+v", facts)
	}
	for _, fact := range facts {
		if fact.SchemaVersion != 1 || fact.ReleaseRevision != 7 {
			t.Fatalf("provenance lost on %+v", fact)
		}
	}
	scroll := "event_facts WHERE event_key = 'sys.page_scroll' AND JSONExtractString(properties_json, 'direction') = 'down' AND JSONExtractInt(properties_json, 'duration_ms') = 3000 AND source_kind = 'browser' AND project_id = '" + testProject + "'"
	if n := integrationCount(t, ctx, chConf, scroll); n != 1 {
		t.Fatal("scroll properties or scope did not reach queryable facts", n)
	}
}
