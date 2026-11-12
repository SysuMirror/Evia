//go:build integration

package pipeline

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/howtomakeaname/Evia/backend/internal/archiving"
)

func archiveExecutor(t *testing.T, c ClickHouseConfig) *ArchiveExecutor {
	t.Helper()
	executor, e := NewArchiveExecutor(c, &http.Client{Timeout: 60 * time.Second})
	if e != nil {
		t.Fatal(e)
	}
	return executor
}

func TestClickHouseArchiveExecutorStreamsExactExpiredScope(t *testing.T) {
	c := erasureDatabase(t)
	ctx := context.Background()
	executor := archiveExecutor(t, c)

	const aged = "2025-01-01 00:00:00.000"
	const recent = "2026-11-01 00:00:00.000"
	cutoff := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	const otherProject = "99999999-9999-4999-8999-999999999999"
	insertErasureRows(t, c,
		retentionRow(t, "aged-1", testProject, "development", aged),
		retentionRow(t, "aged-2", testProject, "development", aged),
		retentionRow(t, "recent-1", testProject, "development", recent),
		retentionRow(t, "other-scope-1", otherProject, "development", aged),
	)

	count, e := executor.CountExpired(ctx, testProject, "development", cutoff)
	if e != nil || count != 2 {
		t.Fatalf("count expired wrong: %d err=%v", count, e)
	}

	var sink bytes.Buffer
	window, e := executor.StreamExpired(ctx, &sink, archiving.Scope{
		ProjectID:   testProject,
		Environment: "development",
		Cutoff:      cutoff,
	})
	if e != nil {
		t.Fatal(e)
	}
	if window.Rows != 2 {
		t.Fatalf("streamed row count wrong: %d", window.Rows)
	}
	if window.WindowStart.IsZero() || window.WindowEnd.IsZero() || window.WindowEnd.Before(window.WindowStart) {
		t.Fatalf("coverage window wrong: %+v", window)
	}

	scanner := bufio.NewScanner(&sink)
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	ids := map[string]bool{}
	lines := 0
	for scanner.Scan() {
		var row map[string]any
		if e = json.Unmarshal(scanner.Bytes(), &row); e != nil {
			t.Fatalf("JSONL line invalid: %v", e)
		}
		ids[row["event_id"].(string)] = true
		lines++
	}
	if e = scanner.Err(); e != nil {
		t.Fatal(e)
	}
	if lines != 2 || !ids["aged-1"] || !ids["aged-2"] || ids["recent-1"] || ids["other-scope-1"] {
		t.Fatalf("archived scope leaked rows: %v lines=%d", ids, lines)
	}
}

func TestClickHouseArchiveExecutorEmptyScopeReportsZero(t *testing.T) {
	c := erasureDatabase(t)
	ctx := context.Background()
	executor := archiveExecutor(t, c)
	cutoff := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	count, e := executor.CountExpired(ctx, testProject, "development", cutoff)
	if e != nil || count != 0 {
		t.Fatalf("empty count wrong: %d err=%v", count, e)
	}
}
