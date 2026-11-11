package main

import (
	"strconv"
	"time"

	"github.com/howtomakeaname/Evia/backend/internal/analysis"
	"github.com/howtomakeaname/Evia/backend/internal/platform/observe"
)

// queryMetricsObserver adapts query worker events to Prometheus instruments.
// One instance is shared by the whole worker pool, so aggregate counters cover
// every configured query worker. The code label is the fixed JobError
// vocabulary (or "ok") and retryable is a boolean enum, keeping cardinality
// bounded.
type queryMetricsObserver struct {
	claimPolls *observe.Counter
	jobs       *observe.Counter
	duration   *observe.Histogram
}

func newQueryMetricsObserver(registry *observe.Registry) analysis.QueryObserver {
	return &queryMetricsObserver{
		claimPolls: registry.Counter("evia_query_claim_polls_total",
			"Query job claim attempts, partitioned by claimed, empty queue or error.",
			"outcome"),
		jobs: registry.Counter("evia_query_jobs_total",
			"Durably completed query jobs, partitioned by result code and retry classification.",
			"code", "retryable"),
		duration: registry.Histogram("evia_query_job_duration_seconds",
			"Wall-clock duration from query claim to durable completion.",
			observe.DefaultDurationBuckets, "code"),
	}
}

func (o *queryMetricsObserver) ClaimPollCompleted(outcome string) {
	o.claimPolls.Inc(outcome)
}

func (o *queryMetricsObserver) QueryProcessed(code string, retryable bool, duration time.Duration) {
	o.jobs.Inc(code, strconv.FormatBool(retryable))
	o.duration.Observe(duration.Seconds(), code)
}
