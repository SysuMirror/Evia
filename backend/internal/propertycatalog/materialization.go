package propertycatalog

import (
	"encoding/json"
	"fmt"
)

// rawFactTable is the only table dedicated slot columns may be added to. It
// is a package-level constant rather than a parameter so generated DDL can
// never target a caller-supplied identifier.
const rawFactTable = "event_facts_raw"

// SlotValue is one resolved dedicated column for a single fact row.
//
// Materialized distinguishes two states that both look like an empty Go
// string but mean different things on disk:
//   - a property absent from the document stores a zero-byte String, the
//     structural "not materialized on this row" signal the reader rejects;
//   - a present property whose JSON value is an empty string stores the
//     two-byte token "\"\"", which must remain materialized and lossless.
type SlotValue struct {
	Column       string
	Token        string
	Materialized bool
}

// Materialization is the writer-side plan for one fact row. Marker is the
// catalog revision to persist in dedicated_layout_version (zero for the
// disabled catalog); Slots is ordered by Catalog.Columns for deterministic
// payloads and contains every physical column, even unmaterialized ones.
type Materialization struct {
	Marker uint8
	Slots  []SlotValue
}

// Plan resolves a row's dedicated token columns from the lossless derived map
// produced by propertylayout.DeriveMap (keys are encoded property keys,
// values are untouched JSON tokens). Only catalog entries matching the row's
// event identity can materialize a column; entries for other events are
// ignored. The derived map is never mutated.
//
// The disabled catalog returns marker zero and no slots, which is the exact
// shape a writer needs to reproduce today's insert payload.
func (c *Catalog) Plan(eventKey string, schemaVersion uint64, derived map[string]json.RawMessage) Materialization {
	if !c.Enabled() {
		return Materialization{}
	}
	// Applicable entries for this row's identity, keyed by physical column.
	// Identity dedupe during Validate guarantees at most one entry per
	// (event, version, property), so each column resolves at most once here.
	applicable := make(map[string]string, len(c.Entries))
	for _, entry := range c.Entries {
		if entry.EventKey == eventKey && entry.SchemaVersion == schemaVersion {
			applicable[SlotColumn(entry.PropertyKey)] = SlotColumn(entry.PropertyKey)
		}
	}
	columns := c.Columns()
	plan := Materialization{Marker: uint8(c.Revision), Slots: make([]SlotValue, 0, len(columns))}
	for _, column := range columns {
		slot := SlotValue{Column: column}
		if _, selected := applicable[column]; selected {
			if token, ok := derived[column]; ok {
				slot.Materialized = true
				slot.Token = string(token)
			}
		}
		plan.Slots = append(plan.Slots, slot)
	}
	return plan
}

// SlotDDL renders the idempotent additive migration that creates the catalog's
// physical slot columns. Statements follow Catalog.Columns order so the same
// catalog revision always generates byte-identical DDL; column names are the
// validated p_-hex identifiers, so no quoting or injection surface exists.
// The disabled catalog has no columns and returns no statements.
func (c *Catalog) SlotDDL() []string {
	columns := c.Columns()
	if len(columns) == 0 {
		return nil
	}
	statements := make([]string, 0, len(columns))
	for _, column := range columns {
		statements = append(statements, fmt.Sprintf(
			"ALTER TABLE %s ADD COLUMN IF NOT EXISTS %s String DEFAULT ''",
			rawFactTable, column,
		))
	}
	return statements
}
