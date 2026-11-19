package access

import (
	"encoding/json"
	"sort"
	"testing"

	"github.com/howtomakeaname/Evia/backend/internal/gen/wire"
)

// TestPrincipalKindVocabularyContract guards the principals.kind vocabulary
// surfaced through the audit read model against the
// ProjectAuditEntry.actor_kind OpenAPI enum. Both values are observable in
// Go through AuditEntry.ActorKind, so the assertion is full set equality;
// the storage-side copy is the migration 010 CHECK constraint.
func TestPrincipalKindVocabularyContract(t *testing.T) {
	var bundle struct {
		Defs map[string]map[string]any `json:"$defs"`
	}
	if err := json.Unmarshal(wire.Schemas, &bundle); err != nil {
		t.Fatal(err)
	}
	props, ok := bundle.Defs["ProjectAuditEntry"]["properties"].(map[string]any)
	if !ok {
		t.Fatal("ProjectAuditEntry schema missing properties")
	}
	raw, ok := props["actor_kind"].(map[string]any)["enum"].([]any)
	if !ok {
		t.Fatal("ProjectAuditEntry.actor_kind enum missing")
	}
	got := make([]string, 0, len(raw))
	for _, value := range raw {
		got = append(got, value.(string))
	}
	want := append([]string(nil), PrincipalKinds...)
	sort.Strings(got)
	sort.Strings(want)
	if len(got) != len(want) {
		t.Fatalf("principal kind drift: contract %v != Go %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("principal kind drift: contract %v != Go %v", got, want)
		}
	}
}
