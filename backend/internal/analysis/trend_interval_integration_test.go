//go:build integration

package analysis

import (
	"encoding/json"
	"testing"
	"time"
)

// TestClickHouseWeekIntervalBuckets verifies the week interval against the real
// migrated view: Monday-start alignment in UTC and timezone-local boundaries.
func TestClickHouseWeekIntervalBuckets(t *testing.T) {
	client := clickHouseDatabase(t)
	stamp := func(y time.Time) string { return y.UTC().Format("2006-01-02 15:04:05.000") }
	mk := func(id string, when time.Time) map[string]any {
		row := fact(id, "weekbucket", "u-"+id, "", "source-one", 0, map[string]any{})
		row["event_time"] = stamp(when)
		row["occurred_at"] = stamp(when)
		return row
	}
	rows := []map[string]any{
		// 2026-11-08 is a Sunday; 2026-11-09 the following Monday.
		mk("sun", time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)),
		mk("edge", time.Date(2026, 11, 8, 17, 0, 0, 0, time.UTC)), // 2026-11-09 01:00 in Asia/Shanghai
		mk("mon", time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC)),
		mk("tue", time.Date(2026, 11, 10, 9, 0, 0, 0, time.UTC)),
	}
	seedRows(t, client, rows)

	query := func(tz string) map[string]float64 {
		r := request(t, baseRequest)
		r.EventVersions = []EventVersion{{EventKey: "weekbucket", SchemaVersion: 1}}
		r.Metrics = []Metric{{Op: "count", Accuracy: "exact"}}
		r.Interval = "week"
		r.Timezone = tz
		r.TimeRange.From = time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
		r.TimeRange.To = time.Date(2026, 11, 15, 0, 0, 0, 0, time.UTC)
		result := execute(t, client, r)
		out := map[string]float64{}
		for _, row := range result.Rows {
			bucket, ok := row["bucket"].(string)
			if !ok {
				t.Fatalf("bucket label missing: %v", row)
			}
			number, ok := row["metric_0"].(json.Number)
			if !ok {
				t.Fatalf("metric missing: %v", row)
			}
			value, e := number.Float64()
			if e != nil {
				t.Fatal(e)
			}
			out[bucket] = value
		}
		return out
	}

	// UTC: Sunday events stay in the week starting Monday 2026-11-02; the Monday
	// and Tuesday events group into the week starting 2026-11-09.
	utcBuckets := query("UTC")
	wantUTC := map[string]float64{
		"2026-11-02T00:00:00Z": 2, // sun + edge, both Sundays in UTC
		"2026-11-09T00:00:00Z": 2, // mon + tue
	}
	if len(utcBuckets) != len(wantUTC) {
		t.Fatalf("UTC week buckets = %v want %v", utcBuckets, wantUTC)
	}
	for bucket, want := range wantUTC {
		if utcBuckets[bucket] != want {
			t.Fatalf("UTC bucket %s = %v want %v (all: %v)", bucket, utcBuckets[bucket], want, utcBuckets)
		}
	}

	// Asia/Shanghai: edge is already local Monday 2026-11-09, so it groups with
	// mon/tue in the week starting local Monday 2026-11-09; only sun stays in the
	// prior week. Bucket labels follow the existing interval convention (see
	// clickhouse.go): the timezone-local calendar boundary is rendered at its
	// UTC midnight, so the label is the Monday date rather than a shifted instant.
	shBuckets := query("Asia/Shanghai")
	wantSH := map[string]float64{
		"2026-11-02T00:00:00Z": 1, // sun only
		"2026-11-09T00:00:00Z": 3, // edge + mon + tue
	}
	if len(shBuckets) != len(wantSH) {
		t.Fatalf("Shanghai week buckets = %v want %v", shBuckets, wantSH)
	}
	for bucket, want := range wantSH {
		if shBuckets[bucket] != want {
			t.Fatalf("Shanghai bucket %s = %v want %v (all: %v)", bucket, shBuckets[bucket], want, shBuckets)
		}
	}
}
