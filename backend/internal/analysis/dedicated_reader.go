package analysis

import (
	"fmt"

	"github.com/howtomakeaname/Evia/backend/internal/propertycatalog"
)

// dedicatedSelection is the reader-side gate for one hot-key slot. It carries
// only server-owned values: the physical column is a validated p_-hex
// identifier and the revision is the immutable catalog's UInt8 marker, so
// neither needs a bound parameter (user property keys stay bound on the
// fallback path).
type dedicatedSelection struct {
	column   string
	revision uint8
	// hit is the row-level predicate that authorizes the dedicated path.
	hit string
	// document is the synthetic one-field document built from the exact token.
	document string
}

// selectDedicated resolves whether a property field may read its token from a
// dedicated column. The disabled catalog, a nil catalog or a field with no
// catalog entry all return ok=false so the caller keeps the existing
// map/document expression unchanged.
//
// The hit predicate requires BOTH the matching catalog revision marker AND a
// non-empty column. An absent property is persisted as the structural zero-byte
// default and therefore fails the check, while a present empty-string property
// stores the two-byte token "\"\"" and still qualifies.
func selectDedicated(catalog *propertycatalog.Catalog, f Field) (dedicatedSelection, bool) {
	if catalog == nil || !catalog.Enabled() {
		return dedicatedSelection{}, false
	}
	slot, ok := catalog.Lookup(f.EventKey, uint64(f.SchemaVersion), f.Key)
	if !ok {
		return dedicatedSelection{}, false
	}
	revision := uint8(catalog.Revision)
	hit := fmt.Sprintf("(dedicated_layout_version=%d AND %s!='')", revision, slot.Column)
	return dedicatedSelection{
		column:   slot.Column,
		revision: revision,
		hit:      hit,
		document: "concat('{\"f\":'," + slot.Column + ",'}')",
	}, true
}

// applyDedicatedTier wraps the existing map-or-document document expression
// with an outer dedicated-column branch. When no slot applies it returns the
// inputs byte-identically, which is what keeps a disabled catalog on the exact
// current query shape.
//
// Fallback order on a miss is: dedicated slot -> version-1 token map ->
// authoritative properties_json, each layer choosing the fixed JSON key 'f'
// only on its own hit and the bound user key otherwise.
func applyDedicatedTier(catalog *propertycatalog.Catalog, f Field, fallbackDoc, fallbackKey string) (doc string, keyExpr string, applied bool) {
	selection, ok := selectDedicated(catalog, f)
	if !ok {
		return fallbackDoc, fallbackKey, false
	}
	doc = "if(" + selection.hit + "," + selection.document + "," + fallbackDoc + ")"
	keyExpr = "if(" + selection.hit + ",'f'," + fallbackKey + ")"
	return doc, keyExpr, true
}
