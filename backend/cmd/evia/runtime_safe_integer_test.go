package main

import (
	"testing"

	"github.com/howtomakeaname/Evia/backend/internal/access"
	"github.com/howtomakeaname/Evia/backend/internal/analysis"
	"github.com/howtomakeaname/Evia/backend/internal/catalog"
	"github.com/howtomakeaname/Evia/backend/internal/delivery"
	"github.com/howtomakeaname/Evia/backend/internal/ingestion"
	"github.com/howtomakeaname/Evia/backend/internal/platform/canonical"
)

// TestSafeIntegerBoundIsSingleSource pins every package-local revision bound
// to the canonical JSON safe-integer constant. These used to be independent
// literal copies of 9007199254740991; a local value edited without updating the
// others would silently split the overflow policy across the API. The
// composition root now fails if any alias diverges from the single source.
func TestSafeIntegerBoundIsSingleSource(t *testing.T) {
	t.Parallel()
	if ingestion.MaxRevision != canonical.MaxInteger {
		t.Fatalf("ingestion.MaxRevision=%d diverges from canonical.MaxInteger=%d", ingestion.MaxRevision, canonical.MaxInteger)
	}
	if catalog.MaxInteger != canonical.MaxInteger {
		t.Fatalf("catalog.MaxInteger=%d diverges from canonical.MaxInteger=%d", catalog.MaxInteger, canonical.MaxInteger)
	}
	if delivery.MaxRevision != canonical.MaxInteger {
		t.Fatalf("delivery.MaxRevision=%d diverges from canonical.MaxInteger=%d", delivery.MaxRevision, canonical.MaxInteger)
	}
	if access.MaxMembershipRevision != canonical.MaxInteger {
		t.Fatalf("access.MaxMembershipRevision=%d diverges from canonical.MaxInteger=%d", access.MaxMembershipRevision, canonical.MaxInteger)
	}
	// analysis keeps an untyped alias because the bound is used in both int64
	// and float64 magnitude comparisons; it must still equal the same value.
	if int64(analysis.MaxSafeInteger) != canonical.MaxInteger {
		t.Fatalf("analysis.MaxSafeInteger=%v diverges from canonical.MaxInteger=%d", analysis.MaxSafeInteger, canonical.MaxInteger)
	}
	if int64(canonical.MaxSafeInteger) != canonical.MaxInteger {
		t.Fatal("canonical untyped and typed safe-integer exports diverge")
	}
}
