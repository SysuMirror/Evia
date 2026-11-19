//go:build integration

package analysis

import (
	"encoding/json"
	"fmt"
	"reflect"
	"testing"

	"github.com/howtomakeaname/Evia/backend/internal/propertylayout"
)

// layoutFactRow returns a copy of a legacy fixture row enriched with the
// version-1 token map derived from its own authoritative document.
func layoutFactRow(t *testing.T, row map[string]any) map[string]any {
	t.Helper()
	out := cloneTrendFact(row)
	derived, err := propertylayout.DeriveMap([]byte(row["properties_json"].(string)))
	if err != nil {
		t.Fatal(err)
	}
	tokens := make(map[string]string, len(derived))
	for encoded, token := range derived {
		tokens[encoded] = string(token)
	}
	out["property_layout_version"] = 1
	out["property_tokens"] = tokens
	return out
}

func propertyEquivalenceRows(t *testing.T, layout bool) []map[string]any {
	t.Helper()
	documents := []string{
		`{"value":1.5}`, `{"value":-2.25}`, `{"value":1e2}`, `{"value":1.00}`,
		`{"value":0}`, `{"value":-3}`, `{"value":2.5e-1}`, `{"value":null}`,
		`{}`, `{"value":"100"}`, `{"value":true}`, `{"value":[]}`, `{"value":{}}`,
		`{"title":"Home","value":7}`, `{"title":"Settings","value":8}`,
		`{"enabled":false,"value":9}`, `{"enabled":true,"value":10}`,
		`{"tags":["a","b"],"value":11}`, `{"tags":["b","c"],"value":12}`,
	}
	rows := make([]map[string]any, 0, len(documents))
	for index, document := range documents {
		row := fact("equivalence-"+string(rune('a'+index)), "opened", "user", "device", "source-one", index, nil)
		row["properties_json"] = document
		if layout {
			row = layoutFactRow(t, row)
		}
		rows = append(rows, row)
	}
	return rows
}

func TestClickHousePropertyLayoutReaderMatchesLegacyDocument(t *testing.T) {
	legacy := clickHouseDatabase(t)
	layout := clickHouseDatabase(t)
	seedRows(t, legacy, propertyEquivalenceRows(t, false))
	seedRows(t, layout, propertyEquivalenceRows(t, true))

	valueField := Field{Kind: "property", EventKey: "opened", SchemaVersion: 1, Key: "value", ResolvedType: "number"}
	titleField := Field{Kind: "property", EventKey: "opened", SchemaVersion: 1, Key: "title", ResolvedType: "string"}
	enabledField := Field{Kind: "property", EventKey: "opened", SchemaVersion: 1, Key: "enabled", ResolvedType: "boolean"}
	tagsField := Field{Kind: "property", EventKey: "opened", SchemaVersion: 1, Key: "tags", ResolvedType: "array:string"}

	requests := []Request{}
	base := request(t, baseRequest)
	base.EventVersions = []EventVersion{{EventKey: "opened", SchemaVersion: 1}}
	metrics := base
	metrics.Metrics = []Metric{{Op: OpCount}, {Op: OpSum, Field: &valueField}, {Op: OpAvg, Field: &valueField}, {Op: OpMin, Field: &valueField}, {Op: OpMax, Field: &valueField}, {Op: OpP95, Field: &valueField}}
	requests = append(requests, metrics)
	for _, filter := range []Filter{
		{Op: "gte", Field: &valueField, Value: json.Number("0")},
		{Op: "lt", Field: &valueField, Value: json.Number("0")},
		{Op: "eq", Field: &valueField, Value: json.Number("100")},
		{Op: "neq", Field: &valueField, Value: json.Number("100")},
		{Op: "in", Field: &valueField, Value: []any{json.Number("0"), json.Number("-3")}},
		{Op: "eq", Field: &titleField, Value: "Home"},
		{Op: "prefix", Field: &titleField, Value: "Set"},
		{Op: "contains", Field: &titleField, Value: "me"},
		{Op: "neq", Field: &enabledField, Value: true},
		{Op: "is_present", Field: &valueField},
		{Op: "is_missing", Field: &valueField},
		{Op: "contains", Field: &tagsField, Value: "b"},
		{Op: "eq", Field: &tagsField, Value: []any{"a", "b"}},
		{Op: "neq", Field: &tagsField, Value: []any{"a", "b"}},
	} {
		r := base
		r.Filter = &filter
		requests = append(requests, r)
	}
	for _, field := range []Field{valueField, titleField, enabledField} {
		r := base
		r.GroupBy = []Field{field}
		requests = append(requests, r)
	}
	for index, query := range requests {
		gotLegacy := trendPlanRows(t, legacy, query, "event_facts")
		gotLayout := trendPlanRows(t, layout, query, "event_facts")
		if !reflect.DeepEqual(gotLegacy, gotLayout) {
			t.Fatalf("request %d diverges between legacy and layout reader:\nlegacy=%v\nlayout=%v", index, gotLegacy, gotLayout)
		}
	}

	// The view-backed events path must choose the same rows under a property filter.
	events := request(t, baseRequest)
	events.EventKeys = []string{"opened"}
	events.Filter = &Filter{Op: "eq", Field: &titleField, Value: "Home"}
	events.Limit = 50
	legacyEvents := execute(t, legacy, events)
	layoutEvents := execute(t, layout, events)
	if !reflect.DeepEqual(legacyEvents.Rows, layoutEvents.Rows) {
		t.Fatalf("events query diverges:\nlegacy=%v\nlayout=%v", legacyEvents.Rows, layoutEvents.Rows)
	}
}

