package analysis

import (
	"context"
	"sort"
	"strings"
)

// pathEvent carries the causal ordering columns needed to reconstruct one
// subject's actual event sequence.
type pathEvent struct {
	time                     int64
	sequence                 int64
	session, tab, source, id string
	eventKey                 string
	isStart                  bool
}

// earlierPathEvent orders events by event time, then by the increasing causal
// sequence within a shared session/tab/source. It mirrors the funnel
// comparator so path and funnel interpret equal-time events identically.
func earlierPathEvent(previous, next pathEvent) bool {
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

// EvaluatePath counts the distinct ordered event sequences subjects traverse.
// Each subject contributes at most one path: either the depth events beginning
// at its earliest pinned start event, or its first depth in-window events when
// no start is pinned. Subjects with too few events are excluded.
func EvaluatePath(request Request, rows []map[string]any) ([]map[string]any, error) {
	return evaluatePath(context.Background(), request, rows)
}

func evaluatePath(ctx context.Context, request Request, rows []map[string]any) ([]map[string]any, error) {
	if request.Path == nil {
		return nil, Invalid("INVALID_PATH", "路径配置缺失")
	}
	depth := request.Path.Depth
	pinnedStart := request.Path.StartEvent != nil
	bySubject := map[string][]pathEvent{}
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
			return nil, Invalid("INVALID_PATH_EVENT", "路径事件时间无效")
		}
		sequence, _ := integer(row["sequence"])
		bySubject[subject] = append(bySubject[subject], pathEvent{
			time:     eventTime,
			sequence: sequence,
			session:  stringValue(row["session_id"]),
			tab:      stringValue(row["tab_id"]),
			source:   stringValue(row["source_id"]),
			id:       stringValue(row["event_id"]),
			eventKey: stringValue(row["event_key"]),
			isStart:  pinnedStart && flagOn(row["is_start"]),
		})
	}

	type pathCount struct {
		steps []string
		count int
	}
	counts := map[string]*pathCount{}
	for _, events := range bySubject {
		if e := ctx.Err(); e != nil {
			return nil, e
		}
		sort.SliceStable(events, func(i, j int) bool {
			return earlierPathEvent(events[i], events[j])
		})
		begin := 0
		if pinnedStart {
			matched := -1
			for i, event := range events {
				if event.isStart {
					matched = i
					break
				}
			}
			if matched < 0 {
				continue
			}
			begin = matched
		}
		if len(events)-begin < depth {
			continue
		}
		steps := make([]string, depth)
		for i := 0; i < depth; i++ {
			steps[i] = events[begin+i].eventKey
		}
		key := strings.Join(steps, "\x1f")
		if existing, ok := counts[key]; ok {
			existing.count++
		} else {
			counts[key] = &pathCount{steps: steps, count: 1}
		}
	}

	aggregated := make([]*pathCount, 0, len(counts))
	for _, value := range counts {
		aggregated = append(aggregated, value)
	}
	sort.Slice(aggregated, func(i, j int) bool {
		if aggregated[i].count != aggregated[j].count {
			return aggregated[i].count > aggregated[j].count
		}
		return strings.Join(aggregated[i].steps, "\x1f") < strings.Join(aggregated[j].steps, "\x1f")
	})

	out := make([]map[string]any, 0, len(aggregated))
	for _, value := range aggregated {
		out = append(out, map[string]any{
			"path":     value.steps,
			"subjects": value.count,
		})
	}
	return out, nil
}
