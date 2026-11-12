package analysis

import (
	"strings"
	"testing"

	"github.com/howtomakeaname/Evia/backend/internal/propertycatalog"
	"github.com/howtomakeaname/Evia/backend/internal/propertylayout"
)

func readerCatalog(t *testing.T) *propertycatalog.Catalog {
	t.Helper()
	catalog := &propertycatalog.Catalog{Revision: 6, Entries: []propertycatalog.Entry{
		{EventKey: "page_opened", SchemaVersion: 1, PropertyKey: "title", ResolvedType: "string"},
	}}
	if err := catalog.Validate(); err != nil {
		t.Fatal(err)
	}
	return catalog
}

func titleField() Field {
	return Field{Kind: "property", EventKey: "page_opened", SchemaVersion: 1, Key: "title", ResolvedType: "string"}
}

func TestSelectDedicatedDisabledAndNil(t *testing.T) {
	field := titleField()
	if _, ok := selectDedicated(nil, field); ok {
		t.Fatal("nil catalog must not select a dedicated path")
	}
	if _, ok := selectDedicated(propertycatalog.Disabled(), field); ok {
		t.Fatal("disabled catalog must not select a dedicated path")
	}
	doc, key, applied := applyDedicatedTier(propertycatalog.Disabled(), field, "DOC", "KEY")
	if applied || doc != "DOC" || key != "KEY" {
		t.Fatalf("disabled tier must return inputs unchanged, got doc=%q key=%q applied=%v", doc, key, applied)
	}
}

func TestSelectDedicatedBuildsRevisionAndColumnGate(t *testing.T) {
	selection, ok := selectDedicated(readerCatalog(t), titleField())
	if !ok {
		t.Fatal("catalog entry must select the dedicated path")
	}
	column := propertycatalog.SlotColumn("title")
	if selection.column != column || selection.revision != 6 {
		t.Fatalf("selection = %+v, want column %s revision 6", selection, column)
	}
	wantHit := "(dedicated_layout_version=6 AND " + column + "!='')"
	if selection.hit != wantHit {
		t.Fatalf("hit = %q, want %q", selection.hit, wantHit)
	}
	if selection.document != "concat('{\"f\":',"+column+",'}')" {
		t.Fatalf("unexpected synthetic document: %q", selection.document)
	}
}

func TestSelectDedicatedRequiresNonEmptyColumn(t *testing.T) {
	selection, _ := selectDedicated(readerCatalog(t), titleField())
	// The !='' guard is what separates an absent property (zero-byte default)
	// from a materialized token; without it the reader would build invalid JSON
	// for rows that never had the property.
	if !strings.Contains(selection.hit, "!=''") {
		t.Fatalf("dedicated gate must reject the zero-byte default: %q", selection.hit)
	}
}

func TestSelectDedicatedMissesUnknownFieldAndIdentity(t *testing.T) {
	catalog := readerCatalog(t)
	misses := []Field{
		{Kind: "property", EventKey: "page_opened", SchemaVersion: 1, Key: "other"},
		{Kind: "property", EventKey: "page_opened", SchemaVersion: 2, Key: "title"},
		{Kind: "property", EventKey: "other_event", SchemaVersion: 1, Key: "title"},
	}
	for _, field := range misses {
		if _, ok := selectDedicated(catalog, field); ok {
			t.Fatalf("field %+v must not match the catalog entry", field)
		}
	}
}

func TestApplyDedicatedTierNestsAroundMapFallback(t *testing.T) {
	catalog := readerCatalog(t)
	column := propertycatalog.SlotColumn("title")
	// Stand in for the existing map-or-document expressions produced by field().
	fallbackDoc := "if((property_layout_version=1 AND mapContains(property_tokens,{p0:String})),concat('{\"f\":',property_tokens[{p0:String}],'}'),properties_json)"
	fallbackKey := "if((property_layout_version=1 AND mapContains(property_tokens,{p0:String})),'f',{p1:String})"

	doc, key, applied := applyDedicatedTier(catalog, titleField(), fallbackDoc, fallbackKey)
	if !applied {
		t.Fatal("tier must apply for a catalog hit")
	}
	// Outer dedicated branch first, then the untouched map fallback.
	if !strings.HasPrefix(doc, "if((dedicated_layout_version=6 AND "+column+"!=''),concat('{\"f\":',"+column+",'}'),") {
		t.Fatalf("dedicated branch must wrap the fallback first: %s", doc)
	}
	if !strings.Contains(doc, fallbackDoc) {
		t.Fatalf("map/document fallback must be preserved inside the dedicated branch: %s", doc)
	}
	if !strings.HasPrefix(key, "if((dedicated_layout_version=6 AND "+column+"!=''),'f',") {
		t.Fatalf("key expression must choose 'f' only on a dedicated hit: %s", key)
	}
	if !strings.Contains(key, fallbackKey) {
		t.Fatalf("fallback key expression must be preserved: %s", key)
	}
}

func TestDedicatedColumnUsesEncodedIdentifierNotUserText(t *testing.T) {
	selection, _ := selectDedicated(readerCatalog(t), titleField())
	// The raw user key must never appear as a physical column; only its encoded
	// form is spliced into SQL, and bound parameters stay on the fallback path.
	if strings.Contains(selection.column, "title") {
		t.Fatalf("physical column must be encoded, got %q", selection.column)
	}
	if selection.column != propertylayout.EncodeKey("title") {
		t.Fatalf("column %q diverges from the shared encoding", selection.column)
	}
}

func TestPlannerFieldEmitsDedicatedTierOnlyWhenCatalogInjected(t *testing.T) {
	column := propertycatalog.SlotColumn("title")

	// Default planner: nil catalog keeps the map/document shape with no
	// dedicated marker or column reference anywhere in the expression.
	defaultPlanner := planner{params: map[string]string{}}
	defaultSQL, err := defaultPlanner.field(titleField())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(defaultSQL.value, "dedicated_layout_version") || strings.Contains(defaultSQL.value, column) {
		t.Fatalf("default planner must not reference dedicated columns: %s", defaultSQL.value)
	}

	// Catalog-enabled planner: the outer dedicated branch wraps the same
	// fallback, and the map/document path remains inside it.
	enabledPlanner := planner{params: map[string]string{}, catalog: readerCatalog(t)}
	enabledSQL, err := enabledPlanner.field(titleField())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(enabledSQL.value, "dedicated_layout_version=6") {
		t.Fatalf("enabled planner missing revision gate: %s", enabledSQL.value)
	}
	if !strings.Contains(enabledSQL.value, column) {
		t.Fatalf("enabled planner missing encoded slot column: %s", enabledSQL.value)
	}
	if !strings.Contains(enabledSQL.value, "property_layout_version=1") {
		t.Fatalf("map fallback must be retained inside the dedicated branch: %s", enabledSQL.value)
	}
}
