package analysis

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
)

// segmentMissingLabel is the deterministic group for subjects whose earliest
// segment event has no usable scalar value.
const segmentMissingLabel = "__missing__"

type segmentSubject struct {
	earliestTime int64
	label        string
	assigned     bool
}

// EvaluateSegment assigns each subject to exactly one group based on the
// scalar field value of its earliest matching event. Subjects are never
// double-counted; blank or absent values join the explicit missing group.
// Rows arrive ordered by subject and event time, but the earliest timestamp
// is still selected defensively.
func EvaluateSegment(request Request, rows []map[string]any) ([]map[string]any, error) {
	return evaluateSegment(context.Background(), request, rows)
}

func evaluateSegment(ctx context.Context, request Request, rows []map[string]any) ([]map[string]any, error) {
	if request.Segment == nil {
		return nil, Invalid("INVALID_SEGMENT", "分群配置缺失")
	}
	subjects := map[string]*segmentSubject{}
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
			return nil, Invalid("INVALID_SEGMENT_EVENT", "分群事件时间无效")
		}
		state := subjects[subject]
		if state == nil {
			state = &segmentSubject{}
			subjects[subject] = state
		}
		if state.assigned && eventTime >= state.earliestTime {
			continue
		}
		state.earliestTime = eventTime
		state.assigned = true
		state.label = segmentLabel(row)
	}

	counts := map[string]int{}
	total := 0
	for _, state := range subjects {
		if !state.assigned {
			continue
		}
		counts[state.label]++
		total++
	}

	labels := make([]string, 0, len(counts))
	for label := range counts {
		labels = append(labels, label)
	}
	sort.Slice(labels, func(i, j int) bool {
		if counts[labels[i]] != counts[labels[j]] {
			return counts[labels[i]] > counts[labels[j]]
		}
		return labels[i] < labels[j]
	})

	out := make([]map[string]any, 0, len(labels))
	for _, label := range labels {
		share := 0.0
		if total > 0 {
			share = float64(counts[label]) / float64(total)
		}
		out = append(out, map[string]any{
			"segment_value": label,
			"subjects":      counts[label],
			"share":         share,
		})
	}
	return out, nil
}

// segmentLabel renders the scalar group value, routing missing evidence to
// the fixed missing label rather than inventing an empty group.
func segmentLabel(row map[string]any) string {
	if !flagOn(row["segment_present"]) {
		return segmentMissingLabel
	}
	value := row["segment_value"]
	if value == nil {
		return segmentMissingLabel
	}
	switch typed := value.(type) {
	case string:
		if typed == "" {
			return segmentMissingLabel
		}
		return typed
	case bool:
		return strconv.FormatBool(typed)
	case json.Number:
		return typed.String()
	case float64:
		return strconv.FormatFloat(typed, 'f', -1, 64)
	case int64:
		return strconv.FormatInt(typed, 10)
	default:
		text := fmt.Sprint(typed)
		if text == "" {
			return segmentMissingLabel
		}
		return text
	}
}
