package main

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/howtomakeaname/Evia/backend/internal/erasure"
	"github.com/howtomakeaname/Evia/backend/internal/platform/observe"
)

func TestErasureMetricsObserverMapsEventsToMetrics(t *testing.T) {
	registry := observe.NewRegistry("erasure-worker")
	observer := newErasureMetricsObserver(registry)

	observer.ClaimPollCompleted(erasure.ClaimOutcomeClaimed)
	observer.ClaimPollCompleted(erasure.ClaimOutcomeClaimed)
	observer.ClaimPollCompleted(erasure.ClaimOutcomeEmpty)
	observer.JobProcessed(erasure.JobOutcomeCompleted, "", 40*time.Millisecond)
	observer.JobProcessed(erasure.JobOutcomeFailed, "ERASURE_DELETE_FAILED", 15*time.Millisecond)
	observer.JobProcessed(erasure.JobOutcomeLeaseLost, "", 5*time.Millisecond)

	var buffer bytes.Buffer
	if _, err := registry.WriteTo(&buffer); err != nil {
		t.Fatalf("render registry: %v", err)
	}
	output := buffer.String()
	for _, expected := range []string{
		`evia_erasure_claim_polls_total{outcome="claimed"} 2`,
		`evia_erasure_claim_polls_total{outcome="empty"} 1`,
		`evia_erasure_jobs_total{outcome="completed",code=""} 1`,
		`evia_erasure_jobs_total{outcome="failed",code="ERASURE_DELETE_FAILED"} 1`,
		`evia_erasure_jobs_total{outcome="lease-lost",code=""} 1`,
		"# TYPE evia_erasure_job_duration_seconds histogram",
		`evia_erasure_job_duration_seconds_count{outcome="completed"} 1`,
		`evia_erasure_job_duration_seconds_count{outcome="failed"} 1`,
	} {
		if !strings.Contains(output, expected) {
			t.Fatalf("erasure metric missing %q:\n%s", expected, output)
		}
	}
}

func TestErasureMetricsObserverSatisfiesDomainPort(t *testing.T) {
	var _ erasure.ErasureObserver = newErasureMetricsObserver(observe.NewRegistry("erasure-worker"))
}
