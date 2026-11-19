package catalog

import (
	"encoding/json"
	"testing"

	"github.com/howtomakeaname/Evia/backend/internal/gen/wire"
)

// TestReleaseStateVocabularyContract guards the Go-produced release state
// subset and the Go-observable acceleration state subset against the Release
// enums in the embedded bundle. The unpublished lifecycle states and the
// ready/failed acceleration states are contract-only reserved values, so
// both assertions are subset rather than set equality.
func TestReleaseStateVocabularyContract(t *testing.T) {
	var bundle struct {
		Defs map[string]map[string]any `json:"$defs"`
	}
	if err := json.Unmarshal(wire.Schemas, &bundle); err != nil {
		t.Fatal(err)
	}
	props, ok := bundle.Defs["Release"]["properties"].(map[string]any)
	if !ok {
		t.Fatal("Release schema missing properties")
	}
	assertSubset := func(property string, subset []string) {
		raw, ok := props[property].(map[string]any)["enum"].([]any)
		if !ok {
			t.Fatalf("Release.%s enum missing", property)
		}
		allowed := map[string]bool{}
		for _, value := range raw {
			allowed[value.(string)] = true
		}
		for _, value := range subset {
			if !allowed[value] {
				t.Fatalf("release %s drift: Go value %q absent from contract %v", property, value, raw)
			}
		}
	}
	assertSubset("state", ReleaseStates)
	assertSubset("acceleration_state", ReleaseAccelerationStates)
}
