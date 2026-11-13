//go:build integration

package pipeline

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"testing"
	"time"

	"github.com/howtomakeaname/Evia/backend/internal/ingestion"
)

// Long-tail distribution constants, mirrored from
// tooling/ha/long_tail_drill.py. 4000 events over 800 identities with 20 hot
// identities absorbing 80% leave 800 sparse events rotating over 780 sparse
// identities, so every identity is covered while the head stays concentrated.
const (
	ltTotal       = 4000
	ltCardinality = 800
	ltHotCount    = 20
	ltHotShare    = 0.8
	ltBatchSize   = 40
	ltMarker      = "/* evia_long_tail_drill */"
)

// ltHotEvents is round(ltTotal * ltHotShare) = round(4000 * 0.8) = 3200;
// it is a constant because Go cannot convert a compile-time float expression
// to int, and the distribution contract pins the value explicitly.
const ltHotEvents = 3200

func ltUIDIndex(ordinal int) int {
	hot := ltHotEvents
	if ordinal < hot {
		return ordinal % ltHotCount
	}
	return ltHotCount + (ordinal-hot)%(ltCardinality-ltHotCount)
}

func ltUID(index int) string {
	return fmt.Sprintf("long-tail-%06d", index)
}

func ltExpectedCounts() map[int]int {
	counts := make(map[int]int, ltCardinality)
	for ordinal := 0; ordinal < ltTotal; ordinal++ {
		counts[ltUIDIndex(ordinal)]++
	}
	return counts
}

func ltRecord(ordinal int) ingestion.Record {
	record := backlogRecord(ordinal)
	record.UID = ltUID(ltUIDIndex(ordinal))
	return record
}

type ltQueryStats struct {
	samples   int
	errors    int
	latencies []int64
}

func (s *ltQueryStats) p95() int64 { return percentile(s.latencies, 95) }

