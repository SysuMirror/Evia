package main

import (
	"time"

	"github.com/howtomakeaname/Evia/backend/internal/erasure"
	"github.com/howtomakeaname/Evia/backend/internal/platform/observe"
)

// erasureMetricsObserver adapts erasure worker events to Prometheus instruments
// on the erasure-worker registry. Label values are fixed enums plus the
// durable failure code, so cardinality stays bounded by the fault vocabulary.
type erasureMetricsObserver struct {
	claimPolls *observe.Counter
	jobs       *observe.Counter
	duration   *observe.Histogram
}

func newErasureMetricsObserver(registry *observe.Registry) erasure.ErasureObserver {
	return &erasureMetricsObserver{
		claimPolls: registry.Counter("evia_erasure_claim_polls_total",
			"Erasure job claim attempts, partitioned by claimed, empty queue or error.",
			"outcome"),
		jobs: registry.Counter("evia_erasure_jobs_total",
			"Terminal erasure job handling, partitioned by outcome and durable failure code.",
			"outcome", "code"),
		duration: registry.Histogram("evia_erasure_job_duration_seconds",
			"Wall-clock duration from job claim to terminal handling.",
			observe.DefaultDurationBuckets, "outcome"),
	}
}

func (o *erasureMetricsObserver) ClaimPollCompleted(outcome string) {
	o.claimPolls.Inc(outcome)
}

func (o *erasureMetricsObserver) JobProcessed(outcome, code string, duration time.Duration) {
	o.jobs.Inc(outcome, code)
	o.duration.Observe(duration.Seconds(), outcome)
}
