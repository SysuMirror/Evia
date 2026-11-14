package analysis

import (
	"context"
	"math"
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

// EvaluateAttribution credits every converting subject once, at its earliest
// conversion, to either the earliest (first-touch) or latest (last-touch)
// in-window touchpoint according to the explicit model. Each subject
// contributes at most one credit; subjects without an in-window touchpoint are
// reported in the fixed direct bucket.
func EvaluateAttribution(request Request, rows []map[string]any) ([]map[string]any, error) {
	return evaluateAttribution(context.Background(), request, rows)
}

func evaluateAttribution(ctx context.Context, request Request, rows []map[string]any) ([]map[string]any, error) {
	if request.Attribution == nil {
		return nil, Invalid("INVALID_ATTRIBUTION", "归因配置缺失")
	}
	window := request.Attribution.WindowSeconds
	bySubject, e := loadAttributionSubjects(ctx, rows)
	if e != nil {
		return nil, e
	}
	switch request.Attribution.Model {
	case "linear":
		return evaluateLinearAttribution(ctx, request, bySubject)
	case "time_decay":
		return evaluateTimeDecayAttribution(ctx, request, bySubject)
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
		if request.Attribution.Model == "first_touch" {
			// First-touch: the earliest in-window touchpoint receives full
			// credit, so scan forward and stop at the first match.
			for i := 0; i < conversionIndex; i++ {
				event := events[i]
				if event.time < cutoff {
					continue
				}
				if event.isTouchpoint {
					credited = event.eventKey
					break
				}
			}
		} else {
			// Last-touch (default): walk backwards to the nearest in-window
			// touchpoint.
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

// loadAttributionSubjects groups raw rows by subject so each attribution model
// shares the same causal ordering inputs.
func loadAttributionSubjects(ctx context.Context, rows []map[string]any) (map[string][]attributionEvent, error) {
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
	return bySubject, nil
}

// linearCredit accumulates equal-weight attribution for one touchpoint:
// subjects counts distinct converting subjects reaching it, credit sums the
// fractional weights so the shares across all rows total exactly one.
type linearCredit struct {
	touchpoint string
	subjects   int
	credit     float64
}

// evaluateLinearAttribution splits each converting subject's unit weight
// equally across its distinct in-window touchpoints. Subjects without an
// in-window touchpoint keep their full weight in the fixed direct bucket, so
// the converting population is never dropped.
func evaluateLinearAttribution(
	ctx context.Context,
	request Request,
	bySubject map[string][]attributionEvent,
) ([]map[string]any, error) {
	window := request.Attribution.WindowSeconds
	credits := map[string]*linearCredit{}
	converters := 0
	for _, events := range bySubject {
		if e := ctx.Err(); e != nil {
			return nil, e
		}
		sort.SliceStable(events, func(i, j int) bool {
			return earlierAttributionEvent(events[i], events[j])
		})
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

		// Distinct in-window touchpoint keys, preserving first-occurrence order.
		seen := map[string]bool{}
		keys := make([]string, 0, len(events))
		for i := 0; i < conversionIndex; i++ {
			event := events[i]
			if event.time < cutoff || !event.isTouchpoint || seen[event.eventKey] {
				continue
			}
			seen[event.eventKey] = true
			keys = append(keys, event.eventKey)
		}

		if len(keys) == 0 {
			keys = []string{directAttributionBucket}
		}
		weight := 1.0 / float64(len(keys))
		for _, key := range keys {
			entry, ok := credits[key]
			if !ok {
				entry = &linearCredit{touchpoint: key}
				credits[key] = entry
			}
			entry.subjects++
			entry.credit += weight
		}
	}

	aggregated := make([]*linearCredit, 0, len(credits))
	for _, value := range credits {
		aggregated = append(aggregated, value)
	}
	sort.Slice(aggregated, func(i, j int) bool {
		if aggregated[i].credit != aggregated[j].credit {
			return aggregated[i].credit > aggregated[j].credit
		}
		return aggregated[i].touchpoint < aggregated[j].touchpoint
	})

	out := make([]map[string]any, 0, len(aggregated))
	for _, value := range aggregated {
		share := 0.0
		if converters > 0 {
			share = value.credit / float64(converters)
		}
		out = append(out, map[string]any{
			"touchpoint": value.touchpoint,
			"subjects":   value.subjects,
			"credit":     value.credit,
			"share":      share,
		})
	}
	return out, nil
}

// evaluateTimeDecayAttribution assigns each converting subject's unit weight
// with an exponential half-life: a touchpoint delta seconds before conversion
// has raw weight 0.5^(delta/half_life), so closer events outweigh older ones.
// Repeated occurrences of the same touchpoint key keep the nearest (highest
// weight) instance. Raw weights are normalized within each subject so they sum
// to one, and a subject without an in-window touchpoint keeps full weight in
// the fixed direct bucket.
func evaluateTimeDecayAttribution(
	ctx context.Context,
	request Request,
	bySubject map[string][]attributionEvent,
) ([]map[string]any, error) {
	window := request.Attribution.WindowSeconds
	halfLife := request.Attribution.DecayHalfLifeSeconds
	credits := map[string]*linearCredit{}
	converters := 0
	for _, events := range bySubject {
		if e := ctx.Err(); e != nil {
			return nil, e
		}
		sort.SliceStable(events, func(i, j int) bool {
			return earlierAttributionEvent(events[i], events[j])
		})
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
		conversionTime := events[conversionIndex].time
		cutoff := conversionTime - window*1_000_000_000

		// Keep the nearest occurrence (smallest delta) per distinct key.
		rawByKey := map[string]float64{}
		order := make([]string, 0, len(events))
		for i := 0; i < conversionIndex; i++ {
			event := events[i]
			if event.time < cutoff || !event.isTouchpoint {
				continue
			}
			deltaSeconds := float64(conversionTime-event.time) / 1_000_000_000.0
			weight := math.Pow(0.5, deltaSeconds/float64(halfLife))
			if existing, ok := rawByKey[event.eventKey]; !ok || weight > existing {
				if !ok {
					order = append(order, event.eventKey)
				}
				rawByKey[event.eventKey] = weight
			}
		}

		if len(rawByKey) == 0 {
			rawByKey[directAttributionBucket] = 1.0
			order = []string{directAttributionBucket}
		}

		// Normalize within the subject so every converter contributes total 1.
		total := 0.0
		for _, key := range order {
			total += rawByKey[key]
		}
		for _, key := range order {
			entry, ok := credits[key]
			if !ok {
				entry = &linearCredit{touchpoint: key}
				credits[key] = entry
			}
			entry.subjects++
			entry.credit += rawByKey[key] / total
		}
	}

	aggregated := make([]*linearCredit, 0, len(credits))
	for _, value := range credits {
		aggregated = append(aggregated, value)
	}
	sort.Slice(aggregated, func(i, j int) bool {
		if aggregated[i].credit != aggregated[j].credit {
			return aggregated[i].credit > aggregated[j].credit
		}
		return aggregated[i].touchpoint < aggregated[j].touchpoint
	})

	out := make([]map[string]any, 0, len(aggregated))
	for _, value := range aggregated {
		share := 0.0
		if converters > 0 {
			share = value.credit / float64(converters)
		}
		out = append(out, map[string]any{
			"touchpoint": value.touchpoint,
			"subjects":   value.subjects,
			"credit":     value.credit,
			"share":      share,
		})
	}
	return out, nil
}
