package analysis

// Query interval closed set, mirrored by the QueryRequest.interval OpenAPI
// enum and guarded by TestIntervalVocabularyMatchesContract.
const (
	IntervalMinute = "minute"
	IntervalHour   = "hour"
	IntervalDay    = "day"
	IntervalWeek   = "week"
)

// Intervals is the single Go-side source of truth for the four trend query
// intervals.
var Intervals = []string{IntervalMinute, IntervalHour, IntervalDay, IntervalWeek}

// ValidInterval reports whether value is one of the published intervals.
// Empty input is intentionally rejected here; callers permit the empty case
// separately because an omitted interval means unbucketed results.
func ValidInterval(value string) bool {
	for _, candidate := range Intervals {
		if value == candidate {
			return true
		}
	}
	return false
}

// MetricIntervals is the interval vocabulary accepted by metric execution.
// Week bucketing is not offered for metric definitions, so this stays a
// strict subset of Intervals rather than a second literal list.
var MetricIntervals = []string{IntervalMinute, IntervalHour, IntervalDay}

// ValidMetricInterval reports whether value is accepted by metric execution.
func ValidMetricInterval(value string) bool {
	for _, candidate := range MetricIntervals {
		if value == candidate {
			return true
		}
	}
	return false
}
