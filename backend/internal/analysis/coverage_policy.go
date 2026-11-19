package analysis

// Coverage state vocabulary. The pipeline currently only certifies provisional
// coverage; settled and partial remain contract-published states reserved for
// the future completeness pipeline. Go-side production is therefore the
// provisional subset, guarded as a subset (not set equality) by
// TestCoverageStateVocabularyContract.
const CoverageStateProvisional = "provisional"

// CoverageStates is the Go-produced subset of
// QueryResult.metadata.coverage.state.
var CoverageStates = []string{CoverageStateProvisional}
