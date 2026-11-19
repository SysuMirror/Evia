package ingestion

// Collect item result status closed set, mirrored by the CollectResponse
// results oneOf const branches and guarded by
// TestItemResultStatusVocabularyMatchesContract. Accepted items carry the
// accepted event id; rejected items carry a structured error instead.
const (
	ItemStatusAccepted = "accepted"
	ItemStatusRejected = "rejected"
)

// ItemStatuses is the single Go-side source of truth for the two collect
// item statuses.
var ItemStatuses = []string{ItemStatusAccepted, ItemStatusRejected}
