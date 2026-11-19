package catalog

// Schema compatibility closed set, mirrored by the ValidationResult.compatibility
// OpenAPI enum and guarded by TestCompatibilityVocabularyMatchesContract.
const (
	CompatibilityInitial    = "initial"
	CompatibilityCompatible = "compatible"
	CompatibilityBreaking   = "breaking"
)

// CompatibilityValues is the single Go-side source of truth for the three
// schema compatibility verdicts.
var CompatibilityValues = []string{
	CompatibilityInitial, CompatibilityCompatible, CompatibilityBreaking,
}

// ValidCompatibility reports whether value is one of the published verdicts.
func ValidCompatibility(value string) bool {
	for _, candidate := range CompatibilityValues {
		if value == candidate {
			return true
		}
	}
	return false
}
