package archiving

import (
	"encoding/json"
	"sort"
	"testing"

	"github.com/howtomakeaname/Evia/backend/internal/gen/wire"
)

// TestArchiveStatusMatchesContract keeps the archive manifest status closed
// set aligned with the ArchiveManifest.status enum in the embedded bundle.
func TestArchiveStatusMatchesContract(t *testing.T) {
	var bundle struct {
		Defs map[string]map[string]any `json:"$defs"`
	}
	if err := json.Unmarshal(wire.Schemas, &bundle); err != nil {
		t.Fatal(err)
	}
	props, ok := bundle.Defs["ArchiveManifest"]["properties"].(map[string]any)
	if !ok {
		t.Fatal("ArchiveManifest schema missing properties")
	}
	raw, ok := props["status"].(map[string]any)["enum"].([]any)
	if !ok {
		t.Fatal("ArchiveManifest.status enum missing from embedded bundle")
	}
	got := make([]string, 0, len(raw))
	for _, value := range raw {
		got = append(got, value.(string))
	}
	want := []string{StatusPending, StatusMaterialized, StatusFailed}
	sort.Strings(got)
	sort.Strings(want)
	if len(got) != len(want) {
		t.Fatalf("archive status drift: contract %v != Go %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("archive status drift: contract %v != Go %v", got, want)
		}
	}
}
