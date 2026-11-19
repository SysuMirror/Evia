package analysis

// Aggregation operator closed set, mirrored by the nested
// QueryRequest.metrics.items.op OpenAPI enum and guarded by
// TestAggregationVocabularyMatchesContract.
const (
	OpCount  = "count"
	OpUnique = "unique"
	OpSum    = "sum"
	OpAvg    = "avg"
	OpMin    = "min"
	OpMax    = "max"
	OpP50    = "p50"
	OpP90    = "p90"
	OpP95    = "p95"
	OpP99    = "p99"
)

// Aggregations is the single Go-side source of truth for the ten metric
// aggregation operators.
var Aggregations = []string{
	OpCount, OpUnique, OpSum, OpAvg, OpMin, OpMax, OpP50, OpP90, OpP95, OpP99,
}

// ValidAggregation reports whether op is one of the published operators.
func ValidAggregation(op string) bool {
	for _, candidate := range Aggregations {
		if op == candidate {
			return true
		}
	}
	return false
}

// ApproximateAggregations is the subset that may request approximate
// accuracy. The closed validation set rejects approximate accuracy on every
// other operator.
var ApproximateAggregations = []string{OpUnique, OpP50, OpP90, OpP95, OpP99}

// ValidApproximateAggregation reports whether op accepts approximate accuracy.
func ValidApproximateAggregation(op string) bool {
	for _, candidate := range ApproximateAggregations {
		if op == candidate {
			return true
		}
	}
	return false
}
