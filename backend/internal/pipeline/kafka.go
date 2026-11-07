package pipeline

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/json"
	"errors"
	"net"
	"regexp"
	"strings"
	"sync/atomic"
	"time"

	"github.com/howtomakeaname/Evia/backend/internal/ingestion"
	"github.com/twmb/franz-go/pkg/kgo"
	"github.com/twmb/franz-go/pkg/sasl/plain"
)

type KafkaConfig struct {
	Brokers         []string
	Topic           string
	DeadLetterTopic string
	Group           string
	ClientID        string
	TLS             *tls.Config
	Username        string
	Password        string
}

var topicPattern = regexp.MustCompile(`^[A-Za-z0-9._-]{1,249}$`)
var ErrQueueUnavailable = errors.New("durable event queue unavailable")

func kafkaOptions(c KafkaConfig) ([]kgo.Opt, error) {
	if len(c.Brokers) == 0 || !validTopic(c.Topic) {
		return nil, errors.New("Kafka brokers and valid topic are required")
	}
	for _, broker := range c.Brokers {
		if host, port, e := net.SplitHostPort(broker); e != nil || host == "" || port == "" {
			return nil, errors.New("invalid Kafka broker address")
		}
	}
	if (c.Username == "") != (c.Password == "") {
		return nil, errors.New("Kafka authentication requires both username and password")
	}
	if c.Username != "" && c.TLS == nil {
		return nil, errors.New("Kafka password authentication requires TLS")
	}
	opts := []kgo.Opt{kgo.SeedBrokers(c.Brokers...), kgo.ClientID(c.ClientID)}
	if c.TLS != nil {
		opts = append(opts, kgo.DialTLSConfig(c.TLS.Clone()))
	}
	if c.Username != "" {
		opts = append(opts, kgo.SASL(plain.Auth{User: c.Username, Pass: c.Password}.AsMechanism()))
	}
	return opts, nil
}
func validTopic(topic string) bool {
	return topicPattern.MatchString(topic) && topic != "." && topic != ".."
}

type producerClient interface {
	ProduceSync(context.Context, ...*kgo.Record) kgo.ProduceResults
	Ping(context.Context) error
	Flush(context.Context) error
	Close()
}
type KafkaProducer struct {
	client            producerClient
	topic, deadLetter string
	closed            atomic.Bool
}

func NewKafkaProducer(c KafkaConfig) (*KafkaProducer, error) {
	opts, e := kafkaOptions(c)
	if e != nil {
		return nil, e
	}
	if !validTopic(c.DeadLetterTopic) || c.DeadLetterTopic == c.Topic {
		return nil, errors.New("a separate valid Kafka quarantine topic is required")
	}
	// franz-go enables idempotent production by default. All in-sync replicas must
	// acknowledge; bounded buffers apply backpressure instead of growing memory.
	opts = append(opts, kgo.RequiredAcks(kgo.AllISRAcks()), kgo.StopProducerOnDataLossDetected(), kgo.MaxBufferedRecords(5000), kgo.MaxBufferedBytes(32*1024*1024), kgo.ProducerBatchMaxBytes(2*1024*1024), kgo.ProducerLinger(5*time.Millisecond), kgo.ProducerBatchCompression(kgo.ZstdCompression()))
	client, e := kgo.NewClient(opts...)
	if e != nil {
		return nil, e
	}
	return &KafkaProducer{client: client, topic: c.Topic, deadLetter: c.DeadLetterTopic}, nil
}
func (p *KafkaProducer) Ping(ctx context.Context) error { return p.client.Ping(ctx) }
func (p *KafkaProducer) Publish(ctx context.Context, records []ingestion.Record) error {
	if p.closed.Load() {
		return ErrQueueUnavailable
	}
	if len(records) < 1 || len(records) > MaxBatchRecords {
		return errors.New("invalid producer batch size")
	}
	out := make([]*kgo.Record, 0, len(records))
	// Encode the whole batch before sending any of it: local input failure cannot
	// produce a partially validated batch. Broker failures may still be ambiguous.
	for _, record := range records {
		value, e := EncodeRecord(record)
		if e != nil {
			return e
		}
		subject := record.UID
		if subject == "" {
			subject = record.AnonymousID
		}
		key := sha256.Sum256([]byte(strings.Join([]string{record.ProjectID, record.Environment, record.SourceID, subject}, "\x00")))
		out = append(out, &kgo.Record{Topic: p.topic, Key: key[:], Value: value, Timestamp: record.ReceivedAt})
	}
	if e := p.client.ProduceSync(ctx, out...).FirstErr(); e != nil {
		return ErrQueueUnavailable
	}
	return nil
}
func (p *KafkaProducer) Quarantine(ctx context.Context, poison []Poison) error {
	if p.closed.Load() {
		return ErrQueueUnavailable
	}
	out := make([]*kgo.Record, 0, len(poison))
	for _, record := range poison {
		value, e := json.Marshal(record)
		if e != nil {
			return ErrQueueUnavailable
		}
		// Original Kafka position is an immutable diagnostic identity. A failed offset
		// commit can replay quarantine records; operators deduplicate by this key.
		key, _ := json.Marshal([]any{record.Topic, record.Partition, record.Offset})
		out = append(out, &kgo.Record{Topic: p.deadLetter, Key: key, Value: value})
	}
	if e := p.client.ProduceSync(ctx, out...).FirstErr(); e != nil {
		return ErrQueueUnavailable
	}
	return nil
}

