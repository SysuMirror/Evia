package analysis

import (
	"encoding/json"
	"fmt"
	"sort"
	"time"
)

type funnelEvent struct {
	time                     int64
	sequence                 int64
	session, tab, source, id string
	matches                  []bool
	entities                 map[int]string
}
type progress struct {
	start, last funnelEvent
	next        int
	entity      string
}

func stringValue(v any) string { value, _ := v.(string); return value }
func follows(previous, next funnelEvent) bool {
	return next.time > previous.time || next.time == previous.time && next.session != "" && next.session == previous.session && next.tab != "" && next.tab == previous.tab && next.source == previous.source && next.sequence > previous.sequence && previous.sequence > 0
}

// EvaluateFunnel counts each raw subject at most once per reached step. Other
// events may interleave. One event cannot satisfy two steps. Equal-time events
// advance only when their common session supplies an increasing causal sequence.
// Candidate starts are retained so a later valid start can satisfy the window.
func EvaluateFunnel(request Request, rows []map[string]any) ([]map[string]any, error) {
	f := request.Funnel
	if f == nil {
		return nil, Invalid("INVALID_FUNNEL", "漏斗配置缺失")
	}
	bySubject := map[string][]funnelEvent{}
	entitySteps := map[int]bool{}
	for _, field := range f.SameEntity {
		entitySteps[field.StepIndex] = true
	}
	for _, row := range rows {
		subject := stringValue(row["subject_id"])
		if subject == "" {
			continue
		}
		eventTime, ok := integer(row["event_time_ns"])
		if !ok {
			return nil, Invalid("INVALID_FUNNEL_EVENT", "漏斗事件时间无效")
		}
		sequence, _ := integer(row["sequence"])
		event := funnelEvent{time: eventTime, sequence: sequence, session: stringValue(row["session_id"]), tab: stringValue(row["tab_id"]), source: stringValue(row["source_id"]), id: stringValue(row["event_id"]), matches: make([]bool, len(f.Steps)), entities: map[int]string{}}
		for i := range f.Steps {
			value := row[fmt.Sprintf("step_%d", i)]
			number, ok := integer(value)
			boolean, _ := value.(bool)
			event.matches[i] = boolean || ok && number != 0
			if entitySteps[i] {
				entity := row[fmt.Sprintf("entity_%d", i)]
				if entity != nil {
					encoded, e := json.Marshal(entity)
					if e != nil {
						return nil, e
					}
					event.entities[i] = string(encoded)
				}
			}
		}
		bySubject[subject] = append(bySubject[subject], event)
	}
	totals := make([]int, len(f.Steps))
	statesBudget := 0
	for _, events := range bySubject {
		sort.Slice(events, func(i, j int) bool {
			a, b := events[i], events[j]
			if a.time != b.time {
				return a.time < b.time
			}
			if a.session != b.session {
				return a.session < b.session
			}
			if a.tab != b.tab {
				return a.tab < b.tab
			}
			if a.sequence != b.sequence {
				return a.sequence < b.sequence
			}
			if a.source != b.source {
				return a.source < b.source
			}
			return a.id < b.id
		})
		states := []progress{}
		reached := 0
		for _, event := range events {
			nextStates := make([]progress, 0, len(states)+1)
			for _, candidate := range states {
				if event.time-candidate.start.time > int64(time.Duration(f.WindowSeconds)*time.Second) {
					continue
				}
				nextStates = append(nextStates, candidate)
				if !follows(candidate.last, event) || !event.matches[candidate.next] {
					continue
				}
				entity := candidate.entity
				if entitySteps[candidate.next] {
					value := event.entities[candidate.next]
					if value == "" || entity != "" && entity != value {
						continue
					}
					entity = value
				}
				advanced := progress{start: candidate.start, last: event, next: candidate.next + 1, entity: entity}
				if advanced.next > reached {
					reached = advanced.next
				}
				if advanced.next < len(f.Steps) {
					nextStates = append(nextStates, advanced)
				}
			}
			if event.matches[0] {
				entity := ""
				if entitySteps[0] {
					entity = event.entities[0]
				}
				if !entitySteps[0] || entity != "" {
					nextStates = append(nextStates, progress{start: event, last: event, next: 1, entity: entity})
					if reached < 1 {
						reached = 1
					}
				}
			}
			// Deduplicate identical progress states; distinct starts/entities are retained
			// because they may satisfy different windows and entity constraints.
			seen := map[string]bool{}
			states = states[:0]
			for _, candidate := range nextStates {
				key := fmt.Sprintf("%d|%s|%s|%d|%s|%s|%d|%s", candidate.start.time, candidate.start.source, candidate.start.id, candidate.last.time, candidate.last.source, candidate.last.id, candidate.next, candidate.entity)
				if !seen[key] {
					seen[key] = true
					states = append(states, candidate)
				}
			}
			statesBudget += len(states)
			if len(states) > 10000 || statesBudget > 1000000 {
				return nil, Invalid("QUERY_COST_EXCEEDED", "漏斗候选状态超过预算，请缩小范围")
			}
			if reached == len(f.Steps) {
				break
			}
		}
		for i := 0; i < reached; i++ {
			totals[i]++
		}
	}
	out := make([]map[string]any, 0, len(f.Steps))
	for i, step := range f.Steps {
		var rate any
		if i == 0 {
			if totals[0] > 0 {
				rate = float64(1)
			}
		} else if totals[i-1] > 0 {
			rate = float64(totals[i]) / float64(totals[i-1])
		}
		out = append(out, map[string]any{"step_index": i, "event_key": step.EventKey, "subjects": totals[i], "conversion_rate": rate})
	}
	return out, nil
}
