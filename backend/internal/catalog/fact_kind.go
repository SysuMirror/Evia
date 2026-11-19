package catalog

// Fact kind closed set, mirrored by the DraftInput.fact_kind OpenAPI enum and
// guarded by TestFactKindVocabularyMatchesContract. A server_confirmed event
// cannot allow browser sources; that cross-rule lives in validation.go.
const (
	FactKindUserIntent      = "user_intent"
	FactKindClientObserved  = "client_observed"
	FactKindServerConfirmed = "server_confirmed"
)

// FactKinds is the single Go-side source of truth for the three fact kinds
// accepted on event draft creation and publication.
var FactKinds = []string{
	FactKindUserIntent, FactKindClientObserved, FactKindServerConfirmed,
}

// ValidFactKind reports whether kind is one of the published fact kinds.
func ValidFactKind(kind string) bool {
	for _, candidate := range FactKinds {
		if kind == candidate {
			return true
		}
	}
	return false
}
