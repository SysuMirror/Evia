package main

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/howtomakeaname/Evia/backend/internal/analysis"
	"github.com/howtomakeaname/Evia/backend/internal/platform/observe"
)

func TestQueryMetricsObserverMapsEventsToMetrics(t *testing.T) {
	registry := observe.NewRegistry("query-worker")
	observer := newQueryMetricsObserver(registry)

	observer.ClaimPollCompleted(analysis.ClaimOutcomeClaimed)
	observer.ClaimPollCompleted(analysis.ClaimOutcomeClaimed)
	observer.ClaimPollCompleted(analysis.ClaimOutcomeEmpty)
	observer.ClaimPollCompleted(analysis.ClaimOutcomeError)
	observer.QueryProcessed(analysis.QueryOutcomeOK, false, 30*time.Millisecond)
	observer.QueryProcessed("QUERY_COST_EXCEEDED", false, 8*time.Millisecond)
	observer.QueryProcessed("QUERY_EXECUTION_FAILED", true, 12*time.Millisecond)

	var buffer bytes.Buffer
	if _, err := registry.WriteTo(&buffer); err != nil {
		t.Fatalf("render registry: %v", err)
	}
	output := buffer.String()
	for _, expected := range []string{
		`evia_query_claim_polls_total{outcome="claimed"} 2`,
		`evia_query_claim_polls_total{outcome="empty"} 1`,
		`evia_query_claim_polls_total{outcome="error"} 1`,
		`evia_query_jobs_total{code="ok",retryable="false"} 1`,
		`evia_query_jobs_total{code="QUERY_COST_EXCEEDED",retryable="false"} 1`,
		`evia_query_jobs_total{code="QUERY_EXECUTION_FAILED",retryable="true"} 1`,
		"# TYPE evia_query_job_duration_seconds histogram",
		`evia_query_job_duration_seconds_count{code="ok"} 1`,
		`evia_query_job_duration_seconds_count{code="QUERY_COST_EXCEEDED"} 1`,
	} {
		if !strings.Contains(output, expected) {
			t.Fatalf("query metric missing %q:\n%s", expected, output)
		}
	}
}

func TestQueryMetricsObserverSatisfiesDomainPort(t *testing.T) {
	var _ analysis.QueryObserver = newQueryMetricsObserver(observe.NewRegistry("query-worker"))
}
