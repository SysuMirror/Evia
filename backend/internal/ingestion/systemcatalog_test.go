package ingestion

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/howtomakeaname/Evia/backend/internal/gen/wire"
)

func TestIsSystemKeyClosedPattern(t *testing.T) {
	positive := []string{"sys.page_view", "sys.page_scroll", "sys.identity_link", "sys.session_end", "sys.operation_failed", "sys.a"}
	for _, key := range positive {
		if !IsSystemKey(key) {
			t.Fatalf("reserved key %q must be recognised", key)
		}
	}
	negative := []string{
		"page_view",           // custom key without prefix
		"SYS.page_view",       // prefix is case-sensitive
		"sys.",                // prefix alone
		"sys.1page",           // first body character must be a letter
		"sys.page-view",       // hyphen is outside the envelope charset
		"sys.page_view.extra", // a second dot is not a sys.* event key
	}
	for _, key := range negative {
		if IsSystemKey(key) {
			t.Fatalf("non-reserved key %q must not match", key)
		}
	}
}

func TestLoadSystemCatalogEmbedded(t *testing.T) {
	catalog, err := LoadSystemCatalog()
	if err != nil {
		t.Fatal(err)
	}
	wantKeys := []string{"sys.identity_link", "sys.operation_failed", "sys.page_scroll", "sys.page_view", "sys.session_end"}
	if got := catalog.Keys(); !reflect.DeepEqual(got, wantKeys) {
		t.Fatalf("keys = %v, want %v", got, wantKeys)
	}
	for _, key := range wantKeys {
		rule, schema, ok := catalog.Rule(key)
		if !ok || schema == nil {
			t.Fatalf("rule %q missing or uncompiled", key)
		}
		if rule.Key != key || rule.Version != 1 {
			t.Fatalf("rule %q has unexpected shape %+v", key, rule)
		}
		if len(rule.AllowedSourceKinds) != 1 || rule.AllowedSourceKinds[0] != SourceKindBrowser {
			t.Fatalf("rule %q must stay browser-only, got %v", key, rule.AllowedSourceKinds)
		}
	}
}

func TestSystemCatalogPropertySchemaValidation(t *testing.T) {
	catalog, err := LoadSystemCatalog()
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		Events []struct {
			Key      string         `json:"event_key"`
			Example  map[string]any `json:"example_properties"`
			Schema   map[string]any `json:"schema"`
			Required []string       `json:"schema.required"`
		} `json:"events"`
	}
	if err = json.Unmarshal(wire.SystemEvents, &fixture); err != nil {
		t.Fatal(err)
	}
	if len(fixture.Events) != 5 {
		t.Fatalf("want 5 embedded events, got %d", len(fixture.Events))
	}
	for _, event := range fixture.Events {
		_, schema, ok := catalog.Rule(event.Key)
		if !ok {
			t.Fatalf("missing rule for %s", event.Key)
		}
		// The shipped example properties must always validate against the compiled schema.
		if err := schema.Validate(event.Example); err != nil {
			t.Fatalf("%s example properties rejected: %v", event.Key, err)
		}
		// Every system event forbids additional properties.
		if err := schema.Validate(map[string]any{"unexpected_field": 1}); err == nil {
			t.Fatalf("%s accepted an undeclared property", event.Key)
		}
	}

	_, scrollSchema, ok := catalog.Rule("sys.page_scroll")
	if !ok {
		t.Fatal("page_scroll rule missing")
	}
	// A required-field omission must fail closed rather than coerce to zero values.
	missingRequired := map[string]any{"from_ratio": 0, "to_ratio": 0.6, "max_ratio": 0.6, "direction": "down"}
	if err := scrollSchema.Validate(missingRequired); err == nil {
		t.Fatal("page_scroll accepted a payload missing duration_ms")
	}
	// Enum and numeric bounds are enforced by the compiled schema too.
	outOfBounds := map[string]any{
		"from_ratio": 0, "to_ratio": 0.6, "max_ratio": 1.5,
		"direction": "sideways", "duration_ms": 1,
	}
	if err := scrollSchema.Validate(outOfBounds); err == nil {
		t.Fatal("page_scroll accepted an out-of-bounds ratio and an unknown direction")
	}
}

func TestSystemCatalogRuleFailsClosed(t *testing.T) {
	catalog, err := LoadSystemCatalog()
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"sys.unknown", "page_view", "", "sys.PAGE_VIEW"} {
		if rule, schema, ok := catalog.Rule(key); ok || schema != nil || rule.Key != "" {
			t.Fatalf("unknown key %q must fail closed, got %+v", key, rule)
		}
	}
}

func TestLoadSystemCatalogRejectsCorruptFixture(t *testing.T) {
	base := func(events string) []byte {
		return []byte(`{"format":"activity.system-events.v1","events":[` + events + `]}`)
	}
	cases := map[string][]byte{
		"invalid json":      []byte(`{not-json`),
		"unexpected format": []byte(`{"format":"something-else","events":[]}`),
		"no events":         []byte(`{"format":"activity.system-events.v1","events":[]}`),
		"non-reserved key":  base(`{"event_key":"page_view","schema_version":1,"allowed_source_kinds":["browser"],"schema":{"type":"object"}}`),
		"duplicate key": base(
			`{"event_key":"sys.page_view","schema_version":1,"allowed_source_kinds":["browser"],"schema":{"type":"object"}},
			 {"event_key":"sys.page_view","schema_version":1,"allowed_source_kinds":["browser"],"schema":{"type":"object"}}`,
		),
		"zero schema version":  base(`{"event_key":"sys.page_view","schema_version":0,"allowed_source_kinds":["browser"],"schema":{"type":"object"}}`),
		"without source kinds": base(`{"event_key":"sys.page_view","schema_version":1,"allowed_source_kinds":[],"schema":{"type":"object"}}`),
		"without schema":       base(`{"event_key":"sys.page_view","schema_version":1,"allowed_source_kinds":["browser"]}`),
		"uncompilable schema":  base(`{"event_key":"sys.page_view","schema_version":1,"allowed_source_kinds":["browser"],"schema":{"type":"not-a-json-schema-type"}}`),
	}
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := loadSystemCatalog(raw); err == nil {
				t.Fatalf("corrupt fixture %q was accepted", name)
			}
		})
	}
}

func TestEmbeddedSystemCatalogMatchesCanonicalFixture(t *testing.T) {
	sourcePath := filepath.Join("..", "..", "..", "contracts", "fixtures", "system-events.json")
	sourceRaw, err := os.ReadFile(sourcePath)
	if err != nil {
		t.Fatal(err)
	}
	var source, embedded any
	if err = json.Unmarshal(sourceRaw, &source); err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(wire.SystemEvents, &embedded); err != nil {
		t.Fatal(err)
	}
	// The generated copy is re-serialised deterministically, so compare the parsed
	// semantics rather than raw bytes to catch any content drift from codegen.
	if !reflect.DeepEqual(source, embedded) {
		t.Fatal("embedded system catalog diverges from contracts/fixtures/system-events.json")
	}
}
