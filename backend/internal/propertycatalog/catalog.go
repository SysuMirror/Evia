// Package propertycatalog defines the bounded, versioned hot-key catalog that
// selects which event properties receive dedicated lossless token columns.
//
// The catalog is pure configuration: it opens no database connection, performs
// no I/O beyond config parsing and cannot change stored data. The authoritative
// properties_json document always remains the source of truth; catalog entries
// only name read-accelerator columns that a future writer materializes in the
// same insert. Nothing in this package is wired into the ingestion or query
// path yet, so importing it cannot alter any served result.
package propertycatalog

import (
	"bytes"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"

	"github.com/howtomakeaname/Evia/backend/internal/propertylayout"
)

const (
	// MaxSlots bounds the physical dedicated columns one revision may
	// materialize: eight slots is 16% of the 50-property ingestion cap.
	MaxSlots = 8
	// MaxRevision mirrors the UInt8 row marker; revision zero stays reserved
	// for the structural default that means "no dedicated layout on this row".
	MaxRevision = 255
)

var (
	// Identical to the analysis package validation patterns; they are
	// duplicated here so this kernel stays dependency-free.
	eventKeyPattern    = regexp.MustCompile(`^(sys\.)?[a-z][a-z0-9_]{0,63}$`)
	propertyKeyPattern = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)
)

// allowedTypes mirrors the planner.field extraction switch. Datetime has no
// property extraction path in the planner, so a datetime entry could never be
// accelerated and is rejected at the catalog boundary instead.
var allowedTypes = map[string]struct{}{
	"string":        {},
	"boolean":       {},
	"number":        {},
	"integer":       {},
	"array:string":  {},
	"array:boolean": {},
	"array:number":  {},
	"array:integer": {},
}

// entryIdentity is the uniqueness key for catalog entries, deliberately
// excluding ResolvedType so conflicting type claims cannot coexist.
type entryIdentity struct {
	eventKey      string
	schemaVersion uint64
	propertyKey   string
}

// Entry names one hot property on one published event version. The physical
// column is derived from PropertyKey only, because event_facts_raw is shared
// across events: entries with the same property key share one column and one
// slot even when they belong to different events.
type Entry struct {
	EventKey      string `json:"event_key"`
	SchemaVersion uint64 `json:"schema_version"`
	PropertyKey   string `json:"property_key"`
	ResolvedType  string `json:"resolved_type"`
}

// Catalog is one immutable revision of the hot-key selection. Revision zero
// with no entries is the disabled state; every active revision uses 1..255 so
// the value fits the UInt8 row marker planned for the raw table.
type Catalog struct {
	Revision uint32  `json:"revision"`
	Entries  []Entry `json:"entries"`
}

// Slot is the resolved physical target of a catalog lookup. Column is the
// encoded dedicated column name; ResolvedType keeps the planner semantics the
// reader must apply to the token found in that column.
type Slot struct {
	Column       string
	ResolvedType string
}

// Disabled returns the zero catalog: lookups always miss, so callers degrade
// to the authoritative document without a nil check.
func Disabled() *Catalog {
	return &Catalog{}
}

// ParseConfig decodes a versioned catalog document. Unknown fields are
// rejected so configuration typos cannot silently disable acceleration, and
// entries are canonicalized into a deterministic order.
func ParseConfig(raw []byte) (*Catalog, error) {
	var catalog Catalog
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&catalog); err != nil {
		return nil, fmt.Errorf("catalog config is not valid JSON: %w", err)
	}
	if err := catalog.Validate(); err != nil {
		return nil, err
	}
	catalog.normalize()
	return &catalog, nil
}

