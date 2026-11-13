//go:build integration

package pipeline

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"sort"
	"strconv"
	"testing"
	"time"

	"github.com/howtomakeaname/Evia/backend/internal/ingestion"
)

// Zipf load constants, mirrored from tooling/ha/zipf_distribution_drill.py.
// 4000 events over 500 identities following P(rank) ~ 1/rank**s, where s
// defaults to 1.0 and is overridable through EVIA_TEST_ZIPF_EXPONENT,
// apportioned with the Hamilton largest-remainder method so integer counts
// sum exactly to the total and every identity is covered.
const (
	zfTotal             = 4000
	zfCardinality       = 500
	defaultZipfExponent = 1.0
	zfBatchSize         = 40
	zfMarker            = "/* evia_zipf_drill */"
)

// parseZipfExponent resolves the production-fitted Zipf exponent from
// EVIA_TEST_ZIPF_EXPONENT so operators can rerun the drill with an exponent
// estimated from real events instead of rebuilding the test. The closed range
// matches the Python evaluator and rejects zero or negative power laws.
func parseZipfExponent() (float64, error) {
	raw := os.Getenv("EVIA_TEST_ZIPF_EXPONENT")
	if raw == "" {
		return defaultZipfExponent, nil
	}
	exponent, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid EVIA_TEST_ZIPF_EXPONENT %q: %w", raw, err)
	}
	if exponent <= 0 || exponent > 3 {
		return 0, fmt.Errorf("EVIA_TEST_ZIPF_EXPONENT must be within (0, 3], got %v", exponent)
	}
	return exponent, nil
}

