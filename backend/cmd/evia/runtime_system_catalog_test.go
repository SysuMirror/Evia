package main

import (
	"sort"
	"testing"

	"github.com/howtomakeaname/Evia/backend/internal/delivery"
	"github.com/howtomakeaname/Evia/backend/internal/ingestion"
)

// TestReservedEventLimitPinsEmbeddedCatalog guards the cross-package invariant
// behind the public config budget: delivery.ReservedEventLimit is a hardcoded
// constant while the real reserved-event count is defined by the embedded
// ingestion system catalog. Adding a sixth sys.* event without updating the
// limit would silently mis-size the PublicConfigEventLimit budget, so the
// composition root pins the two together here rather than trusting comments.
func TestReservedEventLimitPinsEmbeddedCatalog(t *testing.T) {
	t.Parallel()
	catalog, err := ingestion.LoadSystemCatalog()
	if err != nil {
		t.Fatalf("embedded system catalog must load: %v", err)
	}
	catalogKeys := catalog.Keys()
	if len(catalogKeys) != delivery.ReservedEventLimit {
		t.Fatalf("delivery.ReservedEventLimit=%d but embedded catalog has %d rules; update the constant and public config budget together",
			delivery.ReservedEventLimit, len(catalogKeys))
	}

	provider := systemEventProvider{catalog: catalog}
	reserved := provider.ReservedEvents()
	if len(reserved) != delivery.ReservedEventLimit {
		t.Fatalf("provider exposed %d reserved events, want %d", len(reserved), delivery.ReservedEventLimit)
	}

	// The adapter must surface every catalog rule one-to-one without dropping
	// or inventing keys; lexical order from catalog.Keys() is preserved.
	providerKeys := make([]string, 0, len(reserved))
	for _, event := range reserved {
		providerKeys = append(providerKeys, event.Key)
	}
	sort.Strings(providerKeys)
	for i, key := range catalogKeys {
		if providerKeys[i] != key {
			t.Fatalf("reserved provider key mismatch at %d: catalog %q vs provider %q", i, key, providerKeys[i])
		}
	}

	// The public config budget is exactly project capacity plus the platform
	// reserved events; this is the value enforced by the delivery limit guard.
	if want := delivery.ProjectReleaseEventLimit + len(reserved); delivery.PublicConfigEventLimit != want {
		t.Fatalf("PublicConfigEventLimit=%d must equal project limit %d + reserved %d",
			delivery.PublicConfigEventLimit, delivery.ProjectReleaseEventLimit, len(reserved))
	}
}
