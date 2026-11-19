package analysis

// Query subject closed set, mirrored by the QueryRequest.subject and
// MetricDefinition.subject OpenAPI enums and guarded by
// TestSubjectVocabularyMatchesContract.
const (
	SubjectUID       = "uid"
	SubjectAnonymous = "anonymous"
	// SubjectResolvedPerson is published in QueryRequest.subject but stays
	// reserved until the identity graph exists; validation rejects it.
	SubjectResolvedPerson = "resolved_person"
)

// Subjects is the single Go-side source of truth for the two implemented
// query subjects.
var Subjects = []string{SubjectUID, SubjectAnonymous}

// ValidSubject reports whether value is an implemented query subject. The
// reserved resolved_person value is intentionally rejected here.
func ValidSubject(value string) bool {
	for _, candidate := range Subjects {
		if value == candidate {
			return true
		}
	}
	return false
}
