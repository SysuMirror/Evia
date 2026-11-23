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
func projectAuditEnum(t *testing.T, property string) []string {
	t.Helper()
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
	raw, ok := props[property].(map[string]any)["enum"].([]any)
	if !ok {
		t.Fatalf("ProjectAuditEntry.%s enum missing", property)
	}
	values := make([]string, 0, len(raw))
	for _, value := range raw {
		text, ok := value.(string)
		if !ok {
			t.Fatalf("ProjectAuditEntry.%s contains non-string enum value", property)
		}
		values = append(values, text)
	}
	sort.Strings(values)
	return values
}

func assertVocabulary(t *testing.T, name string, got, want []string) {
	t.Helper()
	sort.Strings(want)
	if len(got) != len(want) {
		t.Fatalf("%s drift: contract %v != Go %v", name, got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("%s drift: contract %v != Go %v", name, got, want)
		}
	}
}

func TestPrincipalKindVocabularyContract(t *testing.T) {
	assertVocabulary(t, "principal kind", projectAuditEnum(t, "actor_kind"), append([]string(nil), PrincipalKinds...))
}

// TestAuditOutcomeVocabularyContract prevents the persistence/service result
// vocabulary from drifting from the generic project audit wire contract.
func TestAuditOutcomeVocabularyContract(t *testing.T) {
	assertVocabulary(t, "audit outcome", projectAuditEnum(t, "outcome"), []string{AuditOutcomeSuccess, AuditOutcomeFailure})
}
