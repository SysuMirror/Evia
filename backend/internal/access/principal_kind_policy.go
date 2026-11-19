package access

// Principal kind closed set. The principals table classifies every identity
// as a human account or a machine principal; migration 010 enforces the same
// two values with a CHECK constraint and backfills humans from accounts.
// Go never writes this column (an account trigger and the machine issuance
// SQL do), but the audit read model surfaces it as AuditEntry.ActorKind and
// the boundary serializes it to ProjectAuditEntry.actor_kind. The vocabulary
// therefore lives here as the Go-side single source of truth, guarded by
// TestPrincipalKindVocabularyContract; raw SQL keeps its own literals inside
// query text and the migration CHECK.
const (
	PrincipalKindHuman   = "human"
	PrincipalKindMachine = "machine"
)

// PrincipalKinds is the single Go-side source of truth for the principals.kind
// vocabulary, mirrored by the ProjectAuditEntry.actor_kind OpenAPI enum.
var PrincipalKinds = []string{PrincipalKindHuman, PrincipalKindMachine}
