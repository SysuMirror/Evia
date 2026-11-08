package analysis

import (
	"fmt"
	"strings"
)

// These expressions match migration 003, including the legacy millisecond
// fallback. Both precise timestamps remain in the winner weight even when the
// query does not expose them: equal receipt/hash replays must select the same
// legacy-versus-precise row as event_facts.
const trendOccurredTime = "coalesce(occurred_at_precise,toDateTime64(occurred_at,9,'UTC'))"
const trendEventTime = "coalesce(event_time_precise,toDateTime64(event_time,9,'UTC'))"
const trendWinnerWeight = "tuple(received_at,canonical_hash," + trendOccurredTime + "," + trendEventTime + ")"

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
	// Only grouping keys may be filtered before winner selection. In particular,
	// event, time, UID and property predicates must remain outside this source:
	// a losing conflicting replay may otherwise replace an excluded winner.
	return "(SELECT " + strings.Join(projection, ",") +
		" FROM (SELECT project_id,environment,source_id,event_id,argMin(tuple(" + strings.Join(arguments, ",") +
		")," + trendWinnerWeight + ") AS winner FROM `" + database + "`.`event_facts_raw`" +
		" WHERE project_id=" + p.bind("String", scope.ProjectID) +
		" AND environment=" + p.bind("String", scope.Environment) +
		" GROUP BY project_id,environment,source_id,event_id))", nil
}
