package analysis

// Query job lifecycle closed set, mirrored by the QueryJob.state OpenAPI enum
// and guarded by TestQueryJobStateVocabularyMatchesContract. The same strings
// are persisted by the analysis_query_jobs table; the database CHECK constraint
// in migration 005 is the storage-side copy of this vocabulary.
const (
	JobStateQueued    = "queued"
	JobStateRunning   = "running"
	JobStateSucceeded = "succeeded"
	JobStateFailed    = "failed"
	JobStateCanceled  = "canceled"
	JobStateExpired   = "expired"
)

// JobStates is the single Go-side source of truth for the six analysis query
// job states. Raw SQL keeps its own string literals because they live inside
// query text and are covered by the table CHECK constraint; every Go-side
// comparison or Job.State assignment must use these constants.
var JobStates = []string{
	JobStateQueued, JobStateRunning, JobStateSucceeded,
	JobStateFailed, JobStateCanceled, JobStateExpired,
}
