package access

import (
	"encoding/json"
	"sort"
	"testing"

	"github.com/howtomakeaname/Evia/backend/internal/gen/wire"
)

// TestProjectRoleContractStaysAligned keeps the Go project role closed set
// aligned with every ProjectMember* schema enum in the embedded bundle, so a
// role added on only one side fails the build rather than failing at the HTTP
// boundary.
func TestProjectRoleContractStaysAligned(t *testing.T) {
	var bundle struct {
		Defs map[string]map[string]any `json:"$defs"`
	}
	if err := json.Unmarshal(wire.Schemas, &bundle); err != nil {
		t.Fatal(err)
	}
	want := append([]string(nil), ProjectRoles...)
	sort.Strings(want)
	for _, schema := range []string{"ProjectMember", "ProjectMemberAdd", "ProjectMemberUpdate"} {
		definition, ok := bundle.Defs[schema]
		if !ok {
			t.Fatalf("embedded bundle missing %s schema", schema)
		}
		props, ok := definition["properties"].(map[string]any)
		if !ok {
			t.Fatalf("%s schema missing properties", schema)
		}
		raw, ok := props["role"].(map[string]any)["enum"].([]any)
		if !ok {
			t.Fatalf("%s.role enum missing", schema)
		}
		got := make([]string, 0, len(raw))
		for _, value := range raw {
			got = append(got, value.(string))
		}
		sort.Strings(got)
		if len(got) != len(want) {
			t.Fatalf("%s role drift: contract %v != Go %v", schema, got, want)
		}
		for i := range want {
			if got[i] != want[i] {
				t.Fatalf("%s role drift: contract %v != Go %v", schema, got, want)
			}
		}
	}
}
