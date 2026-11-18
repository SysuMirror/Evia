package analysis

// Attribution model closed set, mirrored by the QueryRequest.attribution.model
// OpenAPI enum and guarded by TestAttributionModelVocabularyMatchesContract.
const (
	AttributionModelLastTouch     = "last_touch"
	AttributionModelFirstTouch    = "first_touch"
	AttributionModelLinear        = "linear"
	AttributionModelPositionBased = "position_based"
	AttributionModelTimeDecay     = "time_decay"
)

// AttributionModels is the single Go-side source of truth for the five
// attribution models accepted by attribution queries.
var AttributionModels = []string{
	AttributionModelLastTouch, AttributionModelFirstTouch, AttributionModelLinear,
	AttributionModelPositionBased, AttributionModelTimeDecay,
}

// ValidAttributionModel reports whether model is one of the published models.
func ValidAttributionModel(model string) bool {
	for _, candidate := range AttributionModels {
		if model == candidate {
			return true
		}
	}
	return false
}