// Close stops new publications, drains acknowledged work within the caller's
// deadline, then releases connections. A failed drain never implies acceptance.
func (p *KafkaProducer) Close(ctx context.Context) error {
	if p.closed.Swap(true) {
		return nil
	}
	e := p.client.Flush(ctx)
	p.client.Close()
	if e != nil {
		return ErrQueueUnavailable
	}
	return nil
}

type KafkaReader struct{ client *kgo.Client }

func NewKafkaReader(c KafkaConfig) (*KafkaReader, error) {
	opts, e := kafkaOptions(c)
	if e != nil {
		return nil, e
	}
	if c.Group == "" || len(c.Group) > 128 {
		return nil, errors.New("a bounded Kafka consumer group is required")
	}
	opts = append(opts, kgo.ConsumeTopics(c.Topic), kgo.ConsumerGroup(c.Group), kgo.DisableAutoCommit(), kgo.BlockRebalanceOnPoll(), kgo.RebalanceTimeout(60*time.Second), kgo.ConsumeStartOffset(kgo.NewOffset().AtStart()), kgo.ConsumeResetOffset(kgo.NoResetOffset()), kgo.FetchIsolationLevel(kgo.ReadCommitted()), kgo.MaxConcurrentFetches(1), kgo.BrokerMaxReadBytes(16*1024*1024), kgo.MaxDecompressBatchBytes(8*1024*1024), kgo.FetchMaxBytes(8*1024*1024), kgo.FetchMaxPartitionBytes(2*1024*1024), kgo.FetchMaxWait(250*time.Millisecond))
	client, e := kgo.NewClient(opts...)
	if e != nil {
		return nil, e
	}
	return &KafkaReader{client: client}, nil
}
func (r *KafkaReader) Poll(ctx context.Context, max int) ([]Message, error) {
	fetches := r.client.PollRecords(ctx, max)
	// A mixed fetch error is never ignored: stop/restart rather than advance a
	// partition past records whose persistence status has not been established.
	if errs := fetches.Errors(); len(errs) > 0 {
		return nil, errors.New("Kafka poll failed")
	}
	out := make([]Message, 0, fetches.NumRecords())
	fetches.EachRecord(func(record *kgo.Record) {
		out = append(out, Message{Topic: record.Topic, Partition: record.Partition, Offset: record.Offset, LeaderEpoch: record.LeaderEpoch, Value: record.Value})
	})
	return out, nil
}
func (r *KafkaReader) Commit(ctx context.Context, messages []Message) error {
	records := make([]*kgo.Record, 0, len(messages))
	for _, message := range messages {
		records = append(records, &kgo.Record{Topic: message.Topic, Partition: message.Partition, Offset: message.Offset, LeaderEpoch: message.LeaderEpoch})
	}
	return r.client.CommitRecords(ctx, records...)
}
func (r *KafkaReader) AllowRebalance() { r.client.AllowRebalance() }
func (r *KafkaReader) Close()          { r.client.CloseAllowingRebalance() }

var _ ingestion.Queue = (*KafkaProducer)(nil)
var _ Reader = (*KafkaReader)(nil)
var _ Quarantiner = (*KafkaProducer)(nil)
