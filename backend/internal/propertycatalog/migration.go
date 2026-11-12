package propertycatalog

import (
	"fmt"
	"strings"
)

// View slot offsets start after the 29 fixed tuple fields defined by
// migrations 001-009. The dedicated marker occupies fact.29; catalog token
// columns therefore begin at fact.30.
const firstViewSlotOffset = 30

// viewHeader is the fixed SELECT projection shared by every event_facts view
// migration. It must stay byte-aligned with migration 009; slot columns are
// only ever appended after fact.29.
const viewProjectionHeader = `CREATE OR REPLACE VIEW event_facts AS
SELECT
    project_id,
    environment,
    source_id,
    event_id,
    fact.1 AS event_key,
    fact.2 AS schema_version,
    fact.3 AS release_revision,
    fact.4 AS occurred_at,
    fact.5 AS event_time,
    fact.6 AS received_at,
    fact.7 AS canonical_hash,
    fact.8 AS uid,
    fact.9 AS anonymous_id,
    fact.10 AS session_id,
    fact.11 AS tab_id,
    fact.12 AS sequence,
    fact.13 AS page_view_id,
    fact.14 AS source_page_view_id,
    fact.15 AS navigation_id,
    fact.16 AS operation_id,
    fact.17 AS route_name,
    fact.18 AS route_template,
    fact.19 AS source_module,
    fact.20 AS navigation_type,
    fact.21 AS previous_page_view_id,
    fact.22 AS sdk_name,
    fact.23 AS sdk_version,
    fact.24 AS context_json,
    fact.25 AS properties_json,
    fact.26 AS source_kind,
    fact.27 AS property_layout_version,
    fact.28 AS property_tokens,
    fact.29 AS dedicated_layout_version`

const viewTuplePrefix = ` FROM
(
    SELECT
        project_id,
        environment,
        source_id,
        event_id,
        argMin(tuple(event_key, schema_version, release_revision, coalesce(occurred_at_precise, toDateTime64(occurred_at, 9, 'UTC')), coalesce(event_time_precise, toDateTime64(event_time, 9, 'UTC')), received_at, canonical_hash, uid, anonymous_id, session_id, tab_id, sequence, page_view_id, source_page_view_id, navigation_id, operation_id, route_name, route_template, source_module, navigation_type, previous_page_view_id, sdk_name, sdk_version, context_json, properties_json, source_kind, property_layout_version, property_tokens, dedicated_layout_version`

const viewTupleSuffix = `), tuple(received_at, canonical_hash, coalesce(occurred_at_precise, toDateTime64(occurred_at, 9, 'UTC')), coalesce(event_time_precise, toDateTime64(event_time, 9, 'UTC')))) AS fact
    FROM event_facts_raw
    GROUP BY project_id, environment, source_id, event_id
);`

// ViewDDL renders the complete event_facts view migration for the catalog.
// Slot columns are appended in Catalog.Columns order, so equivalent catalogs
// always produce identical DDL regardless of entry authoring order. The
// disabled catalog returns the fixed 29-slot view with no slot projections.
func (c *Catalog) ViewDDL() string {
	columns := c.Columns()
	var projection strings.Builder
	projection.WriteString(viewProjectionHeader)
	for index, column := range columns {
		projection.WriteString(fmt.Sprintf(",\n    fact.%d AS %s", firstViewSlotOffset+index, column))
	}
	projection.WriteByte('\n')

	var tuple strings.Builder
	tuple.WriteString(viewTuplePrefix)
	for _, column := range columns {
		tuple.WriteString(", " + column)
	}
	tuple.WriteString(viewTupleSuffix)
	return projection.String() + tuple.String()
}

// MigrationSQL renders the complete operator migration for one catalog
// revision: additive raw-table slot columns followed by the view recreation
// that exposes them inside the same argMin winner tuple. Both parts are
// idempotent and deterministic; historical parts keep their structural
// defaults and require no rewrite.
func (c *Catalog) MigrationSQL() string {
	var out strings.Builder
	out.WriteString("-- Generated from a reviewed hot-key catalog; do not edit by hand.\n")
	out.WriteString(fmt.Sprintf("-- Catalog revision %d, %d slot column(s).\n", c.Revision, len(c.Columns())))
	for _, statement := range c.SlotDDL() {
		out.WriteString(statement)
		out.WriteString(";\n")
	}
	out.WriteString(c.ViewDDL())
	out.WriteByte('\n')
	return out.String()
}
