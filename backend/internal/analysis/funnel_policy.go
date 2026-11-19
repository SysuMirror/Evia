package analysis

// Funnel mode closed set, mirrored by the nested QueryRequest.funnel.mode
// OpenAPI const and guarded by TestFunnelModeVocabularyMatchesContract.
const (
	FunnelModeOrderedWithInterleaving = "ordered_with_interleaving"
)

// FunnelModes is the single Go-side source of truth for the published
// funnel modes.
var FunnelModes = []string{FunnelModeOrderedWithInterleaving}

// ValidFunnelMode reports whether mode is one of the published funnel modes.
func ValidFunnelMode(mode string) bool {
	for _, candidate := range FunnelModes {
		if mode == candidate {
			return true
		}
	}
	return false
}
