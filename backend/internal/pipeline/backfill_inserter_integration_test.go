//go:build integration

package pipeline

import (
	"bytes"
	"context"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/howtomakeaname/Evia/backend/internal/archiving"
)

func viewCount(t *testing.T, c ClickHouseConfig, where string) uint64 {
	t.Helper()
	body, e := clickhouseQuery(context.Background(), c, "SELECT count() FROM event_facts WHERE "+where+" FORMAT TSV")
	if e != nil {
		t.Fatal(e)
	}
	count, e := strconv.ParseUint(strings.TrimSpace(string(body)), 10, 64)
	if e != nil {
		t.Fatal(e)
	}
	return count
}

func TestClickHouseBackfillInserterRoundTrip(t *testing.T) {
	c := erasureDatabase(t)
	ctx := context.Background()
	archiveExec := archiveExecutor(t, c)
	retentionExec := retentionExecutor(t, c)
	inserter, e := NewBackfillInserter(c, &http.Client{Timeout: 60 * time.Second})
	if e != nil {
		t.Fatal(e)
	}

	const aged = "2025-01-01 00:00:00.000"
	cutoff := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	insertErasureRows(t, c,
		retentionRow(t, "bf-aged-1", testProject, "development", aged),
		retentionRow(t, "bf-aged-2", testProject, "development", aged),
	)
	if count := factCount(t, c, "project_id='"+testProject+"' AND environment='development'"); count != 2 {
		t.Fatalf("seed count wrong: %d", count)
	}

	// Archive the exact expired scope into a JSONL artifact on disk.
	dir := t.TempDir()
	artifactPath := filepath.Join(dir, "archive.jsonl")
	var sink bytes.Buffer
	window, e := archiveExec.StreamExpired(ctx, &sink, archiving.Scope{
		ProjectID: testProject, Environment: "development", Cutoff: cutoff,
	})
	if e != nil || window.Rows != 2 {
		t.Fatalf("archive stream wrong: %+v err=%v", window, e)
	}
	if e = os.WriteFile(artifactPath, sink.Bytes(), 0o600); e != nil {
		t.Fatal(e)
	}

	// Delete the expired facts, then backfill them from the artifact.
	deleted, e := retentionExec.DeleteExpired(ctx, testProject, "development", cutoff)
	if e != nil || deleted != 2 {
		t.Fatalf("delete wrong: %d err=%v", deleted, e)
	}
	if count := factCount(t, c, "project_id='"+testProject+"' AND environment='development'"); count != 0 {
		t.Fatalf("post-delete count wrong: %d", count)
	}
	handle, e := os.Open(artifactPath)
	if e != nil {
		t.Fatal(e)
	}
	defer handle.Close()
	inserted, e := inserter.InsertFacts(ctx, testProject, "development", handle)
	if e != nil || inserted != 2 {
		t.Fatalf("backfill wrong: inserted=%d err=%v", inserted, e)
	}
	if count := viewCount(t, c, "project_id='"+testProject+"' AND environment='development'"); count != 2 {
		t.Fatalf("post-backfill view count wrong: %d", count)
	}

	// Re-running the same backfill stays query-safe: the raw table holds
	// duplicate rows but the event_facts view collapses them by event identity.
	handle2, e := os.Open(artifactPath)
	if e != nil {
		t.Fatal(e)
	}
	defer handle2.Close()
	if _, e = inserter.InsertFacts(ctx, testProject, "development", handle2); e != nil {
		t.Fatalf("idempotent reinsert failed: %v", e)
	}
	viewCount := viewCount(t, c, "project_id='"+testProject+"' AND environment='development'")
	if viewCount != 2 {
		t.Fatalf("duplicate backfill changed deduped view count: %d", viewCount)
	}
}

func TestClickHouseBackfillInserterRejectsEmptyStream(t *testing.T) {
	c := erasureDatabase(t)
	inserter, e := NewBackfillInserter(c, &http.Client{Timeout: 60 * time.Second})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = inserter.InsertFacts(context.Background(), testProject, "development", bytes.NewReader(nil)); e == nil {
		t.Fatal("empty stream must be rejected")
	}
}
