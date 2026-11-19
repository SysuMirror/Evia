package analysis

// Query kind closed set, mirrored by the QueryRequest.kind OpenAPI enum and
// guarded by TestQueryKindVocabularyMatchesContract. The same slice backs
// the capability discovery advertisement, so validation and discovery can no
// longer diverge into two copies of the vocabulary.
const (
	QueryKindEvents      = "events"
	QueryKindTrend       = "trend"
	QueryKindFunnel      = "funnel"
	QueryKindJourney     = "journey"
	QueryKindRetention   = "retention"
	QueryKindSegment     = "segment"
	QueryKindPath        = "path"
	QueryKindAttribution = "attribution"
)

// QueryKinds is the single Go-side source of truth for the eight query
// kinds accepted by the analysis engine.
var QueryKinds = []string{
	QueryKindEvents, QueryKindTrend, QueryKindFunnel, QueryKindJourney,
	QueryKindRetention, QueryKindSegment, QueryKindPath, QueryKindAttribution,
}

// ValidQueryKind reports whether kind is one of the published query kinds.
func ValidQueryKind(kind string) bool {
	for _, candidate := range QueryKinds {
		if kind == candidate {
			return true
		}
	}
	return false
}
