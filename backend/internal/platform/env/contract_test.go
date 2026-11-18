package env

import (
	"encoding/json"
	"sort"
	"testing"

	"github.com/howtomakeaname/Evia/backend/internal/gen/wire"
)

// TestEnvironmentVocabularyMatchesEveryContractSchema walks the embedded
// OpenAPI bundle and asserts that every schema property carrying the
// deployment environment vocabulary (environment and target_environment)
// publishes exactly the Go closed set. The same triple is repeated across
// roughly forty schemas, so a per-schema enumeration here is the only
// mechanical guarantee that adding a fourth environment cannot desynchronize
// the validators from the published contract.
func TestEnvironmentVocabularyMatchesEveryContractSchema(t *testing.T) {
	var bundle struct {
		Defs map[string]map[string]any `json:"$defs"`
	}
	if err := json.Unmarshal(wire.Schemas, &bundle); err != nil {
		t.Fatal(err)
	}
	want := append([]string(nil), Environments...)
	sort.Strings(want)

	checked := 0
	var walk func(schemaName string, node map[string]any)
	walk = func(schemaName string, node map[string]any) {
		props, ok := node["properties"].(map[string]any)
		if !ok {
			return
		}
		for name, raw := range props {
			if name != "environment" && name != "target_environment" {
				continue
			}
			property, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			values, ok := property["enum"].([]any)
			if !ok {
				continue
			}
			got := make([]string, 0, len(values))
			for _, value := range values {
				text, ok := value.(string)
				if !ok {
					t.Fatalf("%s.%s enum contains non-string value", schemaName, name)
				}
				got = append(got, text)
			}
			sort.Strings(got)
			if len(got) != len(want) {
				t.Fatalf("%s.%s environment drift: contract %v != Go %v", schemaName, name, got, want)
			}
			for i := range want {
				if got[i] != want[i] {
					t.Fatalf("%s.%s environment drift: contract %v != Go %v", schemaName, name, got, want)
				}
			}
			checked++
		}
	}
	for schemaName, schema := range bundle.Defs {
		walk(schemaName, schema)
	}
	if checked < 30 {
		t.Fatalf("expected the environment vocabulary on dozens of schemas, only checked %d", checked)
	}
}
