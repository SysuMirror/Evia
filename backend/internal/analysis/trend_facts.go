package analysis

import (
	"fmt"
	"strings"
)

// Migration 004 computes these columns from migration 003's exact legacy
// fallback. Packed lowercase hexadecimal preserves the original hash ordering.
// Both precise timestamps remain in the weight even when not exposed, so tied
// receipt/hash replays select the same legacy-versus-precise row as event_facts.
const trendOccurredTime = "occurred_at_effective"
const trendEventTime = "event_time_effective"
const trendWinnerWeight = "tuple(received_at,canonical_hash_bytes," + trendOccurredTime + "," + trendEventTime + ")"

// trendFactsSource projects only the winner payload used by a trend query. The
// deduplication identity and ordering are identical to the public event_facts
// view. One tuple-valued argMin keeps nullable fields on the same winning row;
// independent argMin calls would skip nulls or choose inconsistent tied rows.
func (p *planner) trendFactsSource(database string, scope Scope, request Request) (string, error) {
	needed := map[string]bool{"event_time": true, "received_at": true}
	if len(request.EventKeys) > 0 || len(request.EventVersions) > 0 {
		needed["event_key"] = true
	}
	if len(request.EventVersions) > 0 {
		needed["schema_version"] = true
	}
	if request.IdentityFilter != nil {
		if request.IdentityFilter.UID != nil {
			needed["uid"] = true
		} else if request.IdentityFilter.AnonymousID != nil {
			needed["anonymous_id"] = true
		}
	}
	for _, metric := range request.Metrics {
		if metric.Op == "unique" && metric.Field == nil {
			if request.Subject == "anonymous" {
				needed["anonymous_id"] = true
			} else {
				needed["uid"] = true
			}
		}
	}
	if e := WalkFields(&request, func(field *Field) error {
		if field.Kind == "property" {
			needed["event_key"], needed["schema_version"], needed["properties_json"] = true, true, true
			return nil
		}
		if field.Kind == "system" {
			// Only the seven public system fields may select physical columns.
			// Property keys and all values stay bound parameters in the outer query.
			switch field.Key {
			case "event_key", "event_time", "uid", "anonymous_id", "session_id", "page_view_id", "operation_id":
				needed[field.Key] = true
				return nil
			}
		}
		return Invalid("INVALID_FIELD", "字段无效")
	}); e != nil {
		return "", e
	}
	// Keep a fixed server-owned order for tuple offsets and stable SQL. Time and
	// receipt are always needed for the half-open range and result watermark;
	// event/schema and properties are included only for the DSL that uses them.
	columns := []struct{ name, expression string }{
		{"event_key", "event_key"}, {"schema_version", "schema_version"},
		{"event_time", trendEventTime}, {"received_at", "received_at"},
		{"uid", "uid"}, {"anonymous_id", "anonymous_id"},
		{"session_id", "session_id"}, {"page_view_id", "page_view_id"},
		{"operation_id", "operation_id"}, {"properties_json", "properties_json"},
	}
	arguments := make([]string, 0, len(columns))
	projection := []string{"project_id", "environment", "source_id", "event_id"}
	for _, column := range columns {
		if needed[column.name] {
			arguments = append(arguments, column.expression)
			projection = append(projection, fmt.Sprintf("winner.%d AS %s", len(arguments), column.name))
		}
	}
	// Restrict by grouping keys before selecting winners. A necessary identity
	// equality may first identify candidate keys, but every version of those
	// keys remains eligible. Event, time, UID and property predicates still apply
	// to the winner; a matching losing replay cannot replace an excluded winner.
	predicate := "project_id=" + p.bind("String", scope.ProjectID) +
		" AND environment=" + p.bind("String", scope.Environment)
	if candidates := p.identityCandidatePredicate(database, scope, request); candidates != "" {
		predicate += " AND " + candidates
	}
	return "(SELECT " + strings.Join(projection, ",") +
		" FROM (SELECT project_id,environment,source_id,event_id,argMin(tuple(" + strings.Join(arguments, ",") +
		")," + trendWinnerWeight + ") AS winner FROM `" + database + "`.`event_facts_raw`" +
		" WHERE " + predicate +
		" GROUP BY project_id,environment,source_id,event_id))", nil
}
