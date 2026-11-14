package analysis

import (
	"context"
	"sort"
	"time"
)

type retentionSubject struct {
	hasStart   bool
	firstStart int64
	returns    map[int]struct{}
}

type cohortAggregate struct {
	size     int
	retained []int
}

// EvaluateRetention builds the calendar-cohort retention matrix from the
// bounded raw pull planned by Compile. A subject enters one cohort in the
// period of its earliest start event; offset zero always retains (the entry
// itself), and later offsets count distinct subjects with a return event in
// the corresponding calendar period. Periods are aligned to the request
// timezone; weeks start on Monday.
func EvaluateRetention(request Request, rows []map[string]any) ([]map[string]any, error) {
	return evaluateRetention(context.Background(), request, rows)
}

func evaluateRetention(ctx context.Context, request Request, rows []map[string]any) ([]map[string]any, error) {
	if e := ctx.Err(); e != nil {
		return nil, e
	}
	rt := request.Retention
	if rt == nil {
		return nil, Invalid("INVALID_RETENTION", "留存配置缺失")
	}
	location, e := time.LoadLocation(request.Timezone)
	if e != nil {
		return nil, Invalid("INVALID_TIMEZONE", "时区无效")
	}
	origin := periodFloor(request.TimeRange.From, location, rt.Granularity)

	// First pass: pin each subject's cohort entry to its earliest start event.
	subjects := map[string]*retentionSubject{}
	for _, row := range rows {
		if e := ctx.Err(); e != nil {
			return nil, e
		}
		subject := stringValue(row["subject_id"])
		if subject == "" {
			continue
		}
		eventTime, ok := integer(row["event_time_ns"])
		if !ok {
			return nil, Invalid("INVALID_RETENTION_EVENT", "留存事件时间无效")
		}
		state := subjects[subject]
		if state == nil {
			state = &retentionSubject{returns: map[int]struct{}{}}
			subjects[subject] = state
		}
		if flagOn(row["is_start"]) && (!state.hasStart || eventTime < state.firstStart) {
			state.hasStart = true
			state.firstStart = eventTime
		}
	}

	// Second pass: map return events onto offsets relative to each cohort.
	for _, row := range rows {
		if e := ctx.Err(); e != nil {
			return nil, e
		}
		subject := stringValue(row["subject_id"])
		state := subjects[subject]
		if state == nil || !state.hasStart || !flagOn(row["is_return"]) {
			continue
		}
		eventTime, ok := integer(row["event_time_ns"])
		if !ok {
			return nil, Invalid("INVALID_RETENTION_EVENT", "留存事件时间无效")
		}
		cohortFloor := periodFloor(time.Unix(0, state.firstStart).In(location), location, rt.Granularity)
		offset := periodOffset(cohortFloor, periodFloor(time.Unix(0, eventTime).In(location), location, rt.Granularity), rt.Granularity)
		// Negative offsets describe return events preceding entry and are not
		// retention evidence; offsets at or beyond the matrix size are dropped.
		if offset >= 0 && offset < rt.Periods {
			state.returns[offset] = struct{}{}
		}
	}

	cohorts := map[int]*cohortAggregate{}
	for _, state := range subjects {
		if e := ctx.Err(); e != nil {
			return nil, e
		}
		if !state.hasStart {
			continue
		}
		cohortFloor := periodFloor(time.Unix(0, state.firstStart).In(location), location, rt.Granularity)
		index := periodOffset(origin, cohortFloor, rt.Granularity)
		if index < 0 {
			// The SQL time bound makes this impossible; fail closed instead of
			// silently fabricating a cohort outside the requested range.
			return nil, Invalid("INVALID_RETENTION_EVENT", "留存队列落在查询范围之外")
		}
		aggregate := cohorts[index]
		if aggregate == nil {
			aggregate = &cohortAggregate{retained: make([]int, rt.Periods)}
			cohorts[index] = aggregate
		}
		aggregate.size++
		// Entry-period retention is the cohort itself: every member is present.
		aggregate.retained[0]++
		for offset := range state.returns {
			if offset >= 1 {
				aggregate.retained[offset]++
			}
		}
	}

	indices := make([]int, 0, len(cohorts))
	for index := range cohorts {
		indices = append(indices, index)
	}
	sort.Ints(indices)
	daysPerPeriod := 1
	if rt.Granularity == "week" {
		daysPerPeriod = 7
	}
	out := make([]map[string]any, 0, len(indices))
	for _, index := range indices {
		aggregate := cohorts[index]
		cohortStart := origin.AddDate(0, 0, index*daysPerPeriod)
		rates := make([]any, rt.Periods)
		for offset, retained := range aggregate.retained {
			rates[offset] = float64(retained) / float64(aggregate.size)
		}
		out = append(out, map[string]any{
			"cohort_index":   index,
			"cohort_start":   cohortStart.UTC().Format(time.RFC3339Nano),
			"cohort_size":    aggregate.size,
			"retained":       append([]int(nil), aggregate.retained...),
			"retention_rate": rates,
		})
	}
	return out, nil
}

// flagOn accepts both native booleans and ClickHouse UInt8 0/1 encodings.
func flagOn(value any) bool {
	boolean, _ := value.(bool)
	if boolean {
		return true
	}
	number, ok := integer(value)
	return ok && number != 0
}

// periodFloor truncates to a timezone-local midnight for days, or the Monday
// midnight starting that week for weeks.
func periodFloor(when time.Time, location *time.Location, granularity string) time.Time {
	local := when.In(location)
	year, month, day := local.Date()
	midnight := time.Date(year, month, day, 0, 0, 0, 0, location)
	if granularity == "day" {
		return midnight
	}
	// Go's Weekday is Sunday=0; shift so Monday becomes zero.
	back := (int(midnight.Weekday()) + 6) % 7
	return midnight.AddDate(0, 0, -back)
}

// periodOffset counts whole calendar periods between two aligned floors. It
// uses ordinal day arithmetic rather than nanosecond division so DST shifts
// cannot move the boundary by an hour.
func periodOffset(origin, floor time.Time, granularity string) int {
	days := dayOrdinal(floor) - dayOrdinal(origin)
	if granularity == "week" {
		return days / 7
	}
	return days
}

// dayOrdinal is a monotonic day number across years; 372 exceeds the longest
// possible YearDay (366), so year*372+YearDay never collides between years.
func dayOrdinal(when time.Time) int {
	return when.Year()*372 + when.YearDay()
}
