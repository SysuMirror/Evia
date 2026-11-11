//go:build integration

package pipeline

import (
	"context"
	"encoding/json"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/howtomakeaname/Evia/backend/internal/erasure"
	"github.com/howtomakeaname/Evia/backend/internal/platform/ids"
)

func erasureDatabase(t *testing.T) ClickHouseConfig {
	t.Helper()
	_, c := integrationConfig(t)
	ctx := context.Background()
	if _, e := clickhouseQuery(ctx, c, "CREATE DATABASE "+c.Database); e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if _, e := clickhouseQuery(cleanup, c, "DROP DATABASE "+c.Database+" SYNC"); e != nil {
			t.Error(e)
		}
	})
	names, e := filepath.Glob("../../../deploy/clickhouse/*.sql")
	if e != nil {
		t.Fatal(e)
	}
	for _, name := range names {
		applyFactMigration(t, ctx, c, name)
	}
	return c
}

func erasureExecutor(t *testing.T, config ClickHouseConfig) *ErasureExecutor {
	t.Helper()
	executor, e := NewErasureExecutor(config, &http.Client{Timeout: 30 * time.Second})
	if e != nil {
		t.Fatal(e)
	}
	return executor
}

func insertErasureRows(t *testing.T, c ClickHouseConfig, rows ...factRow) {
	t.Helper()
	var builder strings.Builder
	builder.WriteString("INSERT INTO event_facts_raw FORMAT JSONEachRow\n")
	for _, row := range rows {
		encoded, e := json.Marshal(row)
		if e != nil {
			t.Fatal(e)
		}
		builder.Write(encoded)
		builder.WriteByte('\n')
	}
	if _, e := clickhouseQuery(context.Background(), c, builder.String()); e != nil {
		t.Fatal(e)
	}
}

func erasureFactRow(eventID string, uid, anonymous *string) factRow {
	row, e := rowFor(testRecord())
	if e != nil {
		panic(e)
	}
	row.EventID = eventID
	row.UID = uid
	row.AnonymousID = anonymous
	row.PropertiesJSON = `{"kept":true}`
	return row
}

func erasureViewCount(t *testing.T, c ClickHouseConfig, uid string) uint64 {
	t.Helper()
	body, e := clickhouseQuery(context.Background(), c, "SELECT count() FROM event_facts WHERE project_id='"+testProject+"' AND environment='development' AND uid='"+uid+"' FORMAT TSV")
	if e != nil {
		t.Fatal(e)
	}
	count, e := strconv.ParseUint(strings.TrimSpace(string(body)), 10, 64)
	if e != nil {
		t.Fatal(e)
	}
	return count
}

func TestClickHouseErasureExecutorDeletesIdentityAndVerifies(t *testing.T) {
	c := erasureDatabase(t)
	ctx := context.Background()
	executor := erasureExecutor(t, c)
	erasedUID := "user-erase-me"
	survivorUID := "user-survives"
	anonymous := ids.New()

	insertErasureRows(t, c,
		erasureFactRow("erase-1", &erasedUID, nil),
		erasureFactRow("erase-2", &erasedUID, nil),
		erasureFactRow("keep-1", &survivorUID, nil),
		erasureFactRow("anon-1", nil, &anonymous),
	)

	remaining, e := executor.RemainingIdentityRows(ctx, testProject, "development", erasure.SubjectUID, erasedUID)
	if e != nil || remaining != 2 {
		t.Fatal(remaining, e)
	}
	if e = executor.DeleteIdentity(ctx, testProject, "development", erasure.SubjectUID, erasedUID); e != nil {
		t.Fatal(e)
	}
	remaining, e = executor.RemainingIdentityRows(ctx, testProject, "development", erasure.SubjectUID, erasedUID)
	if e != nil || remaining != 0 {
		t.Fatal("erased identity rows remain", remaining, e)
	}
	// The deduplicated view must immediately hide the erased rows as well.
	if count := erasureViewCount(t, c, erasedUID); count != 0 {
		t.Fatalf("view still exposes %d erased rows", count)
	}
	survivors, e := executor.RemainingIdentityRows(ctx, testProject, "development", erasure.SubjectUID, survivorUID)
	if e != nil || survivors != 1 {
		t.Fatal(survivors, e)
	}
	// Anonymous identities use the nullable anonymous_id column.
	if e = executor.DeleteIdentity(ctx, testProject, "development", erasure.SubjectAnonymousID, anonymous); e != nil {
		t.Fatal(e)
	}
	anonLeft, e := executor.RemainingIdentityRows(ctx, testProject, "development", erasure.SubjectAnonymousID, anonymous)
	if e != nil || anonLeft != 0 {
		t.Fatal(anonLeft, e)
	}
	if e = executor.DeleteIdentity(ctx, testProject, "development", "session_id", "whatever"); e == nil {
		t.Fatal("unsupported subject kind was accepted")
	}
}
