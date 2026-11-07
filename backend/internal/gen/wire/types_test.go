package wire

import (
	"encoding/json"
	"testing"
)

func TestOptionalContainersPreservePresence(t *testing.T) {
	for _, tc := range []struct {
		name    string
		input   DraftInput
		present bool
	}{
		{"missing", DraftInput{Schema: map[string]any{}}, false},
		{"empty", DraftInput{Schema: map[string]any{}, FieldMetadata: map[string]any{}, ExampleProperties: map[string]any{}, AllowedSourceKinds: []string{}}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw, e := json.Marshal(tc.input)
			if e != nil {
				t.Fatal(e)
			}
			var result map[string]json.RawMessage
			if e = json.Unmarshal(raw, &result); e != nil {
				t.Fatal(e)
			}
			for _, key := range []string{"field_metadata", "example_properties", "allowed_source_kinds"} {
				if _, ok := result[key]; ok != tc.present {
					t.Fatalf("%s presence mismatch: %s", key, raw)
				}
			}
		})
	}
}
