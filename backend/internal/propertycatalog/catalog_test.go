package propertycatalog

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/howtomakeaname/Evia/backend/internal/propertylayout"
)

func entry(event string, version uint64, property, kind string) Entry {
	return Entry{EventKey: event, SchemaVersion: version, PropertyKey: property, ResolvedType: kind}
}

func validConfig(t *testing.T, revision uint32, entries ...Entry) *Catalog {
	t.Helper()
	catalog := &Catalog{Revision: revision, Entries: entries}
	if err := catalog.Validate(); err != nil {
		t.Fatalf("unexpected validation error: %v", err)
	}
	catalog.normalize()
	return catalog
}

func TestDisabledCatalogAndNilSafety(t *testing.T) {
	disabled := Disabled()
	if disabled.Enabled() {
		t.Fatal("zero catalog must be disabled")
	}
	if disabled.Columns() != nil {
		t.Fatal("disabled catalog must expose no columns")
	}
	if _, ok := disabled.Lookup("page_opened", 1, "score"); ok {
		t.Fatal("disabled catalog lookup must miss")
	}
	var nilCatalog *Catalog
	if nilCatalog.Enabled() {
		t.Fatal("nil catalog must report disabled")
	}
	if _, ok := nilCatalog.Lookup("page_opened", 1, "score"); ok {
		t.Fatal("nil catalog lookup must miss without panicking")
	}
	if err := nilCatalog.Validate(); err == nil {
		t.Fatal("nil catalog validation must fail")
	}
}

func TestParseConfigHappyPathAndNormalization(t *testing.T) {
	raw := []byte(`{
		"revision": 3,
		"entries": [
			{"event_key":"page_opened","schema_version":2,"property_key":"title","resolved_type":"string"},
			{"event_key":"page_opened","schema_version":1,"property_key":"score","resolved_type":"integer"}
		]
	}`)
	catalog, err := ParseConfig(raw)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if catalog.Revision != 3 || len(catalog.Entries) != 2 {
		t.Fatalf("unexpected catalog: %+v", catalog)
	}
	// Deterministic ordering regardless of authoring order.
	if catalog.Entries[0].PropertyKey != "score" || catalog.Entries[1].PropertyKey != "title" {
		t.Fatalf("entries not canonicalized: %+v", catalog.Entries)
	}
	slot, ok := catalog.Lookup("page_opened", 2, "title")
	if !ok || slot.ResolvedType != "string" || slot.Column != SlotColumn("title") {
		t.Fatalf("unexpected lookup result: %+v ok=%v", slot, ok)
	}
	if _, ok := catalog.Lookup("page_opened", 2, "score"); ok {
		t.Fatal("score is only selected on schema version 1")
	}
}

func TestParseConfigRejectsUnknownFieldsAndMalformedJSON(t *testing.T) {
	if _, err := ParseConfig([]byte(`{"revision":1,"entries":[],"extra":true}`)); err == nil {
		t.Fatal("unknown config field must be rejected")
	}
	if _, err := ParseConfig([]byte(`{not json`)); err == nil {
		t.Fatal("malformed JSON must be rejected")
	}
	if _, err := ParseConfig([]byte(`[]`)); err == nil {
		t.Fatal("non-object config must be rejected")
	}
}

func TestRevisionBounds(t *testing.T) {
	if err := (&Catalog{Revision: 0, Entries: []Entry{entry("a", 1, "k", "string")}}).Validate(); err == nil {
		t.Fatal("entries require a non-zero revision")
	}
	if err := (&Catalog{Revision: 7, Entries: nil}).Validate(); err == nil {
		t.Fatal("non-zero revision without entries must be rejected")
	}
	validConfig(t, MaxRevision, entry("a", 1, "k", "string"))
	if err := (&Catalog{Revision: MaxRevision + 1, Entries: []Entry{entry("a", 1, "k", "string")}}).Validate(); err == nil {
		t.Fatal("revision above the UInt8 marker bound must be rejected")
	}
}

func TestSlotCapIsEight(t *testing.T) {
	entries := make([]Entry, 0, MaxSlots+1)
	for i := 0; i <= MaxSlots; i++ {
		entries = append(entries, entry("page_opened", 1, "prop_"+string(rune('a'+i)), "string"))
	}
	validConfig(t, 1, entries[:MaxSlots]...)
	if err := (&Catalog{Revision: 1, Entries: entries}).Validate(); err == nil {
		t.Fatalf("more than %d entries must be rejected", MaxSlots)
	}
}

func TestEntryFieldValidation(t *testing.T) {
	bad := []Entry{
		entry("Page_Opened", 1, "score", "integer"),
		entry("page_opened", 1, "Score", "integer"),
		entry("page_opened", 0, "score", "integer"),
		entry("page_opened", 1, "score", "datetime"),
		entry("page_opened", 1, "score", "float"),
	}
	for _, candidate := range bad {
		if err := (&Catalog{Revision: 1, Entries: []Entry{candidate}}).Validate(); err == nil {
			t.Fatalf("invalid entry accepted: %+v", candidate)
		}
	}
	for _, kind := range []string{"string", "boolean", "number", "integer", "array:string", "array:boolean", "array:number", "array:integer"} {
		validConfig(t, 1, entry("page_opened", 1, "field_"+strings.ReplaceAll(kind, ":", "_"), kind))
	}
}

