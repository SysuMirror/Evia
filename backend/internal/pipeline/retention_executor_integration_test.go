//go:build integration

package pipeline

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"
)

func retentionExecutor(t *testing.T, c ClickHouseConfig) *RetentionExecutor {
	t.Helper()
	executor, e := NewRetentionExecutor(c, &http.Client{Timeout: 30 * time.Second})
	if e != nil {
		t.Fatal(e)
	}
	return executor
}

func retentionRow(t *testing.T, eventID, project, environment, eventTime string) factRow {
	t.Helper()
	row, e := rowFor(testRecord())
	if e != nil {
		t.Fatal(e)
	}
	row.EventID = eventID
	row.ProjectID = project
	row.Environment = environment
	row.OccurredAt = eventTime
	row.EventTime = eventTime
	row.OccurredAtPrecise = eventTime
	row.EventTimePrecise = eventTime
	return row
}

func factCount(t *testing.T, c ClickHouseConfig, where string) uint64 {
	t.Helper()
	body, e := clickhouseQuery(context.Background(), c, "SELECT count() FROM event_facts_raw WHERE "+where+" FORMAT TSV")
	if e != nil {
		t.Fatal(e)
	}
	count, e := strconv.ParseUint(strings.TrimSpace(string(body)), 10, 64)
	if e != nil {
		t.Fatal(e)
	}
	return count
}

func TestClickHouseRetentionExecutorAgedScopeOnly(t *testing.T) {
	c := erasureDatabase(t)
	ctx := context.Background()
	executor := retentionExecutor(t, c)

	const aged = "2025-01-01 00:00:00.000"
	const recent = "2026-11-01 00:00:00.000"
	cutoff := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	const otherProject = "99999999-9999-4999-8999-999999999999"

	insertErasureRows(t, c,
		retentionRow(t, "aged-1", testProject, "development", aged),
		retentionRow(t, "aged-2", testProject, "development", aged),
		retentionRow(t, "recent-1", testProject, "development", recent),
		// Aged rows outside the targeted scope must survive.
		retentionRow(t, "staging-aged", testProject, "staging", aged),
		retentionRow(t, "project-aged", otherProject, "development", aged),
	)

	candidates, e := executor.CountExpired(ctx, testProject, "development", cutoff)
	if e != nil || candidates != 2 {
		t.Fatalf("candidates=%d err=%v, want 2", candidates, e)
	}

	deleted, e := executor.DeleteExpired(ctx, testProject, "development", cutoff)
	if e != nil || deleted != 2 {
		t.Fatalf("deleted=%d err=%v, want 2", deleted, e)
	}

	// The targeted aged rows are gone; the recent row in the same scope remains.
	if left := factCount(t, c,
		"project_id='"+testProject+"' AND environment='development' AND event_time < toDateTime64('"+clickhouseDateTime(cutoff)+"', 3)"); left != 0 {
		t.Fatalf("expired rows remain: %d", left)
	}
	if recentCount := factCount(t, c,
		"project_id='"+testProject+"' AND environment='development' AND event_id='recent-1'"); recentCount != 1 {
		t.Fatalf("recent row was deleted: %d", recentCount)
	}
	// Other environments and projects are untouched by the scoped delete.
	if staging := factCount(t, c, "event_id='staging-aged'"); staging != 1 {
		t.Fatalf("staging scope affected: %d", staging)
	}
	if other := factCount(t, c, "event_id='project-aged'"); other != 1 {
		t.Fatalf("other project affected: %d", other)
	}
	// Re-running on an already-clean scope is a no-op, not an error.
	again, e := executor.DeleteExpired(ctx, testProject, "development", cutoff)
	if e != nil || again != 0 {
		t.Fatalf("idempotent re-sweep: deleted=%d err=%v", again, e)
	}
}

func TestClickHouseRetentionDryRunDoesNotMutate(t *testing.T) {
	c := erasureDatabase(t)
	ctx := context.Background()
	executor := retentionExecutor(t, c)
	const aged = "2025-03-01 00:00:00.000"
	cutoff := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)

	insertErasureRows(t, c, retentionRow(t, "aged-only", testProject, "production", aged))

	candidates, e := executor.CountExpired(ctx, testProject, "production", cutoff)
	if e != nil || candidates != 1 {
		t.Fatalf("candidates=%d err=%v", candidates, e)
	}
	// Counting alone must not change any rows.
	if total := factCount(t, c, "event_id='aged-only'"); total != 1 {
		t.Fatalf("dry-run count mutated facts: %d", total)
	}
}