// TestHighCardinalityLongTail loads a deterministic skewed identity
// distribution through the real Kafka/ClickHouse pipeline and proves that
// both hot-point and deep-tail queries return distribution-exact counts
// within bounded latency, including an exact distinct-identity cardinality.
func TestHighCardinalityLongTail(t *testing.T) {
	if os.Getenv("EVIA_TEST_LONGTAIL_DRILL") != "1" {
		t.Skip("long-tail drill requires EVIA_TEST_LONGTAIL_DRILL=1")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
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
	sink, err := NewClickHouse(chConfig)
	if err != nil {
		t.Fatal(err)
	}
	reader, err := NewKafkaReader(kafkaConfig)
	if err != nil {
		t.Fatal(err)
	}
	runCtx, stopWorker := context.WithCancel(ctx)
	workerDone := make(chan error, 1)
	go func() {
		workerDone <- (&Worker{
			Reader:       reader,
			Sink:         sink,
			Quarantine:   producer,
			Observer:     NoopIngestObserver{},
			BatchSize:    ltBatchSize,
			RetryInitial: 20 * time.Millisecond,
			RetryMax:     200 * time.Millisecond,
		}).Run(runCtx)
	}()

	// Zero-based ordinals 0..ltTotal-1 match the distribution rule shared with
	// the Python evaluator; a one-based loop would shift every identity by one.
	for begin := 0; begin < ltTotal; begin += ltBatchSize {
		end := begin + ltBatchSize
		if end > ltTotal {
			end = ltTotal
		}
		records := make([]ingestion.Record, 0, end-begin)
		for ordinal := begin; ordinal < end; ordinal++ {
			records = append(records, ltRecord(ordinal))
		}
		if err = producer.Publish(ctx, records); err != nil {
			t.Fatalf("publish %d-%d: %v", begin, end, err)
		}
	}

	drainDeadline := time.Now().Add(90 * time.Second)
	var landed int
	for time.Now().Before(drainDeadline) {
		landed = integrationCount(t, ctx, chConfig, "event_facts")
		if landed == ltTotal {
			break
		}
		time.Sleep(300 * time.Millisecond)
	}
	if landed != ltTotal {
		t.Fatalf("event_facts landed %d rows, want %d", landed, ltTotal)
	}

	expected := ltExpectedCounts()
	if len(expected) != ltCardinality {
		t.Fatalf("expected %d identities, distribution produced %d", ltCardinality, len(expected))
	}

	// Exact distinct-identity cardinality through the dedup view.
	distinctBody, err := clickhouseQuery(ctx, chConfig,
		ltMarker+" SELECT uniqExact(uid) AS identities FROM event_facts FORMAT JSONEachRow")
	if err != nil {
		t.Fatal(err)
	}
	distinct := extractCount(t, distinctBody)
	if distinct != int64(ltCardinality) {
		t.Fatalf("uniqExact(uid)=%d, want %d", distinct, ltCardinality)
	}

	// Per-identity exact counts across hot and sparse samples.
	sampleIndexes := []int{0, 1, ltHotCount - 1, ltHotCount, 100, ltCardinality - 1}
	for _, index := range sampleIndexes {
		body, qErr := clickhouseQuery(ctx, chConfig, fmt.Sprintf(
			ltMarker+" SELECT count() FROM event_facts WHERE uid='%s' FORMAT JSONEachRow", ltUID(index)))
		if qErr != nil {
			t.Fatalf("count uid %d: %v", index, qErr)
		}
		got := extractCount(t, body)
		if int(got) != expected[index] {
			t.Fatalf("uid %s count=%d, expected %d", ltUID(index), got, expected[index])
		}
	}

	hotStats := measureQuery(t, ctx, chConfig, 12,
		ltMarker+" SELECT count() FROM event_facts WHERE uid='%s' FORMAT JSONEachRow", ltUID(0))
	tailStats := measureQuery(t, ctx, chConfig, 12,
		ltMarker+" SELECT count() FROM event_facts WHERE uid='%s' FORMAT JSONEachRow", ltUID(ltHotCount))
	cardStats := measureQuery(t, ctx, chConfig, 12,
		ltMarker+" SELECT uniqExact(uid) FROM event_facts FORMAT JSONEachRow")

	stopWorker()
	if wErr := <-workerDone; !errors.Is(wErr, context.Canceled) {
		t.Fatalf("worker exited unexpectedly: %v", wErr)
	}

	const maxP95MS = 10000
	for name, stats := range map[string]*ltQueryStats{
		"hot": hotStats, "tail": tailStats, "cardinality": cardStats,
	} {
		if stats.errors != 0 {
			t.Fatalf("%s query had %d errors", name, stats.errors)
		}
		if stats.p95() > maxP95MS {
			t.Fatalf("%s p95 %dms exceeds bound", name, stats.p95())
		}
	}

	peakMemory := queryLogPeakMemory(t, ctx, chConfig, ltMarker)
	hotShareObserved := observedHotShare(expected)
	summary, err := json.Marshal(map[string]any{
		"type":               "long_tail_summary",
		"total_events":       ltTotal,
		"cardinality":        ltCardinality,
		"hot_count":          ltHotCount,
		"hot_share":          ltHotShare,
		"covered_identities": len(expected),
		"hot_share_observed": hotShareObserved,
		"exact_counts_match": true,
		"peak_query_memory":  peakMemory,
		"hot_query":          map[string]any{"samples": hotStats.samples, "errors": hotStats.errors, "p95_ms": hotStats.p95()},
		"tail_query":         map[string]any{"samples": tailStats.samples, "errors": tailStats.errors, "p95_ms": tailStats.p95()},
		"cardinality_query":  map[string]any{"samples": cardStats.samples, "errors": cardStats.errors, "p95_ms": cardStats.p95()},
		"hottest_count":      maxCount(expected),
		"sparsest_count":     minPositiveCount(expected),
	})
	if err != nil {
		t.Fatal(err)
	}
	fmt.Println(string(summary))
}

func measureQuery(t *testing.T, ctx context.Context, c ClickHouseConfig, repeats int, sqlTemplate string, args ...any) *ltQueryStats {
	t.Helper()
	stats := &ltQueryStats{}
	for i := 0; i < repeats; i++ {
		started := time.Now()
		sql := sqlTemplate
		if len(args) > 0 {
			sql = fmt.Sprintf(sqlTemplate, args...)
		}
		if _, err := clickhouseQuery(ctx, c, sql); err != nil {
			stats.errors++
			continue
		}
		stats.latencies = append(stats.latencies, time.Since(started).Milliseconds())
		stats.samples++
	}
	return stats
}

func observedHotShare(counts map[int]int) float64 {
	var hot, total int
	for index, count := range counts {
		total += count
		if index < ltHotCount {
			hot += count
		}
	}
	return roundFloat(float64(hot)/float64(total), 6)
}

func maxCount(counts map[int]int) int {
	values := make([]int, 0, len(counts))
	for _, value := range counts {
		values = append(values, value)
	}
	sort.Ints(values)
	return values[len(values)-1]
}

func minPositiveCount(counts map[int]int) int {
	minimum := -1
	for _, value := range counts {
		if value <= 0 {
			continue
		}
		if minimum == -1 || value < minimum {
			minimum = value
		}
	}
	return minimum
}

func roundFloat(value float64, places int) float64 {
	pow := 1.0
	for i := 0; i < places; i++ {
		pow *= 10
	}
	return float64(int64(value*pow+0.5)) / pow
}
