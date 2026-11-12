package pipeline

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/howtomakeaname/Evia/backend/internal/propertycatalog"
	"github.com/howtomakeaname/Evia/backend/internal/propertylayout"
)

func mustCatalog(t *testing.T, revision uint32, entries ...propertycatalog.Entry) *propertycatalog.Catalog {
	t.Helper()
	catalog := &propertycatalog.Catalog{Revision: revision, Entries: entries}
	if err := catalog.Validate(); err != nil {
		t.Fatal(err)
	}
	return catalog
}

func marshaledMap(t *testing.T, row factRow) map[string]json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(row)
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]json.RawMessage
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err, string(raw))
	}
	return out
}

func TestDisabledCatalogPayloadHasNoDedicatedFields(t *testing.T) {
	row, err := rowFor(testRecord())
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(row)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "dedicated") {
		t.Fatalf("disabled catalog payload must contain no dedicated fields: %s", raw)
	}
	if row.DedicatedLayoutVersion != 0 || len(row.dedicatedSlots) != 0 {
		t.Fatalf("disabled row must carry marker 0 and no slots: %+v", row)
	}
	// Explicit nil catalog must behave exactly like the disabled catalog.
	nilRow, err := rowForWithCatalog(testRecord(), nil)
	if err != nil {
		t.Fatal(err)
	}
	nilRaw, err := json.Marshal(nilRow)
	if err != nil {
		t.Fatal(err)
	}
	if string(nilRaw) != string(raw) {
		t.Fatalf("nil catalog changed the payload:\n%s\n%s", raw, nilRaw)
	}
}

func TestEnabledCatalogMaterializesMatchingSlotsAndMarker(t *testing.T) {
	catalog := mustCatalog(t, 4,
		propertycatalog.Entry{EventKey: "page_opened", SchemaVersion: 1, PropertyKey: "title", ResolvedType: "string"},
	)
	row, err := rowForWithCatalog(testRecord(), catalog)
	if err != nil {
		t.Fatal(err)
	}
	if row.DedicatedLayoutVersion != 4 {
		t.Fatalf("marker = %d, want catalog revision 4", row.DedicatedLayoutVersion)
	}
	payload := marshaledMap(t, row)
	column := propertycatalog.SlotColumn("title")
	token, ok := payload[column]
	if !ok {
		t.Fatalf("materialized slot column %s missing from payload: %s", column, payload)
	}
	var storedTitle string
	if err := json.Unmarshal(token, &storedTitle); err != nil {
		t.Fatal(err)
	}
	if storedTitle != `"Home"` {
		t.Fatalf("title slot content = %q, want the exact token including quotes", storedTitle)
	}
	if string(payload["dedicated_layout_version"]) != "4" {
		t.Fatalf("marker not serialized: %s", payload["dedicated_layout_version"])
	}
	// A property outside the catalog must not appear as a slot column even
	// though it remains in the authoritative document and token map.
	if _, ok := payload[propertycatalog.SlotColumn("count")]; ok {
		t.Fatal("non-catalog property must not be emitted as a slot column")
	}
	if _, ok := payload[propertylayout.EncodeKey("count")]; ok {
		t.Fatal("non-catalog property must not leak its map key into the payload")
	}
}

func TestSlotSerializationDistinguishesAbsentFromEmptyString(t *testing.T) {
	catalog := mustCatalog(t, 1,
		propertycatalog.Entry{EventKey: "page_opened", SchemaVersion: 1, PropertyKey: "label", ResolvedType: "string"},
		propertycatalog.Entry{EventKey: "page_opened", SchemaVersion: 1, PropertyKey: "missing", ResolvedType: "string"},
	)
	record := testRecord()
	record.PropertiesJSON = json.RawMessage(`{"label":""}`)
	row, err := rowForWithCatalog(record, catalog)
	if err != nil {
		t.Fatal(err)
	}
	payload := marshaledMap(t, row)
	labelColumn := propertycatalog.SlotColumn("label")
	var storedLabel string
	if err := json.Unmarshal(payload[labelColumn], &storedLabel); err != nil {
		t.Fatal(err)
	}
	if storedLabel != `""` || len(storedLabel) != 2 {
		t.Fatalf("empty-string property must keep the two-byte token as column content, got %q", storedLabel)
	}
	// The absent property's column is omitted so ClickHouse applies its
	// structural DEFAULT '' instead of persisting a misleading token.
	if _, ok := payload[propertycatalog.SlotColumn("missing")]; ok {
		t.Fatal("absent property column must be omitted from the payload")
	}
}

func TestSlotTokenStaysLexicallyLossless(t *testing.T) {
	catalog := mustCatalog(t, 1,
		propertycatalog.Entry{EventKey: "page_opened", SchemaVersion: 1, PropertyKey: "metric", ResolvedType: "integer"},
	)
	record := testRecord()
	record.PropertiesJSON = json.RawMessage(`{"metric":1e2}`)
	row, err := rowForWithCatalog(record, catalog)
	if err != nil {
		t.Fatal(err)
	}
	payload := marshaledMap(t, row)
	column := propertycatalog.SlotColumn("metric")
	var storedMetric string
	if err := json.Unmarshal(payload[column], &storedMetric); err != nil {
		t.Fatal(err)
	}
	if storedMetric != "1e2" {
		t.Fatalf("numeric slot content = %q, must preserve lexical form 1e2", storedMetric)
	}
}

func TestUnmatchedEventIdentityKeepsMarkerWithoutSlots(t *testing.T) {
	catalog := mustCatalog(t, 2,
		propertycatalog.Entry{EventKey: "button_clicked", SchemaVersion: 1, PropertyKey: "title", ResolvedType: "string"},
	)
	// testRecord is page_opened, which has no catalog entry.
	row, err := rowForWithCatalog(testRecord(), catalog)
	if err != nil {
		t.Fatal(err)
	}
	if row.DedicatedLayoutVersion != 2 {
		t.Fatal("marker still records the active catalog revision")
	}
	// The slot name may legitimately appear inside the nested property_tokens
	// map; what must be absent is the top-level dedicated column itself.
	payload := marshaledMap(t, row)
	if _, ok := payload[propertycatalog.SlotColumn("title")]; ok {
		t.Fatalf("unmatched event must emit no dedicated top-level column: %v", payload)
	}
}
