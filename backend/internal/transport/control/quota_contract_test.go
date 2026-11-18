package control

import (
	"encoding/json"
	"sort"
	"testing"

	"github.com/howtomakeaname/Evia/backend/internal/gen/wire"
)

// TestCollectionQuotaVocabularyContract keeps the quota source/runtime
// status wire vocabulary aligned with the CollectionQuota component schema
// in the embedded bundle.
func TestCollectionQuotaVocabularyContract(t *testing.T) {
	var bundle struct {
		Defs map[string]map[string]any `json:"$defs"`
	}
	if err := json.Unmarshal(wire.Schemas, &bundle); err != nil {
		t.Fatal(err)
	}
	props, ok := bundle.Defs["CollectionQuota"]["properties"].(map[string]any)
	if !ok {
		t.Fatal("CollectionQuota schema missing properties")
	}
	enumOf := func(property string) []string {
		node, ok := props[property].(map[string]any)
		if !ok {
			t.Fatalf("CollectionQuota.%s missing", property)
		}
		raw, ok := node["enum"].([]any)
		if !ok {
			t.Fatalf("CollectionQuota.%s has no enum", property)
		}
		out := make([]string, 0, len(raw))
		for _, value := range raw {
			out = append(out, value.(string))
		}
		return out
	}
	assertSameSet := func(property string, want, got []string) {
		a := append([]string(nil), want...)
		b := append([]string(nil), got...)
		sort.Strings(a)
		sort.Strings(b)
		if len(a) != len(b) {
			t.Fatalf("%s drift: contract %v != Go %v", property, b, a)
		}
		for i := range a {
			if a[i] != b[i] {
				t.Fatalf("%s drift: contract %v != Go %v", property, b, a)
			}
		}
	}
	assertSameSet("source",
		[]string{quotaSourceDefault, quotaSourceProjectOverride},
		enumOf("source"))
	assertSameSet("runtime_status",
		[]string{quotaRuntimeApplied, quotaRuntimeResetPending},
		enumOf("runtime_status"))
}
