package access

import (
	"encoding/json"
	"sort"
	"testing"

	"github.com/howtomakeaname/Evia/backend/internal/gen/wire"
)

// TestMachinePermissionContractStaysAlignedWithClosedSet guards against a
// repeat of the data.erasure/data.export drift, where the Go permission
// closed set grew while the embedded OpenAPI enum and its maxItems bounds
// stayed behind and made the new grants impossible to issue over HTTP.
func TestMachinePermissionContractStaysAlignedWithClosedSet(t *testing.T) {
	var bundle struct {
		Defs map[string]struct {
			Enum       []string `json:"enum"`
			Properties map[string]struct {
				MaxItems int `json:"maxItems"`
			} `json:"properties"`
		} `json:"$defs"`
	}
	if err := json.Unmarshal(wire.Schemas, &bundle); err != nil {
		t.Fatal(err)
	}
	permission, ok := bundle.Defs["MachinePermission"]
	if !ok {
		t.Fatal("embedded bundle missing MachinePermission schema")
	}
	want := make([]string, 0, len(machinePermissions))
	for p := range machinePermissions {
		want = append(want, p)
	}
	sort.Strings(want)
	got := append([]string(nil), permission.Enum...)
	sort.Strings(got)
	if len(got) != len(want) {
		t.Fatalf("contract enum length %d != closed set %d: %v vs %v", len(got), len(want), got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("contract enum %v != closed set %v", got, want)
		}
	}
	for _, name := range []string{"MachinePrincipal", "MachinePrincipalInput", "MachineIdentity"} {
		schema, ok := bundle.Defs[name]
		if !ok {
			t.Fatalf("embedded bundle missing %s schema", name)
		}
		permissions, ok := schema.Properties["permissions"]
		if !ok {
			t.Fatalf("%s schema missing permissions property", name)
		}
		if permissions.MaxItems != len(machinePermissions) {
			t.Fatalf("%s permissions maxItems=%d, want closed set size %d", name, permissions.MaxItems, len(machinePermissions))
		}
	}
}
