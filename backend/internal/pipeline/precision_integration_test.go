//go:build integration

package pipeline

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/howtomakeaname/Evia/backend/internal/ingestion"
	"github.com/howtomakeaname/Evia/backend/internal/platform/ids"
)

func applyFactMigration(t *testing.T, ctx context.Context, c ClickHouseConfig, name string) {
	t.Helper()
	data, e := os.ReadFile(name)
	if e != nil {
		t.Fatal(e)
	}
	var sql strings.Builder
	for _, line := range strings.Split(string(data), "\n") {
		if !strings.HasPrefix(strings.TrimSpace(line), "--") {
			sql.WriteString(line)
			sql.WriteByte('\n')
		}
	}
	for _, statement := range strings.Split(sql.String(), ";") {
		if strings.TrimSpace(statement) != "" {
			if _, e = clickhouseQuery(ctx, c, statement); e != nil {
				t.Fatal(name, e)
			}
		}
	}
}
func precisionDatabase(t *testing.T) ClickHouseConfig {
	t.Helper()
	endpoint := os.Getenv("EVIA_TEST_CLICKHOUSE_URL")
	username := os.Getenv("EVIA_TEST_CLICKHOUSE_USERNAME")
	password := os.Getenv("EVIA_TEST_CLICKHOUSE_PASSWORD")
	if endpoint == "" || username == "" || password == "" {
		t.Fatal("explicit EVIA_TEST_CLICKHOUSE_URL/USERNAME/PASSWORD are required")
	}
	c := ClickHouseConfig{URL: endpoint, Username: username, Password: password, Database: "evia_precision_" + strings.ReplaceAll(ids.New(), "-", ""), Client: &http.Client{Timeout: 15 * time.Second}}
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
	applyFactMigration(t, ctx, c, "../../../deploy/clickhouse/001_event_facts.sql")
	return c
}
func insertLegacyFact(t *testing.T, c ClickHouseConfig, row factRow) {
	t.Helper()
	encoded, e := json.Marshal(row)
	if e != nil {
		t.Fatal(e)
	}
	var values map[string]any
	if e = json.Unmarshal(encoded, &values); e != nil {
		t.Fatal(e)
	}
	delete(values, "occurred_at_precise")
	delete(values, "event_time_precise")
	values["occurred_at"] = row.OccurredAt[:23]
	values["event_time"] = row.EventTime[:23]
	encoded, e = json.Marshal(values)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = clickhouseQuery(context.Background(), c, "INSERT INTO event_facts_raw FORMAT JSONEachRow\n"+string(encoded)); e != nil {
		t.Fatal(e)
	}
}
func viewNanoseconds(t *testing.T, c ClickHouseConfig, id string) (string, string) {
	t.Helper()
	if !uuidPattern.MatchString(id) {
		t.Fatal("invalid generated test event id")
	}
	data, e := clickhouseQuery(context.Background(), c, "SELECT toString(toUnixTimestamp64Nano(occurred_at)) AS occurred,toString(toUnixTimestamp64Nano(event_time)) AS event FROM event_facts WHERE event_id='"+id+"' FORMAT JSONEachRow")
	if e != nil {
		t.Fatal(e)
	}
	var result struct {
		Occurred string `json:"occurred"`
		Event    string `json:"event"`
	}
	if e = json.Unmarshal(data, &result); e != nil {
		t.Fatal(e, string(data))
	}
	return result.Occurred, result.Event
}

