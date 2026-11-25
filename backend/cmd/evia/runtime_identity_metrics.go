package main

import (
	"time"

	"github.com/howtomakeaname/Evia/backend/internal/identity"
	"github.com/howtomakeaname/Evia/backend/internal/platform/observe"
)

// identityMetricsObserver adapts fold-round events to Prometheus instruments
// on the identity-worker registry. Labels are fixed enums plus the durable
// failure code; scopes are counted without project identifiers to keep
// cardinality bounded.
type identityMetricsObserver struct {
	scopes   *observe.Counter
	rounds   *observe.Counter
	pruned   *observe.Counter
	duration *observe.Histogram
}

func newIdentityMetricsObserver(registry *observe.Registry) identity.Observer {
	return &identityMetricsObserver{
		scopes: registry.Counter("evia_identity_scopes_total",
			"Identity fold scope attempts, partitioned by outcome.",
			"outcome"),
		rounds: registry.Counter("evia_identity_folds_total",
			"Identity fold terminal handling, partitioned by outcome and durable failure code.",
			"outcome", "code"),
		pruned: registry.Counter("evia_identity_revisions_pruned_total",
			"Identity revisions pruned or prune failures during retention sweeps.",
			"outcome"),
		duration: registry.Histogram("evia_identity_fold_duration_seconds",
			"Wall-clock duration of one identity fold scope round.",
			observe.DefaultDurationBuckets),
	}
}

func (o *identityMetricsObserver) FoldScopeListFailed() {
	o.scopes.Inc("list-failed")
}

func (o *identityMetricsObserver) FoldScopeClaimed(identity.Scope) {
	o.scopes.Inc("claimed")
}

func (o *identityMetricsObserver) FoldScopeSkipped(identity.Scope) {
	o.scopes.Inc("skipped")
}

func (o *identityMetricsObserver) FoldScanFailed(identity.Scope) {
	o.rounds.Inc("scan-failed", "")
}

func (o *identityMetricsObserver) FoldBarrierFailed(identity.Scope) {
	o.rounds.Inc("barrier-failed", "")
}

func (o *identityMetricsObserver) FoldUnchanged(identity.Scope) {
	o.rounds.Inc("unchanged", "")
}

func (o *identityMetricsObserver) FoldCompleted(_ identity.Scope, _, _, _ int64) {
	o.rounds.Inc("completed", "")
}

func (o *identityMetricsObserver) FoldFailed(_ identity.Scope, code string) {
	o.rounds.Inc("failed", code)
}

func (o *identityMetricsObserver) FoldDuration(_ identity.Scope, duration time.Duration) {
	o.duration.Observe(duration.Seconds())
}

func (o *identityMetricsObserver) FoldPruned(_ identity.Scope, deleted int64) {
	if deleted > 0 {
		o.pruned.Add(float64(deleted), "deleted")
	}
}

func (o *identityMetricsObserver) FoldPruneFailed(identity.Scope) {
	o.pruned.Inc("failed")
}
