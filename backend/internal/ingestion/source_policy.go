package ingestion

// Source kind closed set, mirrored by the Source.kind OpenAPI enum and
// guarded by TestSourceVocabularyMatchesContract. Browser sources require
// allowed origins and cannot carry a server secret; server sources invert
// both rules.
const (
	SourceKindBrowser = "browser"
	SourceKindServer  = "server"
)

// SourceKinds is the single Go-side source of truth for the two source kinds.
var SourceKinds = []string{SourceKindBrowser, SourceKindServer}

// ValidSourceKind reports whether kind is browser or server.
func ValidSourceKind(kind string) bool {
	for _, candidate := range SourceKinds {
		if kind == candidate {
			return true
		}
	}
	return false
}

// Source status closed set, mirrored by the Source.status OpenAPI enum and
// guarded by TestSourceVocabularyMatchesContract. A disabled source fails
// closed at collection admission.
const (
	SourceStatusActive   = "active"
	SourceStatusDisabled = "disabled"
)

// SourceStatuses is the single Go-side source of truth for the two source
// statuses.
var SourceStatuses = []string{SourceStatusActive, SourceStatusDisabled}

// ValidSourceStatus reports whether status is active or disabled.
func ValidSourceStatus(status string) bool {
	for _, candidate := range SourceStatuses {
		if status == candidate {
			return true
		}
	}
	return false
}