func TestParseZipfExponent(t *testing.T) {
	cases := []struct {
		name    string
		raw     string
		want    float64
		wantErr bool
	}{
		{name: "empty uses default", raw: "", want: defaultZipfExponent},
		{name: "mild exponent", raw: "0.5", want: 0.5},
		{name: "unit exponent", raw: "1.0", want: 1.0},
		{name: "upper bound inclusive", raw: "3", want: 3.0},
		{name: "zero rejected", raw: "0", wantErr: true},
		{name: "negative rejected", raw: "-1.2", wantErr: true},
		{name: "above range rejected", raw: "3.01", wantErr: true},
		{name: "non numeric rejected", raw: "abc", wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("EVIA_TEST_ZIPF_EXPONENT", tc.raw)
			got, err := parseZipfExponent()
			if tc.wantErr {
				if err == nil {
					t.Fatalf("parseZipfExponent(%q) = %v, want error", tc.raw, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseZipfExponent(%q) unexpected error: %v", tc.raw, err)
			}
			if got != tc.want {
				t.Fatalf("parseZipfExponent(%q) = %v, want %v", tc.raw, got, tc.want)
			}
		})
	}
}

func zipfWeights(exponent float64) []float64 {
	weights := make([]float64, zfCardinality)
	for i := range weights {
		weights[i] = 1.0 / math.Pow(float64(i+1), exponent)
	}
	return weights
}

// hamiltonZipfApportion rounds fractional quotas to integer seats summing
// exactly to zfTotal; equal remainders resolve to the lowest rank via a
// stable sort, matching the Python evaluator.
func hamiltonZipfApportion(exponent float64) []int {
	weights := zipfWeights(exponent)
	sum := 0.0
	for _, weight := range weights {
		sum += weight
	}
	quotas := make([]float64, zfCardinality)
	seats := make([]int, zfCardinality)
	leftover := zfTotal
	for i, weight := range weights {
		quotas[i] = float64(zfTotal) * weight / sum
		seats[i] = int(quotas[i])
		leftover -= seats[i]
	}
	order := make([]int, zfCardinality)
	for i := range order {
		order[i] = i
	}
	sort.SliceStable(order, func(a, b int) bool {
		return quotas[order[a]]-float64(seats[order[a]]) >
			quotas[order[b]]-float64(seats[order[b]])
	})
	for k := 0; k < leftover; k++ {
		seats[order[k]]++
	}
	return seats
}

func zipfUID(rankZeroBased int) string {
	return fmt.Sprintf("zipf-%06d", rankZeroBased+1)
}

// zipfLogLogSlope fits ln(count) = a + b*ln(rank) and returns b; a Zipf law
// with exponent s predicts b = -s.
func zipfLogLogSlope(counts []int) float64 {
	n := float64(len(counts))
	var sumX, sumY, sumXY, sumXX float64
	for i, count := range counts {
		x := math.Log(float64(i + 1))
		y := math.Log(float64(count))
		sumX += x
		sumY += y
		sumXY += x * y
		sumXX += x * x
	}
	denominator := sumXX - sumX*sumX/n
	if denominator == 0 {
		return 0
	}
	return (sumXY - sumX*sumY/n) / denominator
}

// TestZipfDistribution loads an inverse power-law identity distribution
// through the real Kafka/ClickHouse pipeline and proves both exact
// per-identity apportionment and the rank-frequency power-law shape.
func TestZipfDistribution(t *testing.T) {
	if os.Getenv("EVIA_TEST_ZIPF_DRILL") != "1" {
		t.Skip("Zipf drill requires EVIA_TEST_ZIPF_DRILL=1")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()

	exponent, err := parseZipfExponent()
	if err != nil {
		t.Fatal(err)
	}
	expected := hamiltonZipfApportion(exponent)
	if len(expected) != zfCardinality || sumInts(expected) != zfTotal {
		t.Fatalf("apportionment invariant broken: seats=%d total=%d", len(expected), sumInts(expected))
	}
	for _, seat := range expected {
		if seat < 1 {
			t.Fatal("Zipf apportionment left an identity uncovered")
		}
	}

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
			BatchSize:    zfBatchSize,
			RetryInitial: 20 * time.Millisecond,
			RetryMax:     200 * time.Millisecond,
		}).Run(runCtx)
	}()

	// Round-robin over ranks so hot and cold identities interleave on arrival.
	remaining := append([]int(nil), expected...)
	ordinal := 0
	for sumInts(remaining) > 0 {
		records := make([]ingestion.Record, 0, zfBatchSize)
		for rank := 0; rank < zfCardinality && len(records) < zfBatchSize; rank++ {
			if remaining[rank] == 0 {
				continue
			}
			record := backlogRecord(ordinal)
			record.UID = zipfUID(rank)
			records = append(records, record)
			remaining[rank]--
			ordinal++
		}
		if err = producer.Publish(ctx, records); err != nil {
			t.Fatalf("publish at ordinal %d: %v", ordinal, err)
		}
	}

	drainDeadline := time.Now().Add(90 * time.Second)
	var landed int
	for time.Now().Before(drainDeadline) {
		landed = integrationCount(t, ctx, chConfig, "event_facts")
		if landed == zfTotal {
			break
		}
		time.Sleep(300 * time.Millisecond)
	}
	if landed != zfTotal {
		t.Fatalf("event_facts landed %d rows, want %d", landed, zfTotal)
	}

	distinctBody, err := clickhouseQuery(ctx, chConfig,
		zfMarker+" SELECT uniqExact(uid) AS identities FROM event_facts FORMAT JSONEachRow")
	if err != nil {
		t.Fatal(err)
	}
	if distinct := extractCount(t, distinctBody); distinct != int64(zfCardinality) {
		t.Fatalf("uniqExact(uid)=%d, want %d", distinct, zfCardinality)
	}

	// Exact per-identity counts across hot, middle and cold ranks.
	sampleRanks := []int{0, 1, 2, 10, 100, zfCardinality - 1}
	for _, rank := range sampleRanks {
		body, qErr := clickhouseQuery(ctx, chConfig, fmt.Sprintf(
			zfMarker+" SELECT count() FROM event_facts WHERE uid='%s' FORMAT JSONEachRow", zipfUID(rank)))
		if qErr != nil {
			t.Fatalf("count rank %d: %v", rank, qErr)
		}
		if got := extractCount(t, body); int(got) != expected[rank] {
			t.Fatalf("rank %d count=%d, expected %d", rank+1, got, expected[rank])
		}
	}

	// Top-5 GROUP BY must return the five hottest ranks in apportionment order.
	topBody, err := clickhouseQuery(ctx, chConfig,
		zfMarker+" SELECT uid, count() AS c FROM event_facts GROUP BY uid ORDER BY c DESC, uid LIMIT 5 FORMAT JSONEachRow")
	if err != nil {
		t.Fatal(err)
	}
	var topRows []map[string]any
	for _, line := range splitNonEmpty(topBody) {
		var row map[string]any
		if jErr := json.Unmarshal(line, &row); jErr != nil {
			t.Fatal(jErr)
		}
		topRows = append(topRows, row)
	}
	if len(topRows) != 5 {
		t.Fatalf("top-5 returned %d rows", len(topRows))
	}
	for i, row := range topRows {
		wantUID := zipfUID(i)
		if row["uid"] != wantUID {
			t.Fatalf("top rank %d got uid %v, want %s", i, row["uid"], wantUID)
		}
	}

	observedSlope := zipfLogLogSlope(expected)
	hotStats := measureQuery(t, ctx, chConfig, 12,
		zfMarker+" SELECT count() FROM event_facts WHERE uid='%s' FORMAT JSONEachRow", zipfUID(0))
	tailStats := measureQuery(t, ctx, chConfig, 12,
		zfMarker+" SELECT count() FROM event_facts WHERE uid='%s' FORMAT JSONEachRow", zipfUID(zfCardinality-1))
	cardStats := measureQuery(t, ctx, chConfig, 12,
		zfMarker+" SELECT uniqExact(uid) FROM event_facts FORMAT JSONEachRow")

	stopWorker()
	if wErr := <-workerDone; !errors.Is(wErr, context.Canceled) {
		t.Fatalf("worker exited unexpectedly: %v", wErr)
	}

	for name, stats := range map[string]*ltQueryStats{
		"hot": hotStats, "tail": tailStats, "cardinality": cardStats,
	} {
		if stats.errors != 0 || stats.p95() > 10000 {
			t.Fatalf("%s query outside bounds: %+v", name, stats)
		}
	}

	peakMemory := queryLogPeakMemory(t, ctx, chConfig, zfMarker)
	summary, err := json.Marshal(map[string]any{
		"type":               "zipf_distribution_summary",
		"total_events":       zfTotal,
		"cardinality":        zfCardinality,
		"exponent":           exponent,
		"covered_identities": zfCardinality,
		"observed_slope":     observedSlope,
		"exact_counts_match": true,
		"hottest_count":      expected[0],
		"coldest_count":      expected[zfCardinality-1],
		"peak_query_memory":  peakMemory,
		"hot_query":          map[string]any{"samples": hotStats.samples, "errors": hotStats.errors, "p95_ms": hotStats.p95()},
		"tail_query":         map[string]any{"samples": tailStats.samples, "errors": tailStats.errors, "p95_ms": tailStats.p95()},
		"cardinality_query":  map[string]any{"samples": cardStats.samples, "errors": cardStats.errors, "p95_ms": cardStats.p95()},
	})
	if err != nil {
		t.Fatal(err)
	}
	fmt.Println(string(summary))
}

func sumInts(values []int) int {
	total := 0
	for _, value := range values {
		total += value
	}
	return total
}

func splitNonEmpty(body []byte) [][]byte {
	var lines [][]byte
	for _, line := range bytes.Split(bytes.TrimSpace(body), []byte("\n")) {
		if len(line) > 0 {
			lines = append(lines, line)
		}
	}
	return lines
}
