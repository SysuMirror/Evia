//go:build integration

package pipeline

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/howtomakeaname/Evia/backend/internal/platform/ids"
	"github.com/twmb/franz-go/pkg/kgo"
	"github.com/twmb/franz-go/pkg/kmsg"
)

// TestKafkaReaderSampleLagAgainstRealBroker exercises lag math against a real
// Kafka broker: no committed position means no sample, a full commit yields
// zero lag, and uncommitted production increases lag by exactly the delta.
func TestKafkaReaderSampleLagAgainstRealBroker(t *testing.T) {
	brokers := os.Getenv("EVIA_TEST_KAFKA_BROKERS")
	if brokers == "" {
		t.Skip("integration requires EVIA_TEST_KAFKA_BROKERS")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	suffix := strings.ReplaceAll(ids.New(), "-", "")
	topic := "evia.test.lag." + suffix
	group := "evia-test-lag-" + suffix
	admin, err := kgo.NewClient(kgo.SeedBrokers(strings.Split(brokers, ",")...), kgo.RequiredAcks(kgo.AllISRAcks()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(admin.Close)
	create := kmsg.NewPtrCreateTopicsRequest()
	create.Topics = []kmsg.CreateTopicsRequestTopic{{Topic: topic, NumPartitions: 1, ReplicationFactor: 1}}
	if response, err := admin.Request(ctx, create); err != nil {
		t.Fatal("Kafka topic create:", err)
	} else {
		for _, item := range response.(*kmsg.CreateTopicsResponse).Topics {
			if item.ErrorCode != 0 {
				t.Fatal("Kafka topic create failed", item.Topic, item.ErrorCode)
			}
		}
	}
	t.Cleanup(func() {
		cleanup, stop := context.WithTimeout(context.Background(), 10*time.Second)
		defer stop()
		request := kmsg.NewPtrDeleteTopicsRequest()
		name := topic
		request.Topics = []kmsg.DeleteTopicsRequestTopic{{Topic: &name}}
		_, _ = admin.Request(cleanup, request)
	})

	produce := func(count int) {
		records := make([]*kgo.Record, 0, count)
		for i := 0; i < count; i++ {
			records = append(records, &kgo.Record{Topic: topic, Value: []byte("payload")})
		}
		if err := admin.ProduceSync(ctx, records...).FirstErr(); err != nil {
			t.Fatal("produce:", err)
		}
	}
	produce(3)

	config := KafkaConfig{
		Brokers:  strings.Split(brokers, ","),
		Topic:    topic,
		Group:    group,
		ClientID: "evia-lag-integration",
	}
	reader, err := NewKafkaReader(config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(reader.Close)

	// The first polls join the group and drain the three seeded records.
	var messages []Message
	deadline := time.Now().Add(30 * time.Second)
	for len(messages) < 3 && time.Now().Before(deadline) {
		pollCtx, stop := context.WithTimeout(ctx, 5*time.Second)
		batch, err := reader.Poll(pollCtx, 128)
		stop()
		if err != nil {
			t.Fatal("poll:", err)
		}
		messages = append(messages, batch...)
	}
	if len(messages) != 3 {
		t.Fatalf("polled %d messages, want 3", len(messages))
	}

	// Before the first commit the group's position is its reset start
	// (AtStart = 0): sampling already reports the full backlog as lag.
	samples, err := reader.SampleLag(ctx)
	if err != nil {
		t.Fatal("sample before commit:", err)
	}
	if len(samples) != 1 {
		t.Fatalf("samples before commit = %d, want 1 partition", len(samples))
	}
	initial := samples[0]
	if initial.CommittedOffset != 0 || initial.LogEndOffset != 3 || initial.Lag != 3 {
		t.Fatalf("backlog sample before commit wrong: %+v", initial)
	}

	reader.AllowRebalance()
	if err := reader.Commit(ctx, messages); err != nil {
		t.Fatal("commit:", err)
	}
	samples, err = reader.SampleLag(ctx)
	if err != nil {
		t.Fatal("sample after commit:", err)
	}
	if len(samples) != 1 {
		t.Fatalf("samples after commit = %d, want 1 partition", len(samples))
	}
	first := samples[0]
	if first.Topic != topic || first.Partition != 0 || first.CommittedOffset != 3 || first.LogEndOffset != 3 || first.Lag != 0 {
		t.Fatalf("zero-lag sample wrong: %+v", first)
	}

	// Two new records without a commit must show exactly two messages of lag.
	produce(2)
	samples, err = reader.SampleLag(ctx)
	if err != nil {
		t.Fatal("sample after new production:", err)
	}
	if len(samples) != 1 {
		t.Fatalf("samples after production = %d, want 1 partition", len(samples))
	}
	second := samples[0]
	if second.CommittedOffset != 3 || second.LogEndOffset != 5 || second.Lag != 2 {
		t.Fatalf("lag sample wrong: %+v", second)
	}
}