// Validate enforces every bound independently of JSON parsing so callers that
// construct a Catalog in code hit the same rules as config-authored ones.
func (c *Catalog) Validate() error {
	if c == nil {
		return fmt.Errorf("catalog must not be nil")
	}
	if len(c.Entries) == 0 {
		if c.Revision != 0 {
			return fmt.Errorf("revision %d with no entries is invalid; use revision 0 for the disabled catalog", c.Revision)
		}
		return nil
	}
	if c.Revision == 0 {
		return fmt.Errorf("catalog with entries requires a non-zero revision")
	}
	if c.Revision > MaxRevision {
		return fmt.Errorf("catalog revision %d exceeds the UInt8 marker bound %d", c.Revision, MaxRevision)
	}
	if len(c.Entries) > MaxSlots {
		return fmt.Errorf("catalog has %d entries, exceeding the %d dedicated-column slot cap", len(c.Entries), MaxSlots)
	}
	// Identity excludes ResolvedType on purpose: one event version owns one
	// type for a property, so a repeated identity with a different type is a
	// configuration conflict rather than a second slot.
	seen := make(map[entryIdentity]struct{}, len(c.Entries))
	columns := make(map[string]struct{}, len(c.Entries))
	for index, entry := range c.Entries {
		if !eventKeyPattern.MatchString(entry.EventKey) {
			return fmt.Errorf("entry %d: invalid event key %q", index, entry.EventKey)
		}
		if !propertyKeyPattern.MatchString(entry.PropertyKey) {
			return fmt.Errorf("entry %d: invalid property key %q", index, entry.PropertyKey)
		}
		if entry.SchemaVersion < 1 {
			return fmt.Errorf("entry %d: schema version must be positive", index)
		}
		if _, ok := allowedTypes[entry.ResolvedType]; !ok {
			return fmt.Errorf("entry %d: resolved type %q is not acceleratable", index, entry.ResolvedType)
		}
		identity := entryIdentity{eventKey: entry.EventKey, schemaVersion: entry.SchemaVersion, propertyKey: entry.PropertyKey}
		if _, dup := seen[identity]; dup {
			return fmt.Errorf("duplicate catalog entry for %s v%d %q", entry.EventKey, entry.SchemaVersion, entry.PropertyKey)
		}
		seen[identity] = struct{}{}
		columns[SlotColumn(entry.PropertyKey)] = struct{}{}
	}
	if len(columns) > MaxSlots {
		return fmt.Errorf("catalog resolves to %d physical columns, exceeding the %d slot cap", len(columns), MaxSlots)
	}
	return nil
}

// Enabled reports whether any dedicated column may be read or written.
func (c *Catalog) Enabled() bool {
	return c != nil && c.Revision > 0 && len(c.Entries) > 0
}

// Lookup resolves one property to its dedicated slot. The second result is
// false for the disabled catalog, unknown properties or identities that no
// entry selects; callers must then read the authoritative document.
func (c *Catalog) Lookup(eventKey string, schemaVersion uint64, propertyKey string) (Slot, bool) {
	if !c.Enabled() {
		return Slot{}, false
	}
	for _, entry := range c.Entries {
		if entry.EventKey == eventKey && entry.SchemaVersion == schemaVersion && entry.PropertyKey == propertyKey {
			return Slot{Column: SlotColumn(propertyKey), ResolvedType: entry.ResolvedType}, true
		}
	}
	return Slot{}, false
}

// Columns returns the distinct physical column names in deterministic order.
// Shared property keys across events collapse into one column.
func (c *Catalog) Columns() []string {
	if !c.Enabled() {
		return nil
	}
	seen := make(map[string]struct{}, len(c.Entries))
	columns := make([]string, 0, len(c.Entries))
	for _, entry := range c.Entries {
		column := SlotColumn(entry.PropertyKey)
		if _, ok := seen[column]; ok {
			continue
		}
		seen[column] = struct{}{}
		columns = append(columns, column)
	}
	sort.Strings(columns)
	return columns
}

// SlotColumn maps a decoded property key to its physical dedicated column,
// reusing the same reversible encoding as the parked token map so one property
// key has exactly one physical identifier across both layouts.
func SlotColumn(propertyKey string) string {
	return propertylayout.EncodeKey(propertyKey)
}

// normalize puts entries in a stable order so equivalent configs always
// serialize to identical bytes, regardless of authoring order.
func (c *Catalog) normalize() {
	sort.Slice(c.Entries, func(i, j int) bool {
		if c.Entries[i].EventKey != c.Entries[j].EventKey {
			return c.Entries[i].EventKey < c.Entries[j].EventKey
		}
		if c.Entries[i].SchemaVersion != c.Entries[j].SchemaVersion {
			return c.Entries[i].SchemaVersion < c.Entries[j].SchemaVersion
		}
		return c.Entries[i].PropertyKey < c.Entries[j].PropertyKey
	})
}
