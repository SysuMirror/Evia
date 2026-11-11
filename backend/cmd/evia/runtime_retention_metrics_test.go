package main

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/howtomakeaname/Evia/backend/internal/platform/observe"
	"github.com/howtomakeaname/Evia/backend/internal/retention"
)

func renderRegistry(t *testing.T, registry *observe.Registry) string {
	t.Helper()
	var buffer bytes.Buffer
	if _, err := registry.WriteTo(&buffer); err != nil {
		t.Fatalf("render registry: %v", err)
	}
	return buffer.String()
}

func TestRetentionSweepObserverMapsDomainEventsToMetrics(t *testing.T) {
	registry := observe.NewRegistry("retention-worker")
	observer := newRetentionSweepObserver(registry)

	// One successful dry-run pass with two scopes, plus one failed enforcement scope.
	observer.SweepPassCompleted(true, 25*time.Millisecond)
	observer.SweepPassCompleted(false, 5*time.Millisecond)
	observer.ScopeSwept("development", retention.ModeDryRun, true, 7, 12*time.Millisecond)
	observer.ScopeSwept("production", retention.ModeEnforce, true, 42, 30*time.Millisecond)
	observer.ScopeSwept("staging", retention.ModeDryRun, false, 0, 8*time.Millisecond)

	output := renderRegistry(t, registry)
	for _, expected := range []string{
		`evia_retention_sweep_passes_total{result="succeeded"} 1`,
		`evia_retention_sweep_passes_total{result="failed"} 1`,
		`evia_retention_sweeps_total{environment="development",mode="dry-run",result="succeeded"} 1`,
		`evia_retention_sweeps_total{environment="production",mode="enforce",result="succeeded"} 1`,
		`evia_retention_sweeps_total{environment="staging",mode="dry-run",result="failed"} 1`,
		`evia_retention_sweep_candidates_total{environment="development",mode="dry-run"} 7`,
		`evia_retention_sweep_candidates_total{environment="production",mode="enforce"} 42`,
	} {
		if !strings.Contains(output, expected) {
			t.Fatalf("retention metric missing %q:\n%s", expected, output)
		}
	}
	// A failed scope with zero candidates must not create a candidates series.
	if strings.Contains(output, `evia_retention_sweep_candidates_total{environment="staging"`) {
		t.Fatalf("zero-candidate failed scope emitted a candidates series:\n%s", output)
	}
	// Histograms expose bucket, sum and count samples for the observed scopes.
	for _, expected := range []string{
		"# TYPE evia_retention_sweep_duration_seconds histogram",
		"# TYPE evia_retention_sweep_pass_duration_seconds histogram",
		`evia_retention_sweep_duration_seconds_count{environment="development",result="succeeded"} 1`,
		`evia_retention_sweep_pass_duration_seconds_count 2`,
	} {
		if !strings.Contains(output, expected) {
			t.Fatalf("histogram metric missing %q:\n%s", expected, output)
		}
	}
}

func TestRetentionSweepObserverSatisfiesDomainPort(t *testing.T) {
	// Compile-time guarantee that the adapter can be passed to the domain worker.
	var _ retention.SweepObserver = newRetentionSweepObserver(observe.NewRegistry("retention-worker"))
}
