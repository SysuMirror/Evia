//go:build integration

package pipeline

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/howtomakeaname/Evia/backend/internal/ingestion"
)

// TestSteadyMixedReadWrite runs sustained ingestion and concurrent bounded
// analytics against the real Kafka/ClickHouse stack at the same time. It is
// the storage-layer complement of the read-only 514/728 matrix: instead of
// querying a frozen dataset, readers race a live writer. The invariants are
// exact-once landing, zero read failures under write pressure, monotonically
// non-decreasing count observations (insert-only workload), and bounded read
// latency. Query-side resource use is pulled from system.query_log afterwards.
func TestSteadyMixedReadWrite(t *testing.T) {
	if os.Getenv("EVIA_TEST_MIXED_DRILL") != "1" {
		t.Skip("mixed workload drill requires EVIA_TEST_MIXED_DRILL=1")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Second)
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
			BatchSize:    20,
			RetryInitial: 20 * time.Millisecond,
			RetryMax:     200 * time.Millisecond,
		}).Run(runCtx)
	}()

	const (
		totalEvents = 600
		batchSize   = 30
		readerCount = 4
		maxQueryMS  = 10000
		queryMarker = "/* evia_mixed_drill */"
	)
	queries := []string{
		queryMarker + " SELECT count() FROM event_facts SETTINGS max_execution_time=10 FORMAT JSONEachRow",
		queryMarker + " SELECT event_key, count() FROM event_facts GROUP BY event_key " +
			"ORDER BY event_key LIMIT 20 SETTINGS max_execution_time=10 FORMAT JSONEachRow",
		queryMarker + " SELECT count() FROM event_facts WHERE uid='synthetic-user' " +
			"SETTINGS max_execution_time=10 FORMAT JSONEachRow",
	}

	type sample struct {
		shape     int
		latencyMS int64
		count     int64
	}
	var (
		mu          sync.Mutex
		samples     []sample
		readErrors  []string
		countTrails = make([][]int64, readerCount)
	)
	readCtx, stopReaders := context.WithCancel(ctx)
	var readers sync.WaitGroup
	for r := 0; r < readerCount; r++ {
		readers.Add(1)
		go func(readerID int) {
			defer readers.Done()
			shape := 0
			for {
				select {
				case <-readCtx.Done():
					return
				default:
				}
				started := time.Now()
				body, qErr := clickhouseQuery(ctx, chConfig, queries[shape%len(queries)])
				latencyMS := time.Since(started).Milliseconds()
				if qErr != nil {
					mu.Lock()
					readErrors = append(readErrors, qErr.Error())
					mu.Unlock()
				} else {
					countVal := extractCount(t, body)
					mu.Lock()
					samples = append(samples, sample{shape: shape % len(queries), latencyMS: latencyMS, count: countVal})
					if shape%len(queries) == 0 {
						countTrails[readerID] = append(countTrails[readerID], countVal)
					}
					mu.Unlock()
				}
				shape++
			}
		}(r)
	}

	// Steady paced writer: 20 batches of 30 over about five seconds.
	publishStarted := time.Now()
	for begin := 1; begin <= totalEvents; begin += batchSize {
		end := begin + batchSize - 1
		if end > totalEvents {
			end = totalEvents
		}
		if err = producer.Publish(ctx, mixedRecords(begin, end)); err != nil {
			t.Fatalf("publish %d-%d: %v", begin, end, err)
		}
		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-time.After(250 * time.Millisecond):
		}
	}
	publishMS := time.Since(publishStarted).Milliseconds()

	// Wait for the worker to land every event while readers keep racing.
	drainDeadline := time.Now().Add(60 * time.Second)
	var finalCount int
	for time.Now().Before(drainDeadline) {
		finalCount = integrationCount(t, ctx, chConfig, "event_facts")
		if finalCount == totalEvents {
			break
		}
		time.Sleep(300 * time.Millisecond)
	}
	drainMS := time.Since(publishStarted).Milliseconds()
	stopReaders()
	readers.Wait()

	stopWorker()
	if wErr := <-workerDone; !errors.Is(wErr, context.Canceled) {
		t.Fatalf("worker exited unexpectedly: %v", wErr)
	}

	if finalCount != totalEvents {
		t.Fatalf("event_facts landed %d rows, want %d", finalCount, totalEvents)
	}
	rawCount := integrationCount(t, ctx, chConfig, "event_facts_raw")
	if rawCount < totalEvents {
		t.Fatalf("raw table %d rows fewer than produced %d", rawCount, totalEvents)
	}
	if len(readErrors) != 0 {
		t.Fatalf("read failures under mixed load: %d, first: %s", len(readErrors), readErrors[0])
	}
	if len(samples) == 0 {
		t.Fatal("no read samples collected")
	}
	for readerID, trail := range countTrails {
		for i := 1; i < len(trail); i++ {
			if trail[i] < trail[i-1] {
				t.Fatalf("reader %d observed a decreasing count: %d -> %d", readerID, trail[i-1], trail[i])
			}
		}
	}

	latencies := make([]int64, len(samples))
	byShape := map[int][]int64{}
	for i, s := range samples {
		latencies[i] = s.latencyMS
		byShape[s.shape] = append(byShape[s.shape], s.latencyMS)
	}
	p95 := percentile(latencies, 95)
	p50 := percentile(latencies, 50)
	if p95 > maxQueryMS {
		t.Fatalf("mixed-load read p95 %dms exceeded bound %dms", p95, maxQueryMS)
	}

	peakMemory := queryLogPeakMemory(t, ctx, chConfig, queryMarker)
	summary, err := json.Marshal(map[string]any{
		"type":              "mixed_workload_summary",
		"produced":          totalEvents,
		"view_count":        finalCount,
		"raw_count":         rawCount,
		"read_samples":      len(samples),
		"read_errors":       len(readErrors),
		"reader_goroutines": readerCount,
		"p50_read_ms":       p50,
		"p95_read_ms":       p95,
		"per_shape_p95_ms":  mapShapePercentiles(byShape),
		"peak_query_memory": peakMemory,
		"publish_ms":        publishMS,
		"drain_ms":          drainMS,
		"write_rate_per_s":  float64(totalEvents) / (float64(drainMS) / 1000.0),
		"monotonic_readers": readerCount,
	})
	if err != nil {
		t.Fatal(err)
	}
	fmt.Println(string(summary))
}

