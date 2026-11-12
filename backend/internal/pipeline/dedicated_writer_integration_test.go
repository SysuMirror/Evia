//go:build integration

package pipeline

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/howtomakeaname/Evia/backend/internal/ingestion"
	"github.com/howtomakeaname/Evia/backend/internal/propertycatalog"
)

// dedicatedWriterDatabase provisions the full fact schema (migrations
// 001-009) and then creates the catalog's physical slot columns with the
// production SlotDDL generator, mirroring the operator rollout order.
func dedicatedWriterDatabase(t *testing.T, catalog *propertycatalog.Catalog) ClickHouseConfig {
	t.Helper()
	config := propertyLayoutDatabase(t)
	ctx := context.Background()
	for _, statement := range catalog.SlotDDL() {
		if _, err := clickhouseQuery(ctx, config, statement); err != nil {
			t.Fatal(err)
		}
	}
	return config
}

func dedicatedTestCatalog() *propertycatalog.Catalog {
	catalog := &propertycatalog.Catalog{Revision: 5, Entries: []propertycatalog.Entry{
		{EventKey: "page_opened", SchemaVersion: 1, PropertyKey: "exp", ResolvedType: "integer"},
		{EventKey: "page_opened", SchemaVersion: 1, PropertyKey: "title", ResolvedType: "string"},
	}}
	if err := catalog.Validate(); err != nil {
		panic(err)
	}
	return catalog
}

type storedDedicatedRow struct {
	PropertiesJSON string `json:"properties_json"`
	Marker         uint8  `json:"dedicated_layout_version"`
	ExpToken       string `json:"exp_token"`
	TitleToken     string `json:"title_token"`
}

func readDedicatedRow(t *testing.T, config ClickHouseConfig, eventID string) storedDedicatedRow {
	t.Helper()
	expColumn := propertycatalog.SlotColumn("exp")
	titleColumn := propertycatalog.SlotColumn("title")
	data, err := clickhouseQuery(context.Background(), config,
		"SELECT properties_json, dedicated_layout_version, "+
			expColumn+" AS exp_token, "+titleColumn+" AS title_token "+
			"FROM event_facts_raw WHERE event_id='"+eventID+"' FORMAT JSONEachRow")
	if err != nil {
		t.Fatal(err)
	}
	var row storedDedicatedRow
	if err = json.Unmarshal(data, &row); err != nil {
		t.Fatal(err, string(data))
	}
	return row
}

func TestClickHouseDedicatedWriterMaterializesTokensAndMarker(t *testing.T) {
	catalog := dedicatedTestCatalog()
	config := dedicatedWriterDatabase(t, catalog)
	ctx := context.Background()
	sink, err := NewClickHouse(ClickHouseConfig{
		URL: config.URL, Database: config.Database, Username: config.Username,
		Password: config.Password, Client: config.Client, Catalog: catalog,
	})
	if err != nil {
		t.Fatal(err)
	}

	richID := "66666666-6666-4666-8666-666666666661"
	emptyID := "66666666-6666-4666-8666-666666666662"
	richDocument := `{"exp":1e2,"title":"Home"}`
	records := []ingestion.Record{
		layoutRecord(richID, richDocument),
		layoutRecord(emptyID, `{}`),
	}
	for _, record := range records {
		if err = sink.Write(ctx, []ingestion.Record{record}); err != nil {
			t.Fatal(err)
		}
	}

	rich := readDedicatedRow(t, config, richID)
	if rich.Marker != 5 {
		t.Fatalf("rich marker = %d, want catalog revision 5", rich.Marker)
	}
	// Lexical numeric form must survive the String slot untouched.
	if rich.ExpToken != "1e2" {
		t.Fatalf("exp slot token = %q, want literal 1e2", rich.ExpToken)
	}
	// String values keep their full JSON spelling inside the slot.
	if rich.TitleToken != `"Home"` {
		t.Fatalf("title slot token = %q, want JSON string token", rich.TitleToken)
	}
	if rich.PropertiesJSON != richDocument {
		t.Fatalf("authoritative document changed: %s", rich.PropertiesJSON)
	}

	empty := readDedicatedRow(t, config, emptyID)
	if empty.Marker != 5 {
		t.Fatalf("empty document still records the active revision, got %d", empty.Marker)
	}
	if empty.ExpToken != "" || empty.TitleToken != "" {
		t.Fatalf("absent properties must resolve to the zero-byte default, got %q %q", empty.ExpToken, empty.TitleToken)
	}

	// The marker must travel through the deduplicated view as fact.29.
	viewMarker, err := clickhouseQuery(ctx, config,
		"SELECT toString(dedicated_layout_version) FROM event_facts WHERE event_id='"+richID+"' FORMAT TSV")
	if err != nil {
		t.Fatal(err)
	}
	if string(viewMarker) != "5\n" {
		t.Fatalf("view marker = %q, want 5", viewMarker)
	}
}

func TestClickHouseDisabledWriterKeepsLegacyDefaults(t *testing.T) {
	catalog := dedicatedTestCatalog()
	config := dedicatedWriterDatabase(t, catalog)
	ctx := context.Background()
	// No Catalog field: the constructor substitutes the disabled catalog.
	sink, err := NewClickHouse(ClickHouseConfig{
		URL: config.URL, Database: config.Database, Username: config.Username,
		Password: config.Password, Client: config.Client,
	})
	if err != nil {
		t.Fatal(err)
	}
	legacyID := "66666666-6666-4666-8666-666666666663"
	if err = sink.Write(ctx, []ingestion.Record{layoutRecord(legacyID, `{"title":"Legacy"}`)}); err != nil {
		t.Fatal(err)
	}
	stored := readDedicatedRow(t, config, legacyID)
	if stored.Marker != 0 {
		t.Fatalf("disabled writer marker = %d, want structural default 0", stored.Marker)
	}
	if stored.TitleToken != "" {
		t.Fatalf("disabled writer must leave the slot at its zero-byte default, got %q", stored.TitleToken)
	}
}
