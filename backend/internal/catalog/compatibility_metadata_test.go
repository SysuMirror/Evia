package catalog

import "testing"

func TestCompatibilityRejectsChangedFieldInterpretation(t *testing.T) {
	before := DraftInput{Schema: testSchema(t, `{"type":"object","additionalProperties":false,"properties":{"duration":{"type":"number"}}}`), FieldMetadata: map[string]any{"duration": map[string]any{"unit": "ms", "label": "Duration"}}}
	after := before
	after.FieldMetadata = map[string]any{"duration": map[string]any{"unit": "s", "label": "Duration"}}
	if got := Compatibility(&before, after); got != CompatibilityBreaking {
		t.Fatal("same-schema unit change was accepted", got)
	}
	after.FieldMetadata = map[string]any{"duration": map[string]any{"unit": "ms", "label": "Renamed duration"}}
	if got := Compatibility(&before, after); got != CompatibilityBreaking {
		t.Fatal("untyped metadata change was not conservative", got)
	}
	after.FieldMetadata = nil
	if got := Compatibility(&before, after); got != CompatibilityBreaking {
		t.Fatal("removed interpretation metadata was accepted", got)
	}
}
func TestCompatibilityMetadataCanonicalOrdering(t *testing.T) {
	schema := testSchema(t, `{"type":"object","additionalProperties":false,"properties":{"duration":{"type":"number"},"count":{"type":"integer"}}}`)
	before := DraftInput{Schema: schema, FieldMetadata: map[string]any{"duration": map[string]any{"unit": "ms", "label": "Duration"}, "count": map[string]any{"unit": "items"}}}
	after := DraftInput{Schema: schema, FieldMetadata: map[string]any{}}
	after.FieldMetadata["count"] = map[string]any{"unit": "items"}
	after.FieldMetadata["duration"] = map[string]any{"label": "Duration", "unit": "ms"}
	if got := Compatibility(&before, after); got != CompatibilityCompatible {
		t.Fatal("metadata key order changed interpretation", got)
	}
}
