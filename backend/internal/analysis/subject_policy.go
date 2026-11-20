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

// Subjects is the single Go-side source of truth for the two metric
// definition subjects. Metric definitions stay two-valued; queries accept the
// additional resolved_person subject through QuerySubjects.
var Subjects = []string{SubjectUID, SubjectAnonymous}

// QuerySubjects is the closed set of ad-hoc query subjects. resolved_person
// resolves through the versioned identity graph and requires plan-time
// expansion materialized into the durable request.
var QuerySubjects = []string{SubjectUID, SubjectAnonymous, SubjectResolvedPerson}

// ValidSubject reports whether value is an implemented metric subject.
func ValidSubject(value string) bool {
	for _, candidate := range Subjects {
		if value == candidate {
			return true
		}
	}
	return false
}

// ValidQuerySubject reports whether value is an implemented ad-hoc query
// subject, including the identity-graph subject.
func ValidQuerySubject(value string) bool {
	for _, candidate := range QuerySubjects {
		if value == candidate {
			return true
		}
	}
	return false
}

// SubjectRequiresPersonPin reports whether a kind must pin exactly one
// resolved person to run with the resolved subject. Raw-row listings need a
// bounded member predicate; journeys stay single-raw-identity only.
func SubjectRequiresPersonPin(kind string) bool {
	return kind == QueryKindEvents
}
