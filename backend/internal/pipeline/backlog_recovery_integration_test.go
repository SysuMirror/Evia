//go:build integration

package pipeline

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/howtomakeaname/Evia/backend/internal/ingestion"
)

// gateSink wraps the real ClickHouse sink and simulates a sink outage
// (the in-process equivalent of ErrSinkUnavailable while ClickHouse is down).
// While failing it never touches the inner sink, so no partial write can land;
// the production worker keeps retrying the held batch without committing
// offsets, which is exactly how a Kafka backlog forms.
type gateSink struct {
	inner    Sink
	failing  atomic.Bool
	attempts atomic.Int64
}

func (s *gateSink) Write(ctx context.Context, records []ingestion.Record) error {
	s.attempts.Add(1)
	if s.failing.Load() {
		return ErrSinkUnavailable
	}
	return s.inner.Write(ctx, records)
}

func backlogRecord(seq int) ingestion.Record {
	record := testRecord()
	record.EventID = fmt.Sprintf("00000000-0000-4000-8000-%012x", seq)
	record.ReceivedAt = record.ReceivedAt.Add(time.Duration(seq) * time.Millisecond)
	return record
}

// readerLag sums the worker group lag from the production LagReader. The
// kgo client is safe for concurrent use, and while the worker goroutine is
// blocked retrying the held batch it is not polling, so sampling from the
// test goroutine reads the same in-memory committed position without joining
// a second group or stealing the partition assignment.
func readerLag(ctx context.Context, reader *KafkaReader) (int64, error) {
	samples, err := reader.SampleLag(ctx)
	if err != nil {
		return 0, err
	}
	if len(samples) == 0 {
		return 0, errors.New("reader reported no partition lag samples yet")
	}
	var total int64
	for _, sample := range samples {
		total += sample.Lag
	}
	return total, nil
}

