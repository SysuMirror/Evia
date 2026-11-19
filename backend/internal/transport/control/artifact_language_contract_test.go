package control

import (
	"encoding/json"
	"sort"
	"testing"

	"github.com/howtomakeaname/Evia/backend/internal/gen/wire"
)

// TestArtifactLanguageVocabularyContract guards the single-value
// TypeScriptArtifact.language enum against the embedded bundle, so adding an
// artifact language moves Go and contract together.
func TestArtifactLanguageVocabularyContract(t *testing.T) {
	var bundle struct {
		Defs map[string]map[string]any `json:"$defs"`
	}
	if err := json.Unmarshal(wire.Schemas, &bundle); err != nil {
		t.Fatal(err)
	}
	props, ok := bundle.Defs["TypeScriptArtifact"]["properties"].(map[string]any)
	if !ok {
		t.Fatal("TypeScriptArtifact schema missing properties")
	}
	raw, ok := props["language"].(map[string]any)["enum"].([]any)
	if !ok {
		t.Fatal("TypeScriptArtifact.language enum missing")
	}
	got := make([]string, 0, len(raw))
	for _, value := range raw {
		got = append(got, value.(string))
	}
	want := append([]string(nil), artifactLanguages...)
	sort.Strings(got)
	sort.Strings(want)
	if len(got) != len(want) {
		t.Fatalf("artifact language drift: contract %v != Go %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("artifact language drift: contract %v != Go %v", got, want)
		}
	}
}