func TestIdentifierPatternsMatchAnalysisContract(t *testing.T) {
	for _, good := range []string{"page_opened", "sys.page_view", "a", "event_07"} {
		if !eventKeyPattern.MatchString(good) {
			t.Fatalf("event key %q should match", good)
		}
	}
	for _, bad := range []string{"Page", "1event", "_event", "event-x"} {
		if eventKeyPattern.MatchString(bad) {
			t.Fatalf("event key %q should not match", bad)
		}
	}
	for _, good := range []string{"score", "a", "segment_03"} {
		if !propertyKeyPattern.MatchString(good) {
			t.Fatalf("property key %q should match", good)
		}
	}
	for _, bad := range []string{"Score", "1score", "page.title"} {
		if propertyKeyPattern.MatchString(bad) {
			t.Fatalf("property key %q should not match", bad)
		}
	}
}

func TestDuplicateIdentityRejected(t *testing.T) {
	first := entry("page_opened", 1, "score", "integer")
	second := entry("page_opened", 1, "score", "number")
	if err := (&Catalog{Revision: 1, Entries: []Entry{first, second}}).Validate(); err == nil {
		t.Fatal("duplicate event/version/property identity must be rejected even with different types")
	}
}

func TestSharedPropertyKeyAcrossEventsUsesOneColumn(t *testing.T) {
	catalog := validConfig(t, 1,
		entry("page_opened", 1, "score", "integer"),
		entry("button_clicked", 4, "score", "integer"),
	)
	columns := catalog.Columns()
	if len(columns) != 1 || columns[0] != SlotColumn("score") {
		t.Fatalf("shared property key must collapse to one column, got %v", columns)
	}
	for _, identity := range []struct {
		event   string
		version uint64
	}{
		{"page_opened", 1}, {"button_clicked", 4},
	} {
		slot, ok := catalog.Lookup(identity.event, identity.version, "score")
		if !ok || slot.Column != columns[0] {
			t.Fatalf("lookup miss for shared key: %+v", identity)
		}
	}
}

func TestColumnsAreSortedAndOrderIndependent(t *testing.T) {
	first := validConfig(t, 1,
		entry("e", 1, "zebra", "string"),
		entry("e", 1, "alpha", "string"),
		entry("e", 1, "middle", "string"),
	)
	second := validConfig(t, 1,
		entry("e", 1, "middle", "string"),
		entry("e", 1, "zebra", "string"),
		entry("e", 1, "alpha", "string"),
	)
	if strings.Join(first.Columns(), ",") != strings.Join(second.Columns(), ",") {
		t.Fatal("column order must not depend on authoring order")
	}
	columns := first.Columns()
	if columns[0] != SlotColumn("alpha") || columns[2] != SlotColumn("zebra") {
		t.Fatalf("columns must be sorted: %v", columns)
	}
}

func TestSlotColumnReusesPropertyLayoutEncoding(t *testing.T) {
	for _, key := range []string{"score", "segment_03", "page.title", "中文键"} {
		if got := SlotColumn(key); got != propertylayout.EncodeKey(key) || !strings.HasPrefix(got, propertylayout.KeyPrefix) {
			t.Fatalf("slot column %q diverges from the shared encoding for %q", got, key)
		}
	}
}

func TestLoadFileHappyPathAndFailClosed(t *testing.T) {
	dir := t.TempDir()
	validPath := filepath.Join(dir, "catalog.json")
	valid := []byte(`{"revision":1,"entries":[{"event_key":"page_opened","schema_version":1,"property_key":"score","resolved_type":"integer"}]}`)
	if err := os.WriteFile(validPath, valid, 0o600); err != nil {
		t.Fatal(err)
	}
	catalog, err := LoadFile(validPath)
	if err != nil || !catalog.Enabled() || catalog.Revision != 1 {
		t.Fatalf("valid catalog file rejected: %+v err=%v", catalog, err)
	}
	if _, err := LoadFile(filepath.Join(dir, "missing.json")); err == nil {
		t.Fatal("missing catalog file must fail closed rather than disable silently")
	}
	invalidPath := filepath.Join(dir, "invalid.json")
	if err := os.WriteFile(invalidPath, []byte(`{"revision":1,"entries":[{"event_key":"BAD","schema_version":1,"property_key":"k","resolved_type":"string"}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadFile(invalidPath); err == nil {
		t.Fatal("invalid catalog content must be rejected at load time")
	}
	if err := os.WriteFile(filepath.Join(dir, "broken.json"), []byte(`{not json`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadFile(filepath.Join(dir, "broken.json")); err == nil {
		t.Fatal("malformed catalog JSON must be rejected")
	}
}

func TestConfigJSONRoundTrip(t *testing.T) {
	catalog := validConfig(t, 9,
		entry("page_opened", 1, "score", "integer"),
		entry("page_opened", 2, "title", "string"),
	)
	raw, err := json.Marshal(catalog)
	if err != nil {
		t.Fatalf("marshal failed: %v", err)
	}
	parsed, err := ParseConfig(raw)
	if err != nil {
		t.Fatalf("round trip parse failed: %v", err)
	}
	if parsed.Revision != catalog.Revision || len(parsed.Entries) != len(catalog.Entries) {
		t.Fatalf("round trip changed the catalog: %+v", parsed)
	}
	for i := range catalog.Entries {
		if parsed.Entries[i] != catalog.Entries[i] {
			t.Fatalf("entry %d changed across round trip: %+v != %+v", i, parsed.Entries[i], catalog.Entries[i])
		}
	}
}
