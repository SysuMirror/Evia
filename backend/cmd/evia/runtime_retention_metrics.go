package main

import (
	"time"

	"github.com/howtomakeaname/Evia/backend/internal/platform/observe"
	"github.com/howtomakeaname/Evia/backend/internal/retention"
)

// retentionSweepObserver adapts domain sweep events to Prometheus instruments
// on the retention-worker registry. Label values are fixed enums (environment,
// mode, result), so cardinality stays bounded regardless of policy count.
type retentionSweepObserver struct {
	passTotal     *observe.Counter
	passDuration  *observe.Histogram
	scopeTotal    *observe.Counter
	candidates    *observe.Counter
	scopeDuration *observe.Histogram
}

func newRetentionSweepObserver(registry *observe.Registry) retention.SweepObserver {
	return &retentionSweepObserver{
		passTotal: registry.Counter("evia_retention_sweep_passes_total",
			"Completed retention sweep passes over all policies, partitioned by result.", "result"),
		passDuration: registry.Histogram("evia_retention_sweep_pass_duration_seconds",
			"Wall-clock duration of one full retention sweep pass.", observe.DefaultDurationBuckets),
		scopeTotal: registry.Counter("evia_retention_sweeps_total",
			"Completed per-scope retention sweeps, partitioned by environment, mode and result.",
			"environment", "mode", "result"),
		candidates: registry.Counter("evia_retention_sweep_candidates_total",
			"Expired facts counted in dry-run or deleted in enforcement, partitioned by environment and mode.",
			"environment", "mode"),
		scopeDuration: registry.Histogram("evia_retention_sweep_duration_seconds",
			"Wall-clock duration of one per-scope retention sweep.", observe.DefaultDurationBuckets,
			"environment", "result"),
	}
}

func (o *retentionSweepObserver) SweepPassCompleted(succeeded bool, duration time.Duration) {
	o.passTotal.Inc(retention.ResultLabel(succeeded))
	o.passDuration.Observe(duration.Seconds())
}

func (o *retentionSweepObserver) ScopeSwept(environment, mode string, succeeded bool, candidates uint64, duration time.Duration) {
	result := retention.ResultLabel(succeeded)
	o.scopeTotal.Inc(environment, mode, result)
	if candidates > 0 {
		o.candidates.Add(float64(candidates), environment, mode)
	}
	o.scopeDuration.Observe(duration.Seconds(), environment, result)
}
