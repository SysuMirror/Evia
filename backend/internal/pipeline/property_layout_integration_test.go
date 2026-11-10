//go:build integration

package pipeline

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/howtomakeaname/Evia/backend/internal/ingestion"
	"github.com/howtomakeaname/Evia/backend/internal/pipeline/propertylayout"
)

// propertyLayoutDatabase provisions a ClickHouse database with every released
// fact migration, including the version 006 property layout columns.
func propertyLayoutDatabase(t *testing.T) ClickHouseConfig {
	t.Helper()
	config := precisionDatabase(t) // applies 001 and registers cleanup
	ctx := context.Background()
	names, err := filepath.Glob("../../../deploy/clickhouse/*.sql")
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range names {
		if strings.HasSuffix(name, "001_event_facts.sql") {
			continue
		}
		applyFactMigration(t, ctx, config, name)
	}
	return config
}

func layoutRecord(eventID, properties string) ingestion.Record {
	record := testRecord()
	record.EventID = eventID
	record.PropertiesJSON = json.RawMessage(properties)
	return record
}

type storedLayoutRow struct {
	PropertiesJSON string            `json:"properties_json"`
	LayoutVersion  uint8             `json:"property_layout_version"`
	Tokens         map[string]string `json:"property_tokens"`
}

func readLayoutRow(t *testing.T, config ClickHouseConfig, eventID string) storedLayoutRow {
	t.Helper()
	data, err := clickhouseQuery(context.Background(), config,
		"SELECT properties_json, property_layout_version, property_tokens "+
			"FROM event_facts_raw WHERE event_id='"+eventID+"' FORMAT JSONEachRow")
	if err != nil {
		t.Fatal(err)
	}
	var row storedLayoutRow
	if err = json.Unmarshal(data, &row); err != nil {
		t.Fatal(err, string(data))
	}
	return row
}

func TestClickHousePropertyLayoutWriterTokens(t *testing.T) {
	config := propertyLayoutDatabase(t)
	ctx := context.Background()
	sink, err := NewClickHouse(config)
	if err != nil {
		t.Fatal(err)
	}
	// Every lexical form below is one ClickHouse-native JSON extraction was
	// shown to normalize; all shapes stay inside the ingestion contract.
	richID := "44444444-4444-4444-8444-444444444441"
	emptyID := "44444444-4444-4444-8444-444444444442"
	records := []ingestion.Record{
		layoutRecord(richID, `{"exp":1e2,"decimal":1.00,"long_decimal":0.1234567890123456789,"signed_zero":-0.0,"zero":0,"enabled":false,"title":"Home","tags":["a","b"],"scores":[1,1.00]}`),
		layoutRecord(emptyID, `{}`),
	}
	for _, record := range records {
		expected, err := rowFor(record)
		if err != nil {
			t.Fatal(err)
		}
		if err = sink.Write(ctx, []ingestion.Record{record}); err != nil {
			t.Fatal(err)
		}
		stored := readLayoutRow(t, config, record.EventID)
		if stored.LayoutVersion != expected.PropertyLayoutVersion || stored.LayoutVersion != 1 {
			t.Fatalf("layout version = %d, want 1", stored.LayoutVersion)
		}
		if stored.PropertiesJSON != expected.PropertiesJSON {
			t.Fatalf("authoritative document changed: got %s want %s", stored.PropertiesJSON, expected.PropertiesJSON)
		}
		if len(stored.Tokens) != len(expected.PropertyTokens) {
			t.Fatalf("token count = %d, want %d", len(stored.Tokens), len(expected.PropertyTokens))
		}
		for encoded, token := range expected.PropertyTokens {
			if got := stored.Tokens[encoded]; got != token {
				t.Fatalf("token %s = %q, want %q", encoded, got, token)
			}
		}
	}
	// Pin the exact lexical text inside ClickHouse itself, not only in Go.
	rich := readLayoutRow(t, config, richID)
	pins := map[string]string{
		"exp":          "1e2",
		"decimal":      "1.00",
		"long_decimal": "0.1234567890123456789",
		"signed_zero":  "-0.0",
		"zero":         "0",
		"enabled":      "false",
		"title":        `"Home"`,
		"tags":         `["a","b"]`,
		"scores":       "[1,1.00]",
	}
	for key, want := range pins {
		if got := rich.Tokens[propertylayout.EncodeKey(key)]; got != want {
			t.Fatalf("stored token for %q = %q, want %q", key, got, want)
		}
	}
	// An empty properties object legitimately derives version 1 with no tokens;
	// readers must not treat an empty map as "layout absent".
	empty := readLayoutRow(t, config, emptyID)
	if empty.LayoutVersion != 1 || len(empty.Tokens) != 0 {
		t.Fatalf("empty object layout = version %d, %d tokens; want version 1 with empty map", empty.LayoutVersion, len(empty.Tokens))
	}
}