// TestBacklogRecoveryUnderSinkOutage proves the Kafka backlog and drain
// contract under a sustained sink outage: offsets stop advancing while the
// sink is unavailable, consumer lag grows, then the same worker drains the
// backlog after recovery with every produced event landing exactly once.
func TestBacklogRecoveryUnderSinkOutage(t *testing.T) {
	if os.Getenv("EVIA_TEST_BACKLOG_DRILL") != "1" {
		t.Skip("backlog recovery drill requires EVIA_TEST_BACKLOG_DRILL=1")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	kafkaConfig, chConfig := integrationConfig(t)
	preparePipelineDependencies(t, ctx, kafkaConfig, chConfig)

	producer, err := NewKafkaProducer(kafkaConfig)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		closeCtx, stop := context.WithTimeout(context.Background(), 10*time.Second)
		defer stop()
		_ = producer.Close(closeCtx)
	}()
	if err = producer.Ping(ctx); err != nil {
		t.Fatal(err)
	}
	realSink, err := NewClickHouse(chConfig)
	if err != nil {
		t.Fatal(err)
	}
	gate := &gateSink{inner: realSink}

	reader, err := NewKafkaReader(kafkaConfig)
	if err != nil {
		t.Fatal(err)
	}
	runCtx, stopWorker := context.WithCancel(ctx)
	workerDone := make(chan error, 1)
	go func() {
		workerDone <- (&Worker{
			Reader:       reader,
			Sink:         gate,
			Quarantine:   producer,
			Observer:     NoopIngestObserver{},
			BatchSize:    10,
			RetryInitial: 20 * time.Millisecond,
			RetryMax:     200 * time.Millisecond,
		}).Run(runCtx)
	}()

	const baseline = 20
	const backlog = 180
	total := baseline + backlog

	waitCount := func(target int) error {
		deadline := time.Now().Add(60 * time.Second)
		for time.Now().Before(deadline) {
			if integrationCount(t, ctx, chConfig, "event_facts") >= target {
				return nil
			}
			time.Sleep(300 * time.Millisecond)
		}
		return fmt.Errorf("event_facts never reached %d rows", target)
	}

	// Healthy baseline: the pipeline must drain normally before the outage.
	if err = producer.Publish(ctx, backlogRecords(1, baseline)); err != nil {
		t.Fatal(err)
	}
	if err = waitCount(baseline); err != nil {
		t.Fatal(err)
	}

	// Inject the outage and keep producing: offsets freeze while lag grows.
	gate.failing.Store(true)
	outageStarted := time.Now()
	for begin := baseline + 1; begin <= total; begin += 30 {
		end := begin + 29
		if end > total {
			end = total
		}
		if err = producer.Publish(ctx, backlogRecords(begin, end)); err != nil {
			t.Fatal(err)
		}
		time.Sleep(200 * time.Millisecond)
	}

	var peakLag int64
	var lastProbeErr error
	lagSamples := make([]int64, 0, 16)
	sampleDeadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(sampleDeadline) {
		lag, probeErr := readerLag(ctx, reader)
		if probeErr == nil {
			lagSamples = append(lagSamples, lag)
			if lag > peakLag {
				peakLag = lag
			}
		} else {
			lastProbeErr = probeErr
		}
		time.Sleep(500 * time.Millisecond)
	}
	t.Logf("lag probe: samples=%d lastErr=%v", len(lagSamples), lastProbeErr)
	outageMs := time.Since(outageStarted).Milliseconds()
	if len(lagSamples) == 0 {
		t.Fatal("no lag samples collected during the outage")
	}
	if peakLag < backlog/2 {
		t.Fatalf("backlog did not accumulate: peak lag %d, backlog %d", peakLag, backlog)
	}
	// Nothing may land while the gate is closed.
	if frozen := integrationCount(t, ctx, chConfig, "event_facts"); frozen != baseline {
		t.Fatalf("rows landed during the sink outage: %d, want %d", frozen, baseline)
	}

	// Recover the sink: the held batch retries succeed and the worker drains.
	drainStarted := time.Now()
	gate.failing.Store(false)
	if err = waitCount(total); err != nil {
		t.Fatal(err)
	}
	drainDeadline := time.Now().Add(30 * time.Second)
	var finalLag int64 = -1
	for time.Now().Before(drainDeadline) {
		lag, probeErr := readerLag(ctx, reader)
		if probeErr == nil {
			finalLag = lag
			if lag == 0 {
				break
			}
		}
		time.Sleep(300 * time.Millisecond)
	}
	drainMs := time.Since(drainStarted).Milliseconds()
	if finalLag != 0 {
		t.Fatalf("consumer lag did not return to zero: %d", finalLag)
	}

	stopWorker()
	if err = <-workerDone; !errors.Is(err, context.Canceled) {
		t.Fatalf("worker exited unexpectedly: %v", err)
	}

	// Exact-once: the dedup view holds every produced event once; the raw
	// append-only table cannot have fewer rows than the view.
	viewCount := integrationCount(t, ctx, chConfig, "event_facts")
	rawCount := integrationCount(t, ctx, chConfig, "event_facts_raw")
	if viewCount != total {
		t.Fatalf("dedup view count %d, want %d", viewCount, total)
	}
	if rawCount < total {
		t.Fatalf("raw table count %d fewer than produced %d", rawCount, total)
	}

	drainRate := 0.0
	if drainMs > 0 {
		drainRate = float64(backlog) / (float64(drainMs) / 1000.0)
	}
	summary, err := json.Marshal(map[string]any{
		"type":             "backlog_recovery_summary",
		"produced":         total,
		"baseline":         baseline,
		"backlog":          backlog,
		"peak_lag":         peakLag,
		"final_lag":        finalLag,
		"lag_samples":      lagSamples,
		"outage_ms":        outageMs,
		"drain_ms":         drainMs,
		"drain_rate_per_s": drainRate,
		"view_count":       viewCount,
		"raw_count":        rawCount,
		"sink_attempts":    gate.attempts.Load(),
	})
	if err != nil {
		t.Fatal(err)
	}
	fmt.Println(string(summary))
}

func backlogRecords(begin, end int) []ingestion.Record {
	records := make([]ingestion.Record, 0, end-begin+1)
	for seq := begin; seq <= end; seq++ {
		records = append(records, backlogRecord(seq))
	}
	return records
}

var _ Sink = (*gateSink)(nil)
