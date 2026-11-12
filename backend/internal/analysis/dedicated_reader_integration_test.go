//go:build integration

package analysis

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/howtomakeaname/Evia/backend/internal/propertycatalog"
	"github.com/howtomakeaname/Evia/backend/internal/propertylayout"
)

// dedicatedEquivalenceCatalog selects the two properties exercised below.
func dedicatedEquivalenceCatalog(t *testing.T) *propertycatalog.Catalog {
	t.Helper()
	catalog := &propertycatalog.Catalog{Revision: 7, Entries: []propertycatalog.Entry{
		{EventKey: "opened", SchemaVersion: 1, PropertyKey: "value", ResolvedType: "number"},
		{EventKey: "opened", SchemaVersion: 1, PropertyKey: "title", ResolvedType: "string"},
	}}
	if err := catalog.Validate(); err != nil {
		t.Fatal(err)
	}
	return catalog
}

// dedicatedDatabase provisions the full schema, adds the catalog's slot
// columns and recreates the view so the winning tuple carries them. This is
// the test-only projection of the future slot-view migration.
func dedicatedDatabase(t *testing.T, catalog *propertycatalog.Catalog) *ClickHouse {
	t.Helper()
	client := clickHouseDatabase(t)
	for _, ddl := range catalog.SlotDDL() {
		chExec(t, client, ddl)
	}
	columns := catalog.Columns()
	projections := ""
	tupleSlots := ""
	for index, column := range columns {
		projections += ",\n    fact." + itoa(30+index) + " AS " + column
		tupleSlots += ", " + column
	}
	view := "CREATE OR REPLACE VIEW event_facts AS " +
		"SELECT project_id, environment, source_id, event_id," +
		" fact.1 AS event_key, fact.2 AS schema_version, fact.3 AS release_revision," +
		" fact.4 AS occurred_at, fact.5 AS event_time, fact.6 AS received_at, fact.7 AS canonical_hash," +
		" fact.8 AS uid, fact.9 AS anonymous_id, fact.10 AS session_id, fact.11 AS tab_id, fact.12 AS sequence," +
		" fact.13 AS page_view_id, fact.14 AS source_page_view_id, fact.15 AS navigation_id, fact.16 AS operation_id," +
		" fact.17 AS route_name, fact.18 AS route_template, fact.19 AS source_module, fact.20 AS navigation_type," +
		" fact.21 AS previous_page_view_id, fact.22 AS sdk_name, fact.23 AS sdk_version, fact.24 AS context_json," +
		" fact.25 AS properties_json, fact.26 AS source_kind, fact.27 AS property_layout_version," +
		" fact.28 AS property_tokens, fact.29 AS dedicated_layout_version" + projections +
		" FROM (SELECT project_id, environment, source_id, event_id," +
		" argMin(tuple(event_key, schema_version, release_revision," +
		" coalesce(occurred_at_precise, toDateTime64(occurred_at, 9, 'UTC'))," +
		" coalesce(event_time_precise, toDateTime64(event_time, 9, 'UTC'))," +
		" received_at, canonical_hash, uid, anonymous_id, session_id, tab_id, sequence, page_view_id," +
		" source_page_view_id, navigation_id, operation_id, route_name, route_template, source_module," +
		" navigation_type, previous_page_view_id, sdk_name, sdk_version, context_json, properties_json, source_kind," +
		" property_layout_version, property_tokens, dedicated_layout_version" + tupleSlots + ")," +
		" tuple(received_at, canonical_hash, coalesce(occurred_at_precise, toDateTime64(occurred_at, 9, 'UTC'))," +
		" coalesce(event_time_precise, toDateTime64(event_time, 9, 'UTC')))) AS fact" +
		" FROM event_facts_raw GROUP BY project_id, environment, source_id, event_id)"
	chExec(t, client, view)
	return client
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [4]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}