func TestClickHousePropertyLayoutLegacyWriterDefaults(t *testing.T) {
	config := propertyLayoutDatabase(t)
	ctx := context.Background()
	legacyID := "55555555-5555-4555-8555-555555555551"
	record := layoutRecord(legacyID, `{"count":0,"title":"Legacy"}`)
	row, err := rowFor(record)
	if err != nil {
		t.Fatal(err)
	}
	// Simulate a pre-006 writer: strip the derived fields before the raw insert.
	encoded, err := json.Marshal(row)
	if err != nil {
		t.Fatal(err)
	}
	var values map[string]any
	if err = json.Unmarshal(encoded, &values); err != nil {
		t.Fatal(err)
	}
	delete(values, "property_layout_version")
	delete(values, "property_tokens")
	encoded, err = json.Marshal(values)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = clickhouseQuery(ctx, config, "INSERT INTO event_facts_raw FORMAT JSONEachRow\n"+string(encoded)); err != nil {
		t.Fatal(err)
	}
	legacy := readLayoutRow(t, config, legacyID)
	if legacy.LayoutVersion != 0 {
		t.Fatalf("legacy layout version = %d, want 0", legacy.LayoutVersion)
	}
	if len(legacy.Tokens) != 0 {
		t.Fatalf("legacy token map = %v, want empty map", legacy.Tokens)
	}
	if legacy.PropertiesJSON != row.PropertiesJSON {
		t.Fatalf("legacy document changed: got %s want %s", legacy.PropertiesJSON, row.PropertiesJSON)
	}
	// A new writer and the legacy profile must coexist while the view stays on
	// the authoritative document path.
	sink, err := NewClickHouse(config)
	if err != nil {
		t.Fatal(err)
	}
	newID := "55555555-5555-4555-8555-555555555552"
	if err = sink.Write(ctx, []ingestion.Record{layoutRecord(newID, `{"title":"New"}`)}); err != nil {
		t.Fatal(err)
	}
	if count := integrationCount(t, ctx, config, "event_facts"); count != 2 {
		t.Fatalf("view fact count = %d, want 2", count)
	}
	data, err := clickhouseQuery(ctx, config, "SELECT properties_json FROM event_facts WHERE event_id='"+legacyID+"' FORMAT JSONEachRow")
	if err != nil {
		t.Fatal(err)
	}
	var viewRow struct {
		PropertiesJSON string `json:"properties_json"`
	}
	if err = json.Unmarshal(data, &viewRow); err != nil {
		t.Fatal(err, string(data))
	}
	if viewRow.PropertiesJSON != row.PropertiesJSON {
		t.Fatalf("view no longer serves the legacy document: got %s want %s", viewRow.PropertiesJSON, row.PropertiesJSON)
	}
	newRow := readLayoutRow(t, config, newID)
	if newRow.LayoutVersion != 1 || len(newRow.Tokens) != 1 {
		t.Fatalf("new writer layout = version %d, %d tokens; want version 1 with 1 token", newRow.LayoutVersion, len(newRow.Tokens))
	}
}
