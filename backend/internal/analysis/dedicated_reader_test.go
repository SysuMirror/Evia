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

func TestCompileWithCatalogDefaultsByteIdenticalAndEnablesGate(t *testing.T) {
	r := request(t, baseRequest)
	r.Filter = &Filter{Op: "eq", Field: &Field{Kind: "property", EventKey: "page_opened", SchemaVersion: 1, Key: "title", ResolvedType: "string"}, Value: "Home"}
	r.Metrics = []Metric{{Op: OpCount, Accuracy: AccuracyExact}}
	scope := Scope{ProjectID: "project-secret", Environment: "development"}

	legacy, err := Compile("testdb", "event_facts", scope, r)
	if err != nil {
		t.Fatal(err)
	}
	nilCatalog, err := CompileWithCatalog("testdb", "event_facts", scope, r, nil)
	if err != nil {
		t.Fatal(err)
	}
	if legacy.SQL != nilCatalog.SQL {
		t.Fatal("nil catalog must compile byte-identical SQL to Compile")
	}

	enabled, err := CompileWithCatalog("testdb", "event_facts", scope, r, readerCatalog(t))
	if err != nil {
		t.Fatal(err)
	}
	column := propertycatalog.SlotColumn("title")
	if !strings.Contains(enabled.SQL, "dedicated_layout_version=6") || !strings.Contains(enabled.SQL, column) {
		t.Fatalf("catalog-enabled plan missing dedicated gate:\n%s", enabled.SQL)
	}
	if legacy.SQL == enabled.SQL {
		t.Fatal("enabled catalog must change the SQL when the field matches an entry")
	}
	// A fully covered request prunes the token map from the winner tuple and
	// keeps the authoritative document as the fallback tier.
	if strings.Contains(enabled.SQL, "property_tokens") {
		t.Fatalf("covered request must prune the token map:\n%s", enabled.SQL)
	}
	if !strings.Contains(enabled.SQL, "properties_json") {
		t.Fatalf("document fallback lost in catalog-enabled plan:\n%s", enabled.SQL)
	}
}

func TestCompileWithCatalogUnmatchedFieldStaysIdentical(t *testing.T) {
	r := request(t, baseRequest)
	r.Filter = &Filter{Op: "eq", Field: &Field{Kind: "property", EventKey: "other_event", SchemaVersion: 1, Key: "title", ResolvedType: "string"}, Value: "Home"}
	r.Metrics = []Metric{{Op: OpCount, Accuracy: AccuracyExact}}
	scope := Scope{ProjectID: "project-secret", Environment: "development"}

	legacy, err := Compile("testdb", "event_facts", scope, r)
	if err != nil {
		t.Fatal(err)
	}
	enabled, err := CompileWithCatalog("testdb", "event_facts", scope, r, readerCatalog(t))
	if err != nil {
		t.Fatal(err)
	}
	if legacy.SQL != enabled.SQL {
		t.Fatalf("unmatched field must keep SQL byte-identical:\n%s\n%s", legacy.SQL, enabled.SQL)
	}
}

func TestTrendTupleAppendsDedicatedColumnsOnlyOnCatalogHit(t *testing.T) {
	column := propertycatalog.SlotColumn("title")
	scope := Scope{ProjectID: "project-secret", Environment: "development"}
	trendWithTitle := func() Request {
		r := request(t, baseRequest) // baseRequest is a trend query
		r.EventVersions = []EventVersion{{EventKey: "page_opened", SchemaVersion: 1}}
		r.Filter = &Filter{Op: "eq", Field: &Field{Kind: "property", EventKey: "page_opened", SchemaVersion: 1, Key: "title", ResolvedType: "string"}, Value: "Home"}
		r.Metrics = []Metric{{Op: OpCount, Accuracy: AccuracyExact}}
		return r
	}

	// Default and nil-catalog plans must be byte-identical and never reference
	// the marker or slot column.
	legacy, err := Compile("testdb", "event_facts", scope, trendWithTitle())
	if err != nil {
		t.Fatal(err)
	}
	nilPlan, err := CompileWithCatalog("testdb", "event_facts", scope, trendWithTitle(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if legacy.SQL != nilPlan.SQL {
		t.Fatal("nil catalog must keep trend SQL byte-identical")
	}
	if strings.Contains(legacy.SQL, "dedicated_layout_version") || strings.Contains(legacy.SQL, column) {
		t.Fatalf("default trend tuple must not carry dedicated columns: %s", legacy.SQL)
	}

	enabled, err := CompileWithCatalog("testdb", "event_facts", scope, trendWithTitle(), readerCatalog(t))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(enabled.SQL, "winner.") || !strings.Contains(enabled.SQL, "dedicated_layout_version") {
		t.Fatalf("enabled trend tuple missing marker projection: %s", enabled.SQL)
	}
	if !strings.Contains(enabled.SQL, column) {
		t.Fatalf("enabled trend tuple missing slot column %s: %s", column, enabled.SQL)
	}
	// The marker must travel inside the argMin tuple, not only the outer filter.
	if !strings.Contains(enabled.SQL, "argMin(tuple(") {
		t.Fatalf("expected argMin tuple in trend source: %s", enabled.SQL)
	}
}

func TestTrendTupleIgnoresNonCatalogProperty(t *testing.T) {
	scope := Scope{ProjectID: "project-secret", Environment: "development"}
	r := request(t, baseRequest)
	r.EventVersions = []EventVersion{{EventKey: "other_event", SchemaVersion: 1}}
	r.Filter = &Filter{Op: "eq", Field: &Field{Kind: "property", EventKey: "other_event", SchemaVersion: 1, Key: "title", ResolvedType: "string"}, Value: "Home"}
	r.Metrics = []Metric{{Op: OpCount, Accuracy: AccuracyExact}}

	legacy, err := Compile("testdb", "event_facts", scope, r)
	if err != nil {
		t.Fatal(err)
	}
	enabled, err := CompileWithCatalog("testdb", "event_facts", scope, r, readerCatalog(t))
	if err != nil {
		t.Fatal(err)
	}
	if legacy.SQL != enabled.SQL {
		t.Fatalf("non-catalog property must keep trend SQL byte-identical:\n%s\n%s", legacy.SQL, enabled.SQL)
	}
}
