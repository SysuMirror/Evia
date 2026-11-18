package reporting

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/howtomakeaname/Evia/backend/internal/analysis"
	"github.com/howtomakeaname/Evia/backend/internal/platform/contract"
	"github.com/howtomakeaname/Evia/backend/internal/platform/fault"
)

type discoveryStoreFake struct {
	calls int
	err   error
}

func (f *discoveryStoreFake) AuthorizeAnalysisDiscovery(context.Context, string, string) error {
	f.calls++
	return f.err
}
func (f *discoveryStoreFake) ListAnalysisCatalog(context.Context, string, string, string, string, int64, int) (CatalogSnapshot, error) {
	f.calls++
	return CatalogSnapshot{}, f.err
}
func (f *discoveryStoreFake) ValidateAnalysisQuery(_ context.Context, _, _ string, r analysis.Request) (ResolvedValidation, error) {
	f.calls++
	_ = analysis.WalkFields(&r, func(f *analysis.Field) error {
		if f.Kind == "property" {
			f.ResolvedType = "integer"
			if f.Key == "enabled" {
				f.ResolvedType = "boolean"
			}
		}
		return nil
	})
	return ResolvedValidation{Request: r, Dependencies: []Dependency{}}, f.err
}
func discoveryForTest(t *testing.T, f *discoveryStoreFake) *Discovery {
	t.Helper()
	v, e := contract.New()
	if e != nil {
		t.Fatal(e)
	}
	return NewDiscovery(f, v, false)
}

const discoveryBody = `{"kind":"trend","environment":"development","time_range":{"from":"2026-11-07T10:00:00Z","to":"2026-11-07T11:00:00Z"},"timezone":"UTC"}`

func TestDryValidationWirePreservesScalarsAndNeverExposesResolvedTypes(t *testing.T) {
	f := &discoveryStoreFake{}
	s := discoveryForTest(t, f)
	for _, value := range []string{"false", "0"} {
		kind, key := "property", "enabled"
		if value == "0" {
			key = "duration"
		}
		field := `{"kind":"` + kind + `","key":"` + key + `"}`
		if kind == "property" {
			field = `{"kind":"property","key":"` + key + `","event_key":"page_opened","schema_version":1}`
		}
		// Fake resolution supplies integer only for property references. The real
		// resolver's type checks are exercised by PostgreSQL integration tests.
		raw := strings.TrimSuffix(discoveryBody, "}") + `,"event_keys":["page_opened"],"filter":{"op":"eq","field":` + field + `,"value":` + value + `}}`
		out, e := s.ValidateQuery(context.Background(), "actor", "project", json.RawMessage(raw))
		if e != nil {
			t.Fatal(e)
		}
		if !out.Valid || out.ExecutionPerformed || out.ExecutionAvailable || strings.Contains(string(out.NormalizedRequest), "resolved_type") {
			t.Fatal(out)
		}
		if !strings.Contains(string(out.NormalizedRequest), `"value":`+value) {
			t.Fatalf("scalar lost from public request: %s", out.NormalizedRequest)
		}
		if out.Dependencies.SelectionMode != "event_keys" || len(out.Dependencies.UnfixedEventKeys) != 1 || len(out.Dependencies.EventVersions) != 0 {
			t.Fatal("unfixed scope was presented as pinned", out.Dependencies)
		}
	}
}
func TestValidationRejectsInvalidInputBeforePersistenceAndHashesScope(t *testing.T) {
	f := &discoveryStoreFake{}
	s := discoveryForTest(t, f)
	for _, raw := range []string{
		strings.TrimSuffix(discoveryBody, "}") + `,"filter":{"op":"eq","field":{"kind":"system","key":"uid"},"value":null}}`,
		strings.TrimSuffix(discoveryBody, "}") + `,"filter":{"op":"eq","field":{"kind":"property","key":"nested.path","event_key":"page_opened","schema_version":1},"value":1}}`,
		strings.TrimSuffix(discoveryBody, "}") + `,"filter":{"op":"eq","field":{"kind":"system","key":"uid","resolved_type":"string"},"value":"x"}}`,
	} {
		if _, e := s.ValidateQuery(context.Background(), "actor", "project", json.RawMessage(raw)); e == nil {
			t.Fatal("invalid public request accepted")
		}
	}
	if f.calls != 0 {
		t.Fatal("invalid input reached persistence")
	}
	a, e := s.ValidateQuery(context.Background(), "actor", "project", json.RawMessage(discoveryBody))
	if e != nil {
		t.Fatal(e)
	}
	b, e := s.ValidateQuery(context.Background(), "actor", "other-project", json.RawMessage(discoveryBody))
	if e != nil {
		t.Fatal(e)
	}
	if a.QueryHash == b.QueryHash || a.DependencyHash == b.DependencyHash {
		t.Fatal("hash escaped project scope")
	}
	c, e := s.ValidateQuery(context.Background(), "actor", "project", json.RawMessage(discoveryBody))
	if e != nil || a.QueryHash != c.QueryHash || a.DependencyHash != c.DependencyHash {
		t.Fatal("normalization is unstable", e)
	}
	f.err = fault.New(fault.NotFound, "PROJECT_NOT_FOUND", "项目不可见")
	if _, e = s.Capabilities(context.Background(), "actor", "project"); e == nil {
		t.Fatal("unauthorized discovery succeeded")
	}
}
func TestCapabilitiesAdvertiseRetentionSurface(t *testing.T) {
	s := discoveryForTest(t, &discoveryStoreFake{})
	capabilities, e := s.Capabilities(context.Background(), "actor", "project")
	if e != nil {
		t.Fatal(e)
	}
	hasRetention, hasSegment, hasPath, hasAttribution := false, false, false, false
	for _, kind := range capabilities.QueryKinds {
		if kind == "retention" {
			hasRetention = true
		}
		if kind == "segment" {
			hasSegment = true
		}
		if kind == "path" {
			hasPath = true
		}
		if kind == "attribution" {
			hasAttribution = true
		}
	}
	if !hasRetention {
		t.Fatalf("retention must be advertised in query kinds: %v", capabilities.QueryKinds)
	}
	if !hasSegment {
		t.Fatalf("segment must be advertised in query kinds: %v", capabilities.QueryKinds)
	}
	if !hasPath {
		t.Fatalf("path must be advertised in query kinds: %v", capabilities.QueryKinds)
	}
	if !hasAttribution {
		t.Fatalf("attribution must be advertised in query kinds: %v", capabilities.QueryKinds)
	}
	wantGranularities := []string{"day", "week", "month"}
	if len(capabilities.RetentionGranularities) != len(wantGranularities) {
		t.Fatalf("retention granularities wrong: %v", capabilities.RetentionGranularities)
	}
	for i, want := range wantGranularities {
		if capabilities.RetentionGranularities[i] != want {
			t.Fatalf("retention granularities wrong: %v", capabilities.RetentionGranularities)
		}
	}
	if capabilities.Budgets.RetentionMaxPeriods != 12 || capabilities.Budgets.RetentionMaxWindowSeconds != 372*86400 {
		t.Fatalf("retention budgets wrong: %+v", capabilities.Budgets)
	}
}