func mixedRecords(begin, end int) []ingestion.Record {
	records := make([]ingestion.Record, 0, end-begin+1)
	for seq := begin; seq <= end; seq++ {
		record := backlogRecord(seq)
		records = append(records, record)
	}
	return records
}

func extractCount(t *testing.T, body []byte) int64 {
	t.Helper()
	firstLine := bytes.TrimSpace(bytes.SplitN(bytes.TrimSpace(body), []byte("\n"), 2)[0])
	if len(firstLine) == 0 {
		return 0
	}
	decoder := json.NewDecoder(bytes.NewReader(firstLine))
	decoder.UseNumber()
	var row map[string]any
	if err := decoder.Decode(&row); err != nil {
		t.Fatalf("decode query result %q: %v", string(body), err)
	}
	for _, value := range row {
		number, ok := value.(json.Number)
		if !ok {
			continue // skip string columns such as event_key
		}
		if n, err := number.Int64(); err == nil {
			return n
		}
	}
	t.Fatalf("no numeric column in %q", string(body))
	return 0
}

func percentile(values []int64, pct int) int64 {
	if len(values) == 0 {
		return 0
	}
	sorted := append([]int64(nil), values...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	index := (len(sorted) * pct) / 100
	if index >= len(sorted) {
		index = len(sorted) - 1
	}
	return sorted[index]
}

func mapShapePercentiles(byShape map[int][]int64) map[string]int64 {
	out := make(map[string]int64, len(byShape))
	for shape, values := range byShape {
		out[fmt.Sprintf("shape_%d_p95_ms", shape)] = percentile(values, 95)
	}
	return out
}

func queryLogPeakMemory(t *testing.T, ctx context.Context, c ClickHouseConfig, marker string) int64 {
	t.Helper()
	if _, err := clickhouseQuery(ctx, c, "SYSTEM FLUSH LOGS"); err != nil {
		t.Fatalf("flush logs: %v", err)
	}
	sql := "SELECT coalesce(max(memory_usage),0) FROM system.query_log WHERE type='QueryFinish' " +
		"AND position(query, '" + marker + "') > 0"
	body, err := clickhouseQuery(ctx, c, sql+" FORMAT JSONEachRow")
	if err != nil {
		t.Fatalf("query_log peak memory: %v", err)
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	var row map[string]json.Number
	if err := decoder.Decode(&row); err != nil {
		t.Fatalf("decode query_log row: %v", err)
	}
	for _, value := range row {
		n, err := value.Int64()
		if err == nil {
			return n
		}
	}
	return 0
}
