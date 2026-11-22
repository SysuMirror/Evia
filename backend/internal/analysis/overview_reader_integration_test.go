//go:build integration

package analysis

import (
	"context"
	"testing"
	"time"

	"github.com/howtomakeaname/Evia/backend/internal/overview"
)

func TestClickHouseProjectOverviewAggregate(t *testing.T) {
	client := clickHouseDatabase(t)
	rows := []map[string]any{
		fact("a", "alpha", "u1", "anon-1", "source-one", 0, nil),
		fact("b", "alpha", "", "anon-2", "source-one", 1, nil),
		fact("c", "beta", "u2", "", "source-one", 2, nil),
		fact("d", "gamma", "", "anon-3", "source-one", 3, nil),
	}
	rows[0]["session_id"] = "session-1"
	rows[1]["session_id"] = nil
	rows[2]["session_id"] = "session-2"
	rows[3]["session_id"] = "session-2"
	rows[2]["event_time"] = "2026-11-07 11:00:00.000"
	rows[2]["occurred_at"] = "2026-11-07 11:00:00.000"
	rows[2]["received_at"] = "2026-11-07 11:00:01.000000000"
	rows[3]["event_time"] = "2026-11-08 10:00:00.000"
	rows[3]["occurred_at"] = "2026-11-08 10:00:00.000"
	rows[3]["received_at"] = "2026-11-08 10:00:05.000000123"
	duplicate := fact("a", "wrong", "wrong", "wrong", "source-one", 0, nil)
	duplicate["received_at"] = "2026-11-07 10:30:00.000000000"
	duplicate["canonical_hash"] = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	rows = append(rows, duplicate)
	foreignProject := fact("foreign-p", "foreign", "foreign", "foreign", "source-one", 0, nil)
	foreignProject["project_id"] = "project-two"
	foreignEnvironment := fact("foreign-e", "foreign", "foreign", "foreign", "source-one", 0, nil)
	foreignEnvironment["environment"] = "production"
	rows = append(rows, foreignProject, foreignEnvironment)
	seedRows(t, client, rows)

	request := overview.Request{ProjectID: "project-one", Environment: "development", From: time.Date(2026, 11, 7, 10, 0, 0, 0, time.UTC), To: time.Date(2026, 11, 9, 10, 0, 0, 0, time.UTC), Interval: overview.IntervalHour}
	result, err := client.ReadOverview(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if result.TotalEvents != 4 || result.UniqueSessions != 2 || result.IdentifiedUsers != 2 || result.AnonymousIDs != 3 {
		t.Fatalf("aggregate mismatch: %+v", result)
	}
	if result.DataWatermark == nil || result.DataWatermark.Format(time.RFC3339Nano) != "2026-11-08T10:00:05.000000123Z" {
		t.Fatalf("watermark mismatch: %v", result.DataWatermark)
	}
	if len(result.Trend) != 3 || result.Trend[0].Events != 2 || result.Trend[0].Sessions != 1 || result.Trend[1].Bucket.Hour() != 11 || result.Trend[2].Bucket.Day() != 8 {
		t.Fatalf("trend mismatch: %+v", result.Trend)
	}
	if len(result.TopEvents) != 3 || result.TopEvents[0].EventKey != "alpha" || result.TopEvents[0].Count != 2 || result.TopEvents[1].EventKey != "beta" || result.TopEvents[2].EventKey != "gamma" {
		t.Fatalf("top events mismatch: %+v", result.TopEvents)
	}

	empty := request
	empty.Environment = "staging"
	result, err = client.ReadOverview(context.Background(), empty)
	if err != nil {
		t.Fatal(err)
	}
	if result.TotalEvents != 0 || result.DataWatermark != nil || len(result.Trend) != 0 || len(result.TopEvents) != 0 {
		t.Fatalf("empty result mismatch: %+v", result)
	}
}
