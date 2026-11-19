package analysis

// Retention granularity closed set, mirrored by the nested
// QueryRequest.retention.granularity OpenAPI enum and guarded by
// TestRetentionGranularityVocabularyMatchesContract.
const (
	RetentionGranularityDay   = "day"
	RetentionGranularityWeek  = "week"
	RetentionGranularityMonth = "month"
)

// RetentionGranularities is the single Go-side source of truth for the three
// retention matrix granularities.
var RetentionGranularities = []string{
	RetentionGranularityDay, RetentionGranularityWeek, RetentionGranularityMonth,
}

// ValidRetentionGranularity reports whether value is one of the published
// retention granularities.
func ValidRetentionGranularity(value string) bool {
	for _, candidate := range RetentionGranularities {
		if value == candidate {
			return true
		}
	}
	return false
}
