package catalog

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/howtomakeaname/Evia/backend/internal/ingestion"
)

func testSchema(t *testing.T, content string) map[string]any {
	t.Helper()
	var out map[string]any
	if e := json.Unmarshal([]byte(content), &out); e != nil {
		t.Fatal(e)
	}
	return out
}

const validSchema = `{"type":"object","additionalProperties":false,"properties":{"lesson_id":{"type":"integer","minimum":1},"labels":{"type":"array","maxItems":3,"items":{"type":"string","maxLength":20}},"finished":{"type":"boolean"}},"required":["lesson_id"]}`

func TestBoundedSchemaAndSample(t *testing.T) {
	schema := testSchema(t, validSchema)
	if issues := CheckSchema(schema); len(issues) > 0 {
		t.Fatal(issues)
	}
	for _, tc := range []struct{ name, sample, path, code string }{
		{"valid", `{"lesson_id":42,"labels":["a","b"],"finished":true}`, "", ""},
		{"required", `{}`, "/lesson_id", "REQUIRED"},
		{"unknown", `{"lesson_id":1,"secret":true}`, "/secret", "UNKNOWN_PROPERTY"},
		{"unsafe", `{"lesson_id":9007199254740992}`, "/lesson_id", "UNSAFE_NUMBER"},
		{"fraction", `{"lesson_id":1.5}`, "/lesson_id", "UNSAFE_NUMBER"},
		{"heterogeneous", `{"lesson_id":1,"labels":["ok",false]}`, "/labels/1", "TYPE"},
		{"nested", `{"lesson_id":{}}`, "/lesson_id", "TYPE"},
		{"null", `{"lesson_id":null}`, "/lesson_id", "TYPE"},
		{"too many items", `{"lesson_id":1,"labels":["a","b","c","d"]}`, "/labels", "ARRAY_LENGTH"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			issues := CheckSample(schema, testSchema(t, tc.sample))
			if tc.code == "" {
				if len(issues) > 0 {
					t.Fatal(issues)
				}
				return
			}
			for _, i := range issues {
				if i.Path == tc.path && i.Code == tc.code {
					return
				}
			}
			t.Fatalf("missing %s at %s: %+v", tc.code, tc.path, issues)
		})
	}
}
func TestUnsupportedAndUnboundedSchema(t *testing.T) {
	for _, tc := range []struct{ name, schema, path string }{
		{"remote ref", `{"type":"object","additionalProperties":false,"properties":{},"$ref":"https://private.invalid/schema"}`, "/schema/$ref"},
		{"combinator", `{"type":"object","additionalProperties":false,"properties":{"a":{"oneOf":[{"type":"string"}]}}}`, "/schema/properties/a/oneOf"},
		{"open object", `{"type":"object","properties":{}}`, "/schema/additionalProperties"},
		{"nested object", `{"type":"object","additionalProperties":false,"properties":{"a":{"type":"object","properties":{}}}}`, "/schema/properties/a/type"},
		{"nested array", `{"type":"object","additionalProperties":false,"properties":{"a":{"type":"array","items":{"type":"array","items":{"type":"string"}}}}}`, "/schema/properties/a/items/type"},
		{"unsafe bound", `{"type":"object","additionalProperties":false,"properties":{"a":{"type":"integer","maximum":9007199254740992}}}`, "/schema/properties/a/maximum"},
		{"array bound", `{"type":"object","additionalProperties":false,"properties":{"a":{"type":"array","maxItems":21,"items":{"type":"string"}}}}`, "/schema/properties/a/maxItems"},
		{"required typo", `{"type":"object","additionalProperties":false,"properties":{},"required":["missing"]}`, "/schema/required"},
		{"invalid pattern", `{"type":"object","additionalProperties":false,"properties":{"a":{"type":"string","pattern":"["}}}`, "/schema/properties/a/pattern"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			issues := CheckSchema(testSchema(t, tc.schema))
			for _, i := range issues {
				if i.Path == tc.path {
					return
				}
			}
			t.Fatalf("missing %s: %+v", tc.path, issues)
		})
	}
}
func TestCompatibilityClassification(t *testing.T) {
	before := DraftInput{Schema: testSchema(t, validSchema), AllowedSourceKinds: []string{ingestion.SourceKindBrowser, ingestion.SourceKindServer}, FactKind: str("user_intent")}
	if got := Compatibility(nil, before); got != "initial" {
		t.Fatal(got)
	}
	for _, tc := range []struct{ name, from, to, want string }{
		{"same", `"minimum":1`, `"minimum":1`, "compatible"},
		{"relax", `"minimum":1`, `"minimum":0`, "compatible"},
		{"tighten", `"minimum":1`, `"minimum":2`, "breaking"},
		{"new required", `"required":["lesson_id"]`, `"required":["lesson_id","labels"]`, "breaking"},
		{"remove required", `"required":["lesson_id"]`, `"required":[]`, "compatible"},
		{"change type", `"type":"integer"`, `"type":"number"`, "compatible"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			next := before
			next.Schema = testSchema(t, strings.Replace(validSchema, tc.from, tc.to, 1))
			if got := Compatibility(&before, next); got != tc.want {
				t.Fatal(got, tc.want)
			}
		})
	}
	next := before
	next.AllowedSourceKinds = []string{ingestion.SourceKindServer}
	if Compatibility(&before, next) != "breaking" {
		t.Fatal("source removal compatible")
	}
	next = before
	next.FactKind = str("server_confirmed")
	if Compatibility(&before, next) != "breaking" {
		t.Fatal("semantic change compatible")
	}
}
func TestCanonicalJSONHash(t *testing.T) {
	first := map[string]any{"z": json.Number("1.0"), "a": map[string]any{"b": true}}
	second := map[string]any{"a": map[string]any{"b": true}, "z": 1}
	a, e := Hash(first)
	if e != nil {
		t.Fatal(e)
	}
	b, e := Hash(second)
	if e != nil || a != b {
		t.Fatal("equivalent JSON differs", a, b, e)
	}
	if _, e := Hash(map[string]any{"n": json.Number("9007199254740992")}); e == nil {
		t.Fatal("unsafe canonical number accepted")
	}
}

