package reporting

import (
	"time"

	"github.com/howtomakeaname/Evia/backend/internal/analysis"
)

// Comparison calendar units. "previous_period" mirrors the exact window
// duration; the named units preserve local wall-clock time across DST.
const (
	ComparisonPreviousPeriod = "previous_period"
	ComparisonDay            = "day"
	ComparisonWeek           = "week"
	ComparisonMonth          = "month"
	ComparisonYear           = "year"
)

// MaxComparisonAmount bounds how far back a single comparison request reaches.
const MaxComparisonAmount = 12

// ComparisonSpec declares how the comparison window is derived from the
// requested time range. The shift is always backwards and is resolved in the
// run's IANA timezone so "same local time last week/month" survives DST.
type ComparisonSpec struct {
	// Unit is one of the Comparison* constants.
	Unit string `json:"unit"`
	// Amount is 1..MaxComparisonAmount; ignored for previous_period.
	Amount int `json:"amount"`
}

// Valid reports whether the spec uses a closed-set unit and a bounded amount.
func (c ComparisonSpec) Valid() bool {
	switch c.Unit {
	case ComparisonPreviousPeriod:
		return true
	case ComparisonDay, ComparisonWeek, ComparisonMonth, ComparisonYear:
		return c.Amount >= 1 && c.Amount <= MaxComparisonAmount
	default:
		return false
	}
}

// shiftOne moves one calendar unit backwards while preserving the wall-clock
// fields in the given zone. time.Date normalizes non-existent/ambiguous DST
// instants deterministically instead of subtracting a fixed second count.
func shiftOne(instant time.Time, location *time.Location, unit string) time.Time {
	year, month, day := instant.Date()
	hour, minute, second := instant.Clock()
	switch unit {
	case ComparisonYear:
		return time.Date(year-1, month, day, hour, minute, second, instant.Nanosecond(), location)
	case ComparisonMonth:
		return time.Date(year, month-1, day, hour, minute, second, instant.Nanosecond(), location)
	case ComparisonWeek:
		return time.Date(year, month, day-7, hour, minute, second, instant.Nanosecond(), location)
	case ComparisonDay:
		return time.Date(year, month, day-1, hour, minute, second, instant.Nanosecond(), location)
	default:
		return instant
	}
}

// ShiftTimeRange returns the comparison window for the requested range.
// Calendar units rebuild each endpoint from its zoned wall-clock fields, so a
// spring-forward or fall-back transition changes the offset in seconds rather
// than corrupting the local time of the comparison window.
func ShiftTimeRange(runner analysis.TimeRange, timezone string, spec ComparisonSpec) (analysis.TimeRange, error) {
	if !spec.Valid() {
		return analysis.TimeRange{}, analysis.Invalid("INVALID_COMPARISON_SPEC", "比较期单位或数量无效", "/comparison")
	}
	if spec.Unit == ComparisonPreviousPeriod {
		duration := runner.To.Sub(runner.From)
		if duration <= 0 {
			return analysis.TimeRange{}, analysis.Invalid("INVALID_COMPARISON_SPEC", "时间范围无效，无法推导比较期", "/time_range")
		}
		return analysis.TimeRange{From: runner.From.Add(-duration), To: runner.To.Add(-duration)}, nil
	}
	location, e := time.LoadLocation(timezone)
	if e != nil {
		return analysis.TimeRange{}, analysis.Invalid("INVALID_TIMEZONE", "时区无效，无法推导比较期", "/timezone")
	}
	from := runner.From.In(location)
	to := runner.To.In(location)
	for i := 0; i < spec.Amount; i++ {
		from = shiftOne(from, location, spec.Unit)
		to = shiftOne(to, location, spec.Unit)
	}
	if from.Year() < 1970 || to.Year() > 2100 {
		return analysis.TimeRange{}, analysis.Invalid("INVALID_COMPARISON_SPEC", "比较期超出允许的时间范围", "/comparison")
	}
	return analysis.TimeRange{From: from.UTC(), To: to.UTC()}, nil
}