// dedicatedFactRow enriches a fixture row with the catalog marker and the
// exact slot tokens derived from its authoritative document.
func dedicatedFactRow(t *testing.T, row map[string]any, catalog *propertycatalog.Catalog) map[string]any {
	t.Helper()
	out := cloneTrendFact(row)
	derived, err := propertylayout.DeriveMap([]byte(row["properties_json"].(string)))
	if err != nil {
		t.Fatal(err)
	}
	out["dedicated_layout_version"] = catalog.Revision
	for _, column := range catalog.Columns() {
		if token, ok := derived[column]; ok {
			// JSONEachRow encodes this Go string as a JSON string, so the
			// physical String column stores the exact token content.
			out[column] = string(token)
		}
	}
	return out
}

func TestClickHouseDedicatedReaderMatchesDocumentReader(t *testing.T) {
	catalog := dedicatedEquivalenceCatalog(t)
	client := dedicatedDatabase(t, catalog)

	documents := []string{
		`{"value":1.5}`, `{"value":1e2}`, `{"value":1.00}`, `{"value":0}`, `{"value":-3}`,
		`{"value":null}`, `{}`, `{"value":"100"}`, `{"value":true}`,
		`{"title":"Home","value":7}`, `{"title":"Settings","value":8}`,
	}
	rows := make([]map[string]any, 0, len(documents))
	for index, document := range documents {
		row := fact("dedicated-"+string(rune('a'+index)), "opened", "user", "device", "source-one", index, nil)
		row["properties_json"] = document
		rows = append(rows, dedicatedFactRow(t, row, catalog))
	}
	// One legacy row keeps the structural default marker and empty slots, so
	// its reads must fall back to the document even on the catalog engine.
	legacy := fact("dedicated-legacy", "opened", "user", "device", "source-one", 99, nil)
	legacy["properties_json"] = `{"title":"Home","value":7}`
	rows = append(rows, legacy)
	seedRows(t, client, rows)

	disabled, err := NewClickHouse(ClickHouseConfig{
		URL: client.endpoint.String(), Database: client.database,
		Username: client.username, Password: client.password, Client: client.client,
	})
	if err != nil {
		t.Fatal(err)
	}
	enabled, err := NewClickHouse(ClickHouseConfig{
		URL: client.endpoint.String(), Database: client.database,
		Username: client.username, Password: client.password, Client: client.client, Catalog: catalog,
	})
	if err != nil {
		t.Fatal(err)
	}

	titleField := Field{Kind: "property", EventKey: "opened", SchemaVersion: 1, Key: "title", ResolvedType: "string"}
	valueField := Field{Kind: "property", EventKey: "opened", SchemaVersion: 1, Key: "value", ResolvedType: "number"}
	eventsBase := strings.Replace(baseRequest, `"kind":"trend"`, `"kind":"events"`, 1)
	queries := []Request{}
	for _, filter := range []Filter{
		{Op: "eq", Field: &titleField, Value: "Home"},
		{Op: "prefix", Field: &titleField, Value: "Set"},
		{Op: "is_present", Field: &titleField},
		{Op: "is_missing", Field: &titleField},
		{Op: "gte", Field: &valueField, Value: json.Number("0")},
		{Op: "eq", Field: &valueField, Value: json.Number("100")},
	} {
		r := request(t, eventsBase)
		r.EventKeys = []string{"opened"}
		r.Filter = &filter
		r.Limit = 50
		queries = append(queries, r)
	}
	for index, query := range queries {
		gotDisabled := execute(t, disabled, query)
		gotEnabled := execute(t, enabled, query)
		if !reflect.DeepEqual(gotDisabled.Rows, gotEnabled.Rows) {
			t.Fatalf("events query %d diverges between document and dedicated readers:\ndisabled=%v\nenabled=%v", index, gotDisabled.Rows, gotEnabled.Rows)
		}
	}

	// The legacy fallback row must be returned under a title filter on both
	// engines, proving the zero-marker/empty-slot path reads the document.
	fallback := request(t, eventsBase)
	fallback.EventKeys = []string{"opened"}
	fallback.Filter = &Filter{Op: "eq", Field: &titleField, Value: "Home"}
	fallback.Limit = 50
	enabledRows := execute(t, enabled, fallback).Rows
	found := false
	for _, row := range enabledRows {
		if row["event_id"] == "dedicated-legacy" {
			found = true
		}
	}
	if !found {
		t.Fatalf("legacy fallback row missing from dedicated reader results: %v", enabledRows)
	}
}
