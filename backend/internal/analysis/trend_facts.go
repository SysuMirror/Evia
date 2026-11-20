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
		if metric.Op == OpUnique && metric.Field == nil {
			if request.Subject == SubjectAnonymous {
				needed["anonymous_id"] = true
			} else {
				needed["uid"] = true
			}
		}
	}
	if request.Subject == SubjectResolvedPerson {
		// The plan-time identity mapping reads both raw identity columns;
		// the winner tuple must carry them or the outer multiIf would
		// reference unprojected columns.
		needed["uid"], needed["anonymous_id"] = true, true
	}
	dedicatedNeeded := map[string]bool{}
	if e := WalkFields(&request, func(field *Field) error {
		if field.Kind == "property" {
			// The layout columns travel in the same winner tuple so the
			// planner's property expressions can choose tokens or document
			// without a second read of the raw table.
			needed["event_key"], needed["schema_version"], needed["properties_json"] = true, true, true
			needed["property_layout_version"], needed["property_tokens"] = true, true
			// A catalog hit additionally requires the marker and the slot
			// column inside the same winning tuple; a disabled catalog never
			// hits, so the default query appends nothing.
			if selection, ok := selectDedicated(p.catalog, *field); ok {
				needed["dedicated_layout_version"] = true
				dedicatedNeeded[selection.column] = true
			}
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
		{"property_layout_version", "property_layout_version"}, {"property_tokens", "property_tokens"},
	}
	arguments := make([]string, 0, len(columns))
	// The raw predicate fixes both authorization dimensions for every input
	// row. Return those dimensions as bound constants rather than carrying the
	// same strings in each winner's grouping key and aggregate output block.
	project := p.bind("String", scope.ProjectID)
	environment := p.bind("String", scope.Environment)
	projection := []string{project + " AS project_id", environment + " AS environment", "source_id", "event_id"}
	for _, column := range columns {
		if needed[column.name] {
			arguments = append(arguments, column.expression)
			projection = append(projection, fmt.Sprintf("winner.%d AS %s", len(arguments), column.name))
		}
	}
	// Dedicated slots are appended after the fixed columns so existing tuple
	// offsets never shift. Catalog column order keeps the SQL deterministic
	// and independent of request field order.
	if len(dedicatedNeeded) > 0 {
		arguments = append(arguments, "dedicated_layout_version")
		projection = append(projection, fmt.Sprintf("winner.%d AS dedicated_layout_version", len(arguments)))
		for _, column := range p.catalog.Columns() {
			if !dedicatedNeeded[column] {
				continue
			}
			arguments = append(arguments, column)
			projection = append(projection, fmt.Sprintf("winner.%d AS %s", len(arguments), column))
		}
	}
	// Restrict by grouping keys before selecting winners. A necessary identity
	// equality may first identify candidate keys, but every version of those
	// keys remains eligible. Event, time, UID and property predicates still apply
	// to the winner; a matching losing replay cannot replace an excluded winner.
	// Qualify the stored scope columns: the outer constant aliases must never
	// replace the authoritative raw-table authorization predicate.
	predicate := "raw_facts.project_id=" + project +
		" AND raw_facts.environment=" + environment
	if candidates := p.identityCandidatePredicate(database, scope, request); candidates != "" {
		predicate += " AND " + candidates
	}
	return "(SELECT " + strings.Join(projection, ",") +
		" FROM (SELECT source_id,event_id,argMin(tuple(" + strings.Join(arguments, ",") +
		")," + trendWinnerWeight + ") AS winner FROM `" + database + "`.`event_facts_raw` AS raw_facts" +
		" WHERE " + predicate +
		" GROUP BY source_id,event_id))", nil
}