func TestClickHousePropertyLayoutMixedWinnerFallback(t *testing.T) {
	client := clickHouseDatabase(t)
	// Identity A: the legacy row wins; the version-1 replay must not be read.
	winnerA := fact("mixed-a", "opened", "user", "device", "source-one", 10, nil)
	winnerA["properties_json"] = `{"value":5}`
	loserA := fact("mixed-a", "opened", "user", "device", "source-one", 20, nil)
	loserA["properties_json"] = `{"value":500}`
	loserA = layoutFactRow(t, loserA)
	// Identity B: the version-1 row wins; the legacy replay loses.
	winnerB := fact("mixed-b", "opened", "user", "device", "source-one", 10, nil)
	winnerB["properties_json"] = `{"value":7}`
	winnerB = layoutFactRow(t, winnerB)
	loserB := fact("mixed-b", "opened", "user", "device", "source-one", 20, nil)
	loserB["properties_json"] = `{"value":700}`
	seedRows(t, client, []map[string]any{winnerA, loserA, winnerB, loserB})

	field := Field{Kind: "property", EventKey: "opened", SchemaVersion: 1, Key: "value", ResolvedType: "number"}
	base := request(t, baseRequest)
	base.EventVersions = []EventVersion{{EventKey: "opened", SchemaVersion: 1}}
	for _, tc := range []struct {
		value string
		want  string
	}{
		{"5", "1"}, {"500", "0"}, {"7", "1"}, {"700", "0"},
	} {
		r := base
		r.Filter = &Filter{Op: "eq", Field: &field, Value: json.Number(tc.value)}
		got := trendPlanRows(t, client, r, "event_facts")
		if len(got) != 1 || fmt.Sprint(got[0]["metric_0"]) != tc.want {
			t.Fatalf("mixed winner cohort value=%s = %v, want count %s", tc.value, got, tc.want)
		}
	}
	grouped := base
	grouped.GroupBy = []Field{field}
	got := trendPlanRows(t, client, grouped, "event_facts")
	groups := map[string]string{}
	for _, row := range got {
		groups[fmt.Sprint(row["group_0"])] = fmt.Sprint(row["metric_0"])
	}
	want := map[string]string{"5": "1", "7": "1"}
	if !reflect.DeepEqual(groups, want) {
		t.Fatalf("mixed winner grouping = %v, want %v", groups, want)
	}
}
