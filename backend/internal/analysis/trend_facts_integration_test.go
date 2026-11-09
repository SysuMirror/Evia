//go:build integration

package analysis

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/howtomakeaname/Evia/backend/internal/platform/ids"
)

func trendPlanRows(t *testing.T, client *ClickHouse, r Request, table string) []map[string]any {
	t.Helper()
	plan, e := Compile(client.database, table, Scope{ProjectID: "project-one", Environment: r.Environment}, r)
	if e != nil {
		t.Fatal(e)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	response, e := client.request(ctx, plan.SQL+" FORMAT JSONEachRow", "evia-trend-test-"+ids.New(), plan.Parameters, true)
	if e != nil {
		t.Fatal(e)
	}
	defer response.Body.Close()
	raw, e := io.ReadAll(io.LimitReader(response.Body, 4*1024*1024+1))
	if e != nil || response.StatusCode != http.StatusOK || len(raw) > 4*1024*1024 {
		t.Fatalf("trend fixture failed: status=%d error=%v body=%s", response.StatusCode, e, raw)
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	rows := []map[string]any{}
	for {
		var row map[string]any
		if e = decoder.Decode(&row); e == io.EOF {
			return rows
		} else if e != nil {
			t.Fatal(e)
		}
		rows = append(rows, row)
	}
}

func cloneTrendFact(row map[string]any) map[string]any {
	out := make(map[string]any, len(row))
	for key, value := range row {
		out[key] = value
	}
	return out
}

func TestClickHouseNarrowTrendMatchesFullWinnerView(t *testing.T) {
	client := clickHouseDatabase(t)
	// A separate name deliberately exercises the original view-backed planner
	// as an oracle; production always uses the fixed event_facts source name.
	chExec(t, client, "CREATE VIEW event_facts_reference AS SELECT * FROM event_facts")
	makeFact := func(id, uid string, second int, score any) map[string]any {
		return fact(id, "opened", uid, "anon-"+id, "source-one", second,
			map[string]any{"score": score, "enabled": false, "tags": []string{"z"}})
	}
	winner := makeFact("conflict", "winner", 0, 0)
	winner["properties_json"] = `{"score":0,"enabled":false,"tags":[]}`
	loser := makeFact("conflict", "loser", 30, 999)
	loser["event_key"], loser["schema_version"] = "closed", 2
	loser["properties_json"] = `{"score":999,"enabled":true,"tags":["loser"]}`
	loser["canonical_hash"] = strings.Repeat("b", 64)
	otherSource := makeFact("conflict", "other-source", 1, 5)
	otherSource["source_id"] = "source-two"
	otherSource["properties_json"] = `{"score":5,"enabled":false,"tags":["a","b"]}`
	otherProject := cloneTrendFact(winner)
	otherProject["project_id"] = "project-two"
	otherEnvironment := cloneTrendFact(winner)
	otherEnvironment["environment"] = "production"
	nullWinner := makeFact("null-winner", "", 2, nil)
	nullLoser := makeFact("null-winner", "late-nonnull", 31, 999)
	missing := makeFact("missing", "missing", 3, nil)
	missing["properties_json"] = `{}`
	hashWinner := makeFact("hash-order", "hash-first", 9, 7)
	hashLoser := makeFact("hash-order", "hash-later", 8, 999)
	hashWinner["received_at"], hashLoser["received_at"] = "2026-11-07 10:00:10.000000000", "2026-11-07 10:00:10.000000000"
	// Cross a hexadecimal digit boundary to exercise packed-byte hash ordering.
	hashWinner["canonical_hash"] = "09" + strings.Repeat("f", 62)
	hashLoser["canonical_hash"] = "0a" + strings.Repeat("0", 62)
	legacy := makeFact("precision-order", "legacy", 12, 11)
	legacy["occurred_at_precise"], legacy["event_time_precise"] = nil, nil
	preciseReplay := cloneTrendFact(legacy)
	preciseReplay["uid"] = "invented-precision"
	preciseReplay["occurred_at_precise"], preciseReplay["event_time_precise"] = "2026-11-07 10:00:12.000000001", "2026-11-07 10:00:12.000000001"
	occurredWinner := makeFact("occurred-order", "occurred-first", 14, 13)
	occurredLoser := cloneTrendFact(occurredWinner)
	occurredLoser["uid"] = "occurred-later"
	occurredWinner["occurred_at_precise"], occurredLoser["occurred_at_precise"] = "2026-11-07 10:00:14.000000001", "2026-11-07 10:00:14.000000002"
	occurredWinner["event_time_precise"], occurredLoser["event_time_precise"] = "2026-11-07 10:00:14.000000009", "2026-11-07 10:00:14.000000001"
	eventWinner := makeFact("event-time-order", "event-first", 16, 17)
	eventLoser := cloneTrendFact(eventWinner)
	eventLoser["uid"] = "event-later"
	eventWinner["event_time_precise"], eventLoser["event_time_precise"] = "2026-11-07 10:00:16.000000001", "2026-11-07 10:00:16.000000002"
	rows := []map[string]any{loser, otherProject, otherEnvironment, nullLoser, hashLoser, preciseReplay, occurredLoser, eventLoser, winner, otherSource, nullWinner, missing, hashWinner, legacy, occurredWinner, eventWinner}
	seedRows(t, client, rows)
	propertyField := func(key, kind string) *Field {
		return &Field{Kind: "property", EventKey: "opened", SchemaVersion: 1, Key: key, ResolvedType: kind}
	}
	for _, tc := range []struct {
		name  string
		edit  func(*Request)
		count string
	}{
		{"full cohort", func(*Request) {}, "8"},
		{"losing event key", func(r *Request) { r.EventKeys = []string{"closed"} }, "0"},
		{"losing version", func(r *Request) { r.EventVersions = []EventVersion{{EventKey: "closed", SchemaVersion: 2}} }, "0"},
		{"losing time", func(r *Request) {
			r.TimeRange.From = time.Date(2026, 11, 7, 10, 0, 30, 0, time.UTC)
			r.TimeRange.To = r.TimeRange.From.Add(2 * time.Second)
		}, "0"},
		{"losing UID", func(r *Request) {
			r.Filter = &Filter{Op: "eq", Field: &Field{Kind: "system", Key: "uid", ResolvedType: "string"}, Value: "loser"}
		}, "0"},
		{"nullable winner", func(r *Request) { uid := "late-nonnull"; r.IdentityFilter = &IdentityFilter{UID: &uid} }, "0"},
		{"false", func(r *Request) {
			r.Filter = &Filter{Op: "eq", Field: propertyField("enabled", "boolean"), Value: false}
		}, "7"},
		{"zero", func(r *Request) {
			r.Filter = &Filter{Op: "eq", Field: propertyField("score", "number"), Value: json.Number("0")}
		}, "1"},
		{"empty array", func(r *Request) {
			r.Filter = &Filter{Op: "eq", Field: propertyField("tags", "array:string"), Value: []any{}}
		}, "1"},
		{"missing", func(r *Request) { r.Filter = &Filter{Op: "is_missing", Field: propertyField("score", "number")} }, "1"},
		{"hash ordering", func(r *Request) {
			r.Filter = &Filter{Op: "eq", Field: &Field{Kind: "system", Key: "uid", ResolvedType: "string"}, Value: "hash-later"}
		}, "0"},
		{"legacy precision ordering", func(r *Request) {
			r.TimeRange.From = time.Date(2026, 11, 7, 10, 0, 12, 1, time.UTC)
			r.TimeRange.To = r.TimeRange.From.Add(time.Nanosecond)
		}, "0"},
		{"occurred before event ordering", func(r *Request) {
			r.Filter = &Filter{Op: "eq", Field: &Field{Kind: "system", Key: "uid", ResolvedType: "string"}, Value: "occurred-later"}
		}, "0"},
		{"event precision ordering", func(r *Request) {
			r.Filter = &Filter{Op: "eq", Field: &Field{Kind: "system", Key: "uid", ResolvedType: "string"}, Value: "event-later"}
		}, "0"},
		{"typed metrics", func(r *Request) {
			r.Subject = "uid"
			r.Metrics = []Metric{{Op: "count"}, {Op: "unique"}, {Op: "sum", Field: propertyField("score", "number")}, {Op: "avg", Field: propertyField("score", "number")}, {Op: "p95", Field: propertyField("score", "number")}}
		}, "8"},
		{"anonymous subject", func(r *Request) { r.Subject = "anonymous"; r.Metrics = []Metric{{Op: "count"}, {Op: "unique"}} }, "8"},
		{"missing null zero grouping", func(r *Request) { r.GroupBy = []Field{*propertyField("score", "number")} }, ""},
		{"UID and bucket grouping", func(r *Request) {
			r.GroupBy = []Field{{Kind: "system", Key: "uid", ResolvedType: "string"}}
			r.Interval = "minute"
		}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := request(t, baseRequest)
			tc.edit(&r)
			narrow := trendPlanRows(t, client, r, "event_facts")
			full := trendPlanRows(t, client, r, "event_facts_reference")
			if !reflect.DeepEqual(narrow, full) {
				t.Fatalf("narrow winner disagrees with full view: narrow=%v full=%v", narrow, full)
			}
			if tc.count != "" && (len(narrow) != 1 || narrow[0]["metric_0"] != json.Number(tc.count)) {
				t.Fatal("winning-cohort count changed", narrow, tc.count)
			}
		})
	}
}