func TestDraftOptionalValueRoundTrip(t *testing.T) {
	for _, raw := range []string{
		`{"schema":{}}`,
		`{"schema":{},"field_metadata":{},"example_properties":{},"allowed_source_kinds":[],"display_name":"","trigger":""}`,
	} {
		var definition DraftInput
		if e := json.Unmarshal([]byte(raw), &definition); e != nil {
			t.Fatal(e)
		}
		encoded, e := json.Marshal(definition)
		if e != nil {
			t.Fatal(e)
		}
		var roundTrip map[string]any
		if e = json.Unmarshal(encoded, &roundTrip); e != nil {
			t.Fatal(e)
		}
		var expected map[string]any
		if e = json.Unmarshal([]byte(raw), &expected); e != nil {
			t.Fatal(e)
		}
		want, e := Hash(expected)
		if e != nil {
			t.Fatal(e)
		}
		got, e := Hash(roundTrip)
		if e != nil || want != got {
			t.Fatalf("optional values changed: %s -> %s (%v)", raw, encoded, e)
		}
	}
}

func TestPublicationCompletenessAndMetadataLimits(t *testing.T) {
	base := DraftInput{Schema: testSchema(t, validSchema)}
	if issues := CheckDefinition(base, false); len(issues) != 0 {
		t.Fatal("incomplete draft rejected", issues)
	}
	issues := CheckDefinition(base, true)
	paths := map[string]bool{}
	for _, i := range issues {
		paths[i.Path] = true
	}
	for _, p := range []string{"/display_name", "/trigger", "/allowed_source_kinds", "/fact_kind", "/example_properties/lesson_id"} {
		if !paths[p] {
			t.Fatal("publication accepted incomplete field", p, issues)
		}
	}
	base.FieldMetadata = map[string]any{"lesson_id": map[string]any{"value": float64(MaxInteger) + 1}}
	if issues := CheckDefinition(base, false); len(issues) == 0 {
		t.Fatal("unsafe metadata number accepted")
	}
	schema := testSchema(t, validSchema)
	schema["$schema"] = "https://json-schema.org/draft/2020-12/schema"
	if issues := CheckSchema(schema); len(issues) != 0 {
		t.Fatal("local dialect marker rejected", issues)
	}
	schema["$schema"] = "https://private.invalid/schema"
	if issues := CheckSchema(schema); len(issues) == 0 {
		t.Fatal("unsupported dialect accepted")
	}
	if issues := CheckSample(testSchema(t, validSchema), map[string]any{"lesson_id": 1, "labels": []any{strings.Repeat("a", 1025)}}); len(issues) == 0 {
		t.Fatal("unbounded sample string accepted")
	}
}

type noCallStore struct{ Store }

func TestServiceRejectsInvalidInputsBeforePersistence(t *testing.T) {
	svc := New(noCallStore{})
	ctx := context.Background()
	if _, e := svc.CreateEvent(ctx, "a", "p", EventInput{Key: "sys.internal", DisplayName: "Example"}); e == nil {
		t.Fatal("reserved key accepted")
	}
	if _, e := svc.SaveDraft(ctx, "a", "p", "event", 0, DraftInput{}); e == nil {
		t.Fatal("zero revision accepted")
	}
	if _, e := svc.SaveDraft(ctx, "a", "p", "event", 1, DraftInput{Schema: map[string]any{}}); e == nil {
		t.Fatal("invalid schema accepted")
	}
	if _, e := svc.ListEvents(ctx, "a", "p", "", "", 201); e == nil {
		t.Fatal("unbounded page accepted")
	}
	if _, e := svc.ListReleases(ctx, "a", "p", "other", "", 10); e == nil {
		t.Fatal("unknown environment accepted")
	}
	input := ReleaseInput{Environment: "production", Events: []ReleaseEvent{{Key: "lesson", DraftRevision: 1}, {Key: "lesson", DraftRevision: 1}}}
	if _, e := svc.CreateRelease(ctx, "a", "p", "unique", input); e == nil {
		t.Fatal("duplicate release selection accepted")
	}
	input.Events = input.Events[:1]
	if _, e := svc.CreateRelease(ctx, "a", "p", "\n", input); e == nil {
		t.Fatal("invalid key accepted")
	}
}

func str(s string) *string { return &s }
