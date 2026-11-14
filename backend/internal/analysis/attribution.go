package analysis

import (
	"context"
	"sort"
)

// directAttributionBucket receives conversions whose subject has no matching
// touchpoint inside the lookback window, so the converting population is never
// silently dropped from the denominator.
const directAttributionBucket = "__direct__"

// attributionEvent carries the causal ordering columns and the conversion /
// touchpoint markers needed to assign one last-touch credit per subject.
type attributionEvent struct {
	time                     int64
	sequence                 int64
	session, tab, source, id string
	eventKey                 string
	isConversion             bool
	isTouchpoint             bool
}

// earlierAttributionEvent orders events exactly like the funnel and path
// comparators: event time first, then the increasing causal sequence within a
// shared session/tab/source for equal timestamps.
func earlierAttributionEvent(previous, next attributionEvent) bool {
	if previous.time != next.time {
		return previous.time < next.time
	}
	return next.session != "" &&
		next.session == previous.session &&
		next.tab == previous.tab &&
		next.source == previous.source &&
		next.sequence > previous.sequence &&
		previous.sequence > 0
}

// EvaluateAttribution credits every converting subject to the last touchpoint
// before its earliest conversion within the bounded lookback window. Each
// subject contributes at most one credit; subjects without an in-window
// touchpoint are reported in the fixed direct bucket.
func EvaluateAttribution(request Request, rows []map[string]any) ([]map[string]any, error) {
	return evaluateAttribution(context.Background(), request, rows)
}

func evaluateAttribution(ctx context.Context, request Request, rows []map[string]any) ([]map[string]any, error) {
	if request.Attribution == nil {
		return nil, Invalid("INVALID_ATTRIBUTION", "归因配置缺失")
	}
	window := request.Attribution.WindowSeconds
	bySubject := map[string][]attributionEvent{}
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
			return nil, Invalid("INVALID_ATTRIBUTION_EVENT", "归因事件时间无效")
		}
		sequence, _ := integer(row["sequence"])
		bySubject[subject] = append(bySubject[subject], attributionEvent{
			time:         eventTime,
			sequence:     sequence,
			session:      stringValue(row["session_id"]),
			tab:          stringValue(row["tab_id"]),
			source:       stringValue(row["source_id"]),
			id:           stringValue(row["event_id"]),
			eventKey:     stringValue(row["event_key"]),
			isConversion: flagOn(row["is_conversion"]),
			isTouchpoint: flagOn(row["is_touchpoint"]),
		})
	}

	type creditCount struct {
		touchpoint string
		count      int
	}
	counts := map[string]*creditCount{}
	converters := 0
	for _, events := range bySubject {
		if e := ctx.Err(); e != nil {
			return nil, e
		}
		sort.SliceStable(events, func(i, j int) bool {
			return earlierAttributionEvent(events[i], events[j])
		})
		// The earliest conversion is the single attribution point so one
		// subject can never receive more than one credit.
		conversionIndex := -1
		for i, event := range events {
			if event.isConversion {
				conversionIndex = i
				break
			}
		}
		if conversionIndex < 0 {
			continue
		}
		converters++
		cutoff := events[conversionIndex].time - window*1_000_000_000
		credited := directAttributionBucket
		// Walk backwards to the nearest touchpoint still inside the window.
		for i := conversionIndex - 1; i >= 0; i-- {
			event := events[i]
			if event.time < cutoff {
				break
			}
			if event.isTouchpoint {
				credited = event.eventKey
				break
			}
		}
		if existing, ok := counts[credited]; ok {
			existing.count++
		} else {
			counts[credited] = &creditCount{touchpoint: credited, count: 1}
		}
	}

	aggregated := make([]*creditCount, 0, len(counts))
	for _, value := range counts {
		aggregated = append(aggregated, value)
	}
	sort.Slice(aggregated, func(i, j int) bool {
		if aggregated[i].count != aggregated[j].count {
			return aggregated[i].count > aggregated[j].count
		}
		return aggregated[i].touchpoint < aggregated[j].touchpoint
	})

	out := make([]map[string]any, 0, len(aggregated))
	for _, value := range aggregated {
		share := 0.0
		if converters > 0 {
			share = float64(value.count) / float64(converters)
		}
		out = append(out, map[string]any{
			"touchpoint": value.touchpoint,
			"subjects":   value.count,
			"share":      share,
		})
	}
	return out, nil
}
