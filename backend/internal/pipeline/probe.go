package pipeline

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/howtomakeaname/Evia/backend/internal/delivery"
	"github.com/howtomakeaname/Evia/backend/internal/platform/contract"
	"github.com/howtomakeaname/Evia/backend/internal/platform/env"
	"github.com/twmb/franz-go/pkg/kgo"
)

type probeMessage struct {
	Version int                   `json:"version"`
	Request delivery.ProbeRequest `json:"request"`
}
type ProbeClient struct {
	producer *KafkaProducer
	store    *probeStore
}
type probeStore struct{ ch *ClickHouse }

func NewProbeClient(kafka KafkaConfig, ch ClickHouseConfig) (*ProbeClient, error) {
	store, e := NewClickHouse(ch)
	if e != nil {
		return nil, e
	}
	producer, e := NewKafkaProducer(kafka)
	if e != nil {
		return nil, e
	}
	return &ProbeClient{producer: producer, store: &probeStore{ch: store}}, nil
}
func (c *ProbeClient) Close(ctx context.Context) error { return c.producer.Close(ctx) }
func (c *ProbeClient) Ping(ctx context.Context) error {
	if e := c.producer.Ping(ctx); e != nil {
		return e
	}
	_, e := c.store.query(ctx, "SELECT nonce FROM pipeline_readiness LIMIT 0 FORMAT JSONEachRow", nil, nil)
	return e
}
func (c *ProbeClient) Verify(ctx context.Context, request delivery.ProbeRequest) (delivery.Proof, error) {
	if !validProbe(request, time.Now().UTC()) {
		return delivery.Proof{}, delivery.Unavailable("PIPELINE_PROBE_INVALID")
	}
	bounded, cancel := context.WithDeadline(ctx, request.ExpiresAt)
	defer cancel()
	raw, e := json.Marshal(probeMessage{Version: 1, Request: request})
	if e != nil {
		return delivery.Proof{}, e
	}
	release, e := c.producer.admit(bounded)
	if e != nil {
		return delivery.Proof{}, e
	}
	if e = c.producer.publish(bounded, []*kgo.Record{{Topic: c.producer.topic, Key: []byte(request.Nonce), Value: raw, Timestamp: time.Now().UTC()}}, release); e != nil {
		return delivery.Proof{}, e
	}
	timer := time.NewTicker(50 * time.Millisecond)
	defer timer.Stop()
	for {
		proof, found, e := c.store.read(bounded, request)
		if e == nil && found {
			if e = delivery.ValidateProof(request, proof, time.Now().UTC()); e != nil {
				return delivery.Proof{}, e
			}
			return proof, nil
		}
		select {
		case <-bounded.Done():
			return delivery.Proof{}, delivery.Unavailable("PIPELINE_READBACK_TIMEOUT")
		case <-timer.C:
		}
	}
}
func validProbe(r delivery.ProbeRequest, now time.Time) bool {
	return uuidPattern.MatchString(r.ProjectID) && uuidPattern.MatchString(r.ReleaseID) && uuidPattern.MatchString(r.Nonce) && r.Revision > 0 && r.Revision <= maxInteger && len(r.ManifestHash) == 64 && strings.Trim(r.ManifestHash, "abcdef0123456789") == "" && env.Valid(r.Environment) && !r.IssuedAt.Before(now.Add(-20*time.Second)) && !r.IssuedAt.After(now.Add(time.Second)) && r.ExpiresAt.After(now) && r.ExpiresAt.Sub(r.IssuedAt) <= 20*time.Second
}
func decodeProbe(raw []byte) (delivery.ProbeRequest, error) {
	var message probeMessage
	if len(raw) > 4096 {
		return message.Request, errInvalidRecord
	}
	if _, e := contract.Decode(raw); e != nil {
		return message.Request, errInvalidRecord
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if e := d.Decode(&message); e != nil || message.Version != 1 || !validProbe(message.Request, time.Now().UTC()) {
		return message.Request, errInvalidRecord
	}
	return message.Request, nil
}
func (s *probeStore) query(ctx context.Context, sql string, params map[string]string, body []byte) ([]byte, error) {
	endpoint := *s.ch.endpoint
	q := endpoint.Query()
	q.Set("database", s.ch.database)
	q.Set("query", sql)
	q.Set("wait_end_of_query", "1")
	q.Set("async_insert", "0")
	q.Set("date_time_input_format", "best_effort")
	for key, value := range params {
		q.Set("param_"+key, value)
	}
	endpoint.RawQuery = q.Encode()
	request, e := http.NewRequestWithContext(ctx, http.MethodPost, endpoint.String(), bytes.NewReader(body))
	if e != nil {
		return nil, ErrSinkUnavailable
	}
	if s.ch.username != "" {
		request.SetBasicAuth(s.ch.username, s.ch.password)
	}
	response, e := s.ch.client.Do(request)
	if e != nil {
		return nil, ErrSinkUnavailable
	}
	defer response.Body.Close()
	raw, e := io.ReadAll(io.LimitReader(response.Body, 1024*1024+1))
	if e != nil || len(raw) > 1024*1024 || response.StatusCode != 200 {
		return nil, ErrSinkUnavailable
	}
	return raw, nil
}
func (s *probeStore) capability(ctx context.Context) error {
	raw, e := s.query(ctx, `SELECT table,name,type FROM system.columns WHERE database={database:String} AND table IN ('event_facts_raw','event_facts') FORMAT JSONEachRow`, map[string]string{"database": s.ch.database}, nil)
	if e != nil {
		return e
	}
	required := map[string]string{"project_id": "String", "environment": "LowCardinality(String)", "source_id": "String", "source_kind": "LowCardinality(String)", "event_id": "String", "event_key": "LowCardinality(String)", "schema_version": "UInt64", "release_revision": "UInt64", "canonical_hash": "FixedString(64)", "uid": "Nullable(String)", "context_json": "String", "properties_json": "String", "event_time": "DateTime64(9, 'UTC')", "occurred_at": "DateTime64(9, 'UTC')", "received_at": "DateTime64(9, 'UTC')"}
	for _, name := range []string{"anonymous_id", "session_id", "tab_id", "page_view_id", "source_page_view_id", "navigation_id", "operation_id", "route_name", "route_template", "source_module", "navigation_type", "previous_page_view_id"} {
		required[name] = "Nullable(String)"
	}
	required["sequence"] = "Nullable(UInt64)"
	required["sdk_name"] = "String"
	required["sdk_version"] = "String"
	found := map[string]map[string]string{}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	for {
		var row struct{ Table, Name, Type string }
		e = decoder.Decode(&row)
		if e == io.EOF {
			break
		}
		if e != nil {
			return ErrSinkUnavailable
		}
		if found[row.Table] == nil {
			found[row.Table] = map[string]string{}
		}
		found[row.Table][row.Name] = row.Type
	}
	for _, table := range []string{"event_facts_raw", "event_facts"} {
		for name, kind := range required {
			if table == "event_facts_raw" && (name == "event_time" || name == "occurred_at") {
				continue
			}
			actual := found[table][name]
			if kind == "LowCardinality(String)" && actual == "String" && table == "event_facts" {
				continue
			}
			if actual != kind {
				return delivery.Unavailable("PIPELINE_SCHEMA_CAPABILITY_MISSING")
			}
		}
	}
	for _, name := range []string{"event_time_precise", "occurred_at_precise"} {
		if found["event_facts_raw"][name] != "Nullable(DateTime64(9, 'UTC'))" {
			return delivery.Unavailable("PIPELINE_SCHEMA_CAPABILITY_MISSING")
		}
	}
	// Verify effective grants on the real fact objects as well as the receipt
	// table. A receipt-only writer must never make collection appear ready.
	for _, grant := range []string{"INSERT ON event_facts_raw", "SELECT ON event_facts"} {
		raw, e = s.query(ctx, "CHECK GRANT "+grant, nil, nil)
		if e != nil {
			return e
		}
		if strings.TrimSpace(string(raw)) != "1" {
			return delivery.Unavailable("PIPELINE_FACT_GRANT_MISSING")
		}
	}
	return nil
}
func stamp(t time.Time) string { return t.UTC().Format("2006-01-02 15:04:05.000000000") }
func (s *probeStore) write(ctx context.Context, requests []delivery.ProbeRequest) error {
	if e := s.capability(ctx); e != nil {
		return e
	}
	var body bytes.Buffer
	encoder := json.NewEncoder(&body)
	now := time.Now().UTC()
	for _, request := range requests {
		if !validProbe(request, now) {
			return errInvalidRecord
		}
		row := map[string]any{"project_id": request.ProjectID, "environment": request.Environment, "release_id": request.ReleaseID, "revision": request.Revision, "manifest_hash": request.ManifestHash, "nonce": request.Nonce, "issued_at": stamp(request.IssuedAt), "expires_at": stamp(request.ExpiresAt), "capability": delivery.Capability, "observed_at": stamp(now)}
		if e := encoder.Encode(row); e != nil {
			return e
		}
	}
	raw, e := s.query(ctx, "INSERT INTO pipeline_readiness FORMAT JSONEachRow", nil, body.Bytes())
	if e != nil || strings.TrimSpace(string(raw)) != "" {
		return ErrSinkUnavailable
	}
	return nil
}
func (s *probeStore) read(ctx context.Context, r delivery.ProbeRequest) (delivery.Proof, bool, error) {
	params := map[string]string{"project": r.ProjectID, "environment": r.Environment, "release": r.ReleaseID, "revision": strconv.FormatInt(r.Revision, 10), "hash": r.ManifestHash, "nonce": r.Nonce, "issued": stamp(r.IssuedAt), "expires": stamp(r.ExpiresAt)}
	raw, e := s.query(ctx, `SELECT toString(observed_at) AS observed_at,capability FROM pipeline_readiness WHERE project_id={project:String} AND environment={environment:String} AND release_id={release:String} AND revision={revision:UInt64} AND manifest_hash={hash:String} AND nonce={nonce:UUID} AND issued_at={issued:DateTime64(9,'UTC')} AND expires_at={expires:DateTime64(9,'UTC')} AND expires_at>now64(9) ORDER BY observed_at LIMIT 1 FORMAT JSONEachRow`, params, nil)
	if e != nil {
		return delivery.Proof{}, false, e
	}
	if len(bytes.TrimSpace(raw)) == 0 {
		return delivery.Proof{}, false, nil
	}
	var row struct {
		Observed   string `json:"observed_at"`
		Capability string `json:"capability"`
	}
	if e = json.Unmarshal(bytes.TrimSpace(raw), &row); e != nil {
		return delivery.Proof{}, false, e
	}
	observed, e := time.ParseInLocation("2006-01-02 15:04:05.999999999", row.Observed, time.UTC)
	if e != nil {
		return delivery.Proof{}, false, e
	}
	return delivery.Proof{ProbeRequest: r, Capability: row.Capability, ObservedAt: observed}, true, nil
}

type ProbeWorker struct {
	closeErr error
	reader   *KafkaReader
	producer *KafkaProducer
	store    *probeStore
	once     sync.Once
}

func NewProbeWorker(kafka KafkaConfig, ch ClickHouseConfig) (*ProbeWorker, error) {
	store, e := NewClickHouse(ch)
	if e != nil {
		return nil, e
	}
	producer, e := NewKafkaProducer(kafka)
	if e != nil {
		return nil, e
	}
	reader, e := NewKafkaReader(kafka)
	if e != nil {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = producer.Close(ctx)
		return nil, e
	}
	return &ProbeWorker{reader: reader, producer: producer, store: &probeStore{ch: store}}, nil
}
func (w *ProbeWorker) Ping(ctx context.Context) error {
	if e := w.producer.Ping(ctx); e != nil {
		return e
	}
	return w.store.capability(ctx)
}
func (w *ProbeWorker) Close(ctx context.Context) error {
	w.once.Do(func() { w.reader.Close(); w.closeErr = w.producer.Close(ctx) })
	return w.closeErr
}
func (w *ProbeWorker) Run(ctx context.Context) (runErr error) {
	defer func() {
		closeCtx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
		defer cancel()
		runErr = errors.Join(runErr, w.Close(closeCtx))
	}()
	for {
		messages, e := w.reader.Poll(ctx, 16)
		if e != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return e
		}
		requests := []delivery.ProbeRequest{}
		poison := []Poison{}
		for _, message := range messages {
			request, e := decodeProbe(message.Value)
			if e != nil {
				poison = append(poison, Poison{Version: 1, Topic: message.Topic, Partition: message.Partition, Offset: message.Offset, Code: "INVALID_READINESS_PROBE", Value: message.Value})
			} else {
				requests = append(requests, request)
			}
		}
		// Readiness is the proof that the same Kafka→ClickHouse path is alive. A
		// transient dependency outage must therefore leave the batch uncommitted and
		// keep retrying, rather than terminate this task and make the supervisor stop
		// the otherwise recoverable fact worker. Context cancellation still exits
		// promptly; permanent failures remain visible as a stalled readiness lease.
		if len(requests) > 0 {
			e = retry(ctx, 100*time.Millisecond, time.Second, func() error { return w.store.write(ctx, requests) })
		}
		if e == nil && len(poison) > 0 {
			e = retry(ctx, 100*time.Millisecond, time.Second, func() error { return w.producer.Quarantine(ctx, poison) })
		}
		if e == nil && len(messages) > 0 {
			e = retry(ctx, 100*time.Millisecond, time.Second, func() error { return w.reader.Commit(ctx, messages) })
		}
		w.reader.AllowRebalance()
		if e != nil {
			return e
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
	}
}

var _ delivery.Probe = (*ProbeClient)(nil)
