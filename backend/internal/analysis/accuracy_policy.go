package analysis

// Query result accuracy closed set, mirrored by the QueryResult.metadata.accuracy
// OpenAPI enum and guarded by TestAccuracyVocabularyMatchesContract.
// Approximate is only valid for the bounded approximate aggregation operators.
const (
	AccuracyExact         = "exact"
	AccuracyApproximate   = "approximate"
	AccuracyNotApplicable = "not_applicable"
)

// AccuracyValues is the single Go-side source of truth for the three result
// accuracy markers.
var AccuracyValues = []string{
	AccuracyExact, AccuracyApproximate, AccuracyNotApplicable,
}

// ValidAccuracy reports whether value is one of the published accuracy markers.
func ValidAccuracy(value string) bool {
	for _, candidate := range AccuracyValues {
		if value == candidate {
			return true
		}
	}
	return false
}
