package ingestion

// Source application state vocabulary. New sources start pending; the
// storage read model derives applied from observed_at through a SQL CASE
// expression. failed remains a contract-published state with no Go or SQL
// producer today. Go-observable values are therefore a subset of the
// application_state enums on Source/SourceIssued/Retention, guarded as a
// subset by TestApplicationStateVocabularyContract.
const (
	ApplicationStatePending = "pending"
	ApplicationStateApplied = "applied"
)

// ApplicationStates is the Go-observable subset of the published
// application_state vocabulary. The SQL CASE expression keeps its own string
// literals because application_state is a derived, never stored column.
var ApplicationStates = []string{ApplicationStatePending, ApplicationStateApplied}
