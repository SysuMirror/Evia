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

// dedicatedDatabase provisions the full schema and applies the production
// migration generator's output, so the test exercises the exact DDL an
// operator would obtain from evia-catalog-migration rather than a hand-built
// test-only view.
func dedicatedDatabase(t *testing.T, catalog *propertycatalog.Catalog) *ClickHouse {
	t.Helper()
	client := clickHouseDatabase(t)
	for _, ddl := range catalog.SlotDDL() {
		chExec(t, client, ddl)
	}
	chExec(t, client, catalog.ViewDDL())
	return client
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

func TestClickHouseDedicatedTrendMatchesDocumentTrend(t *testing.T) {
	catalog := dedicatedEquivalenceCatalog(t)
	client := dedicatedDatabase(t, catalog)

	documents := []string{
		`{"value":1.5}`, `{"value":1e2}`, `{"value":1.00}`, `{"value":0}`, `{"value":-3}`,
		`{"value":null}`, `{}`, `{"value":"100"}`, `{"value":true}`,
		`{"title":"Home","value":7}`, `{"title":"Settings","value":8}`,
	}
	rows := make([]map[string]any, 0, len(documents)+1)
	for index, document := range documents {
		row := fact("trend-"+string(rune('a'+index)), "opened", "user", "device", "source-one", index, nil)
		row["properties_json"] = document
		rows = append(rows, dedicatedFactRow(t, row, catalog))
	}
	// Legacy row: zero marker and empty slots must fall back to the document.
	legacy := fact("trend-legacy", "opened", "user", "device", "source-one", 99, nil)
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
	base := request(t, baseRequest) // trend kind
	base.EventVersions = []EventVersion{{EventKey: "opened", SchemaVersion: 1}}
	queries := []Request{}

	metrics := base
	metrics.Metrics = []Metric{
		{Op: OpCount}, {Op: OpSum, Field: &valueField}, {Op: OpAvg, Field: &valueField},
		{Op: OpMin, Field: &valueField}, {Op: OpMax, Field: &valueField},
	}
	queries = append(queries, metrics)

	grouped := base
	grouped.GroupBy = []Field{titleField}
	grouped.Metrics = []Metric{{Op: OpCount}}
	queries = append(queries, grouped)

	for _, filter := range []Filter{
		{Op: "gte", Field: &valueField, Value: json.Number("0")},
		{Op: "eq", Field: &valueField, Value: json.Number("100")},
		{Op: "is_present", Field: &titleField},
		{Op: "is_missing", Field: &titleField},
	} {
		r := base
		r.Filter = &filter
		r.Metrics = []Metric{{Op: OpCount}}
		queries = append(queries, r)
	}

	for index, query := range queries {
		gotDisabled := execute(t, disabled, query)
		gotEnabled := execute(t, enabled, query)
		if !reflect.DeepEqual(gotDisabled.Rows, gotEnabled.Rows) {
			t.Fatalf("trend query %d diverges:\ndisabled=%v\nenabled=%v", index, gotDisabled.Rows, gotEnabled.Rows)
		}
	}
}
