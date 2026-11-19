package pipeline

// Browser navigation type closed set, mirrored by the
// EventEnvelope.context.navigation_type OpenAPI enum and guarded by
// TestNavigationTypeVocabularyMatchesContract. An empty value stays legal
// because the SDK omits the field on ordinary push navigation.
const (
	NavigationInitial = "initial"
	NavigationPush    = "push"
	NavigationReplace = "replace"
	NavigationPop     = "pop"
	NavigationRestore = "restore"
)

// NavigationTypes is the single Go-side source of truth for the five
// browser navigation types.
var NavigationTypes = []string{
	NavigationInitial, NavigationPush, NavigationReplace, NavigationPop, NavigationRestore,
}

// ValidNavigationType reports whether value is one of the published
// navigation types. Empty input is intentionally rejected here; the record
// validator permits the empty case separately.
func ValidNavigationType(value string) bool {
	for _, candidate := range NavigationTypes {
		if value == candidate {
			return true
		}
	}
	return false
}