func TestClickHousePrecisionPopulatedUpgradeAndReplay(t *testing.T) {
	c := precisionDatabase(t)
	ctx := context.Background()
	legacy := testRecord()
	legacyRow, e := rowFor(legacy)
	if e != nil {
		t.Fatal(e)
	}
	insertLegacyFact(t, c, legacyRow)
	sink, e := NewClickHouse(c)
	if e != nil {
		t.Fatal(e)
	}
	unmigrated := legacy
	unmigrated.EventID = "33333333-3333-4333-8333-333333333334"
	if e = sink.Write(ctx, []ingestion.Record{unmigrated}); !errors.Is(e, ErrSinkUnavailable) {
		t.Fatal("new writer silently accepted missing precision migration", e)
	}
	if count := integrationCount(t, ctx, c, "event_facts_raw"); count != 1 {
		t.Fatal("unmigrated write changed legacy data", count)
	}
	for i := 0; i < 2; i++ {
		applyFactMigration(t, ctx, c, "../../../deploy/clickhouse/003_event_precision.sql")
	}
	// Version 006 is an equally mandatory writer precondition: with the
	// precision columns present but the property layout columns missing, the
	// writer must still fail closed rather than drop the derived fields.
	if e = sink.Write(ctx, []ingestion.Record{unmigrated}); !errors.Is(e, ErrSinkUnavailable) {
		t.Fatal("new writer silently accepted missing property layout migration", e)
	}
	applyFactMigration(t, ctx, c, "../../../deploy/clickhouse/006_property_layout.sql")
	for _, name := range []string{"occurred_at", "event_time"} {
		data, e := clickhouseQuery(ctx, c, "SELECT type FROM system.columns WHERE database='"+c.Database+"' AND table='event_facts' AND name='"+name+"' FORMAT JSONEachRow")
		var column struct {
			Type string `json:"type"`
		}
		decodeError := json.Unmarshal(data, &column)
		if e != nil || decodeError != nil || column.Type != "DateTime64(9, 'UTC')" {
			t.Fatal("view precision type mismatch", name, string(data), e)
		}
	}
	wantLegacy := fmt.Sprint(legacy.OccurredAt.Truncate(time.Millisecond).UnixNano())
	occurred, event := viewNanoseconds(t, c, legacy.EventID)
	if occurred != wantLegacy || event != wantLegacy {
		t.Fatal("legacy data changed during upgrade", occurred, event, wantLegacy)
	}
	// Same canonical hash/receipt but newly explicit precision must not make the
	// old-versus-replay argMin winner nondeterministic or invent historical nanos.
	if e = sink.Write(ctx, []ingestion.Record{legacy}); e != nil {
		t.Fatal(e)
	}
	occurred, event = viewNanoseconds(t, c, legacy.EventID)
	if occurred != wantLegacy || event != wantLegacy {
		t.Fatal("legacy replay changed stored precision", occurred, event)
	}
	first := testRecord()
	first.EventID = "33333333-3333-4333-8333-333333333334"
	first.OccurredAt = first.OccurredAt.Truncate(time.Millisecond).Add(time.Nanosecond)
	second := first
	second.EventID = "33333333-3333-4333-8333-333333333335"
	second.OccurredAt = first.OccurredAt.Add(time.Nanosecond)
	if e = sink.Write(ctx, []ingestion.Record{second, first}); e != nil {
		t.Fatal(e)
	}
	retry := first
	retry.ReceivedAt = retry.ReceivedAt.Add(time.Second)
	conflict := retry
	conflict.OccurredAt = conflict.OccurredAt.Add(time.Nanosecond)
	if e = sink.Write(ctx, []ingestion.Record{retry, conflict}); e != nil {
		t.Fatal(e)
	}
	for _, record := range []ingestion.Record{first, second} {
		want := fmt.Sprint(record.OccurredAt.UnixNano())
		occurred, event = viewNanoseconds(t, c, record.EventID)
		if occurred != want || event != want {
			t.Fatal("nanosecond value or first winner lost through view", record.EventID, occurred, event, want)
		}
	}
	// Rolling back to an old writer remains safe after expansion: its actual
	// millisecond values populate the new columns through compatible defaults.
	rollback := testRecord()
	rollback.EventID = "33333333-3333-4333-8333-333333333336"
	rollbackRow, e := rowFor(rollback)
	if e != nil {
		t.Fatal(e)
	}
	insertLegacyFact(t, c, rollbackRow)
	occurred, event = viewNanoseconds(t, c, rollback.EventID)
	if occurred != wantLegacy || event != wantLegacy {
		t.Fatal("old writer rollback incompatible", occurred, event)
	}
	applyFactMigration(t, ctx, c, "../../../deploy/clickhouse/003_event_precision.sql")
	if count := integrationCount(t, ctx, c, "event_facts"); count != 4 {
		t.Fatal("precision upgrade/replay inflated facts", count)
	}
}

func TestClickHousePrecisionRejectsUnsupportedLegacyDateBeforeDDL(t *testing.T) {
	c := precisionDatabase(t)
	row, e := rowFor(testRecord())
	if e != nil {
		t.Fatal(e)
	}
	row.OccurredAt = "2299-01-01 00:00:00.000000000"
	row.EventTime = row.OccurredAt
	insertLegacyFact(t, c, row)
	data, e := os.ReadFile("../../../deploy/clickhouse/003_event_precision.sql")
	if e != nil {
		t.Fatal(e)
	}
	var sql strings.Builder
	for _, line := range strings.Split(string(data), "\n") {
		if !strings.HasPrefix(strings.TrimSpace(line), "--") {
			sql.WriteString(line)
			sql.WriteByte('\n')
		}
	}
	first := strings.Split(sql.String(), ";")[0]
	if _, e = clickhouseQuery(context.Background(), c, first); e == nil {
		t.Fatal("unsupported historical timestamp silently overflowed")
	}
	data, e = clickhouseQuery(context.Background(), c, "SELECT count() AS n FROM system.columns WHERE database='"+c.Database+"' AND table='event_facts_raw' AND name IN ('occurred_at_precise','event_time_precise') FORMAT JSONEachRow")
	if e != nil {
		t.Fatal(e)
	}
	var columns struct {
		N json.Number `json:"n"`
	}
	if e = json.Unmarshal(data, &columns); e != nil || columns.N.String() != "0" {
		t.Fatal("failed preflight applied partial DDL", columns, e)
	}
}