func TestCatalogProjectsOnlyQueryableTopLevelTypedFields(t *testing.T) {
	definition := PublishedDefinition{Event: CatalogEvent{EventKey: "page_opened", SchemaVersion: 2}, Definition: map[string]any{"schema": map[string]any{"properties": map[string]any{
		"count": map[string]any{"type": "integer"}, "enabled": map[string]any{"type": "boolean"}, "tags": map[string]any{"type": "array", "items": map[string]any{"type": "string"}}, "nullable": map[string]any{"type": []any{"string", "null"}}, "object": map[string]any{"type": "object"}, "any_array": map[string]any{"type": "array"}, "nested.path": map[string]any{"type": "string"}, "date": map[string]any{"type": "string", "format": "date-time"}}, "required": []any{"count"}}, "field_metadata": map[string]any{"count": map[string]any{"description": "Synthetic count"}}}}
	fields := ProjectFields(definition)
	if len(fields) != 4 {
		t.Fatalf("unexpected supported projection: %+v", fields)
	}
	// count, date, enabled, tags are supported; nullable and objects are absent.
	seen := map[string]CatalogField{}
	for _, f := range fields {
		seen[f.Field.Key] = f
		if f.Nullable || f.Field.ResolvedType != "" {
			t.Fatal(f)
		}
	}
	if !seen["count"].Required || seen["enabled"].Required || seen["tags"].Groupable || seen["date"].Type != "string" {
		t.Fatal(fields)
	}
	if seen["count"].Metadata["description"] != "Synthetic count" || seen["enabled"].MissingSemantics != "absent_key" || seen["enabled"].NullSemantics != "present_but_not_a_typed_value" {
		t.Fatal(fields)
	}
}

func TestFieldCapabilitiesAdvertiseNegationOperators(t *testing.T) {
	hasOp := func(ops []string, want string) bool {
		for _, op := range ops {
			if op == want {
				return true
			}
		}
		return false
	}
	stringCaps, ok := FieldCapabilities("string")
	if !ok {
		t.Fatal("string capabilities missing")
	}
	for _, op := range []string{"not_in", "not_prefix", "not_contains", "suffix", "not_suffix"} {
		if !hasOp(stringCaps.FilterOperators, op) {
			t.Fatalf("string capabilities must advertise %s: %v", op, stringCaps.FilterOperators)
		}
	}
	numberCaps, _ := FieldCapabilities("number")
	if !hasOp(numberCaps.FilterOperators, "not_in") || hasOp(numberCaps.FilterOperators, "not_prefix") || hasOp(numberCaps.FilterOperators, "suffix") {
		t.Fatalf("number capabilities negation set wrong: %v", numberCaps.FilterOperators)
	}
	arrayCaps, _ := FieldCapabilities("array:string")
	if !hasOp(arrayCaps.FilterOperators, "not_contains") || hasOp(arrayCaps.FilterOperators, "not_in") {
		t.Fatalf("array capabilities negation set wrong: %v", arrayCaps.FilterOperators)
	}
}

func TestCapabilitiesAdvertiseWeekInterval(t *testing.T) {
	s := discoveryForTest(t, &discoveryStoreFake{})
	capabilities, e := s.Capabilities(context.Background(), "actor", "project")
	if e != nil {
		t.Fatal(e)
	}
	want := map[string]bool{"minute": false, "hour": false, "day": false, "week": false}
	for _, interval := range capabilities.Intervals {
		if _, known := want[interval]; known {
			want[interval] = true
		}
	}
	for interval, found := range want {
		if !found {
			t.Fatalf("capabilities intervals missing %s: %v", interval, capabilities.Intervals)
		}
	}
}
