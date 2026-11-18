//go:build integration

package ingestion

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/howtomakeaname/Evia/backend/internal/platform/budget"
	"github.com/howtomakeaname/Evia/backend/internal/platform/contract"
	"github.com/howtomakeaname/Evia/backend/internal/platform/fault"
	"github.com/howtomakeaname/Evia/backend/internal/platform/ids"
)

// TestRealBudgetAdmissionThroughCollector proves the full admission chain
// that the stub-based HTTP 429 test and the budget package unit tests cover
// only in isolation: the real Redis sliding-window limiter is injected into a
// Collector, and a budget exhaustion must surface as fault.Limited *before*
// any queue publish, keep independent scopes isolated, and recover after the
// token bucket refills. It needs only Redis because the reserve happens ahead
// of the durable queue (see Collector.Collect).
func TestRealBudgetAdmissionThroughCollector(t *testing.T) {
	limiter := realAdmissionLimiter(t, func(c *budget.Config) {
		// Effectively no refill within the test window; burst equals the
		// maximum batch so one full batch exhausts the scope deterministically.
		c.Default = budget.Policy{Rate: 0.001, Burst: budget.ContractMaxBatchEvents}
	})
	t.Cleanup(func() { resetAdmissionBucket(t, limiter, testProject, "development") })

	collector, queue, rejections := admissionCollector(t, limiter, testProject, "development")

	// First batch consumes the whole burst and must be acknowledged and published.
	fullBatch := make([]json.RawMessage, 0, budget.ContractMaxBatchEvents)
	for i := 0; i < budget.ContractMaxBatchEvents; i++ {
		fullBatch = append(fullBatch, eventJSON(validEvent()))
	}
	_, status, err := collector.Collect(context.Background(), batchOf(fullBatch...), browserAuth())
	if err != nil || status != 202 {
		t.Fatalf("full-burst batch must be accepted: status=%d err=%v", status, err)
	}
	if len(queue.records) != budget.ContractMaxBatchEvents || queue.calls != 1 {
		t.Fatalf("accepted batch must reach the queue once: calls=%d records=%d",
			queue.calls, len(queue.records))
	}

	// The next single event is over budget: Limited, and never published.
	_, status, err = collector.Collect(
		context.Background(), batchOf(eventJSON(validEvent())), browserAuth())
	if err == nil {
		t.Fatal("exhausted scope must reject")
	}
	var problem *fault.Error
	if !errors.As(err, &problem) || problem.Kind != fault.Limited {
		t.Fatalf("want fault.Limited, got %v", err)
	}
	if status != 0 {
		t.Fatalf("rejected collection must not carry an HTTP success status: %d", status)
	}
	if queue.calls != 1 {
		t.Fatalf("budget-limited batch must not publish, queue calls=%d", queue.calls)
	}
	if got := rejections.counts["COLLECTION_BUDGET_EXCEEDED"]; got != 1 {
		t.Fatalf("rejection sink must record one budget rejection, got %d", got)
	}
}

func TestRealBudgetAdmissionScopeIsolation(t *testing.T) {
	limiter := realAdmissionLimiter(t, func(c *budget.Config) {
		c.Default = budget.Policy{Rate: 0.001, Burst: budget.ContractMaxBatchEvents}
	})
	t.Cleanup(func() {
		resetAdmissionBucket(t, limiter, testProject, "development")
		resetAdmissionBucket(t, limiter, testProject, "production")
	})

	exhausted, _, _ := admissionCollector(t, limiter, testProject, "development")
	fullBatch := make([]json.RawMessage, 0, budget.ContractMaxBatchEvents)
	for i := 0; i < budget.ContractMaxBatchEvents; i++ {
		fullBatch = append(fullBatch, eventJSON(validEvent()))
	}
	if _, _, err := exhausted.Collect(context.Background(), batchOf(fullBatch...), browserAuth()); err != nil {
		t.Fatalf("priming batch failed: %v", err)
	}
	if _, _, err := exhausted.Collect(
		context.Background(), batchOf(eventJSON(validEvent())), browserAuth()); err == nil {
		t.Fatal("primed scope must be exhausted")
	}

	// A distinct environment on the same project owns an independent bucket and
	// must remain fully admissible despite the exhausted development scope.
	other, otherQueue, _ := admissionCollector(t, limiter, testProject, "production")
	_, status, err := other.Collect(
		context.Background(), batchOf(eventJSON(validEvent())), browserAuth())
	if err != nil || status != 202 || otherQueue.calls != 1 {
		t.Fatalf("independent scope must not share the exhausted bucket: status=%d err=%v calls=%d",
			status, err, otherQueue.calls)
	}
}

func TestRealBudgetAdmissionRefillRecovery(t *testing.T) {
	// One event/second: a single token needs a full second to accrue, so the
	// immediate post-burst reserve stays deterministically exhausted even under
	// race-detector scheduling, while recovery still lands inside the 3s poll.
	limiter := realAdmissionLimiter(t, func(c *budget.Config) {
		c.Default = budget.Policy{Rate: 1, Burst: budget.ContractMaxBatchEvents}
	})
	t.Cleanup(func() { resetAdmissionBucket(t, limiter, testProject, "staging") })

	collector, _, _ := admissionCollector(t, limiter, testProject, "staging")

	fullBatch := make([]json.RawMessage, 0, budget.ContractMaxBatchEvents)
	for i := 0; i < budget.ContractMaxBatchEvents; i++ {
		fullBatch = append(fullBatch, eventJSON(validEvent()))
	}
	if _, _, err := collector.Collect(context.Background(), batchOf(fullBatch...), browserAuth()); err != nil {
		t.Fatalf("priming batch failed: %v", err)
	}
	if _, _, err := collector.Collect(
		context.Background(), batchOf(eventJSON(validEvent())), browserAuth()); err == nil {
		t.Fatal("scope must be exhausted immediately after the full burst")
	}

	// Poll until a refilled token makes the next single event admissible. A
	// deadline rather than a fixed sleep keeps this deterministic under load.
	deadline := time.Now().Add(3 * time.Second)
	recovered := false
	for time.Now().Before(deadline) {
		if _, status, err := collector.Collect(
			context.Background(), batchOf(eventJSON(validEvent())), browserAuth()); err == nil && status == 202 {
			recovered = true
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !recovered {
		t.Fatal("budget did not refill and re-admit within the deadline")
	}
}

// realAdmissionLimiter builds a Redis-backed limiter with an isolated key
// prefix, failing the test when the dev Redis dependency is absent.
func realAdmissionLimiter(t *testing.T, change func(*budget.Config)) *budget.Limiter {
	t.Helper()
	endpoint := os.Getenv("EVIA_TEST_REDIS_URL")
	if endpoint == "" {
		t.Fatal("EVIA_TEST_REDIS_URL is required for budget admission integration tests")
	}
	cfg := budget.DefaultConfig(endpoint)
	cfg.AllowInsecureLocal = true
	cfg.KeyPrefix = "evia:test:admission:" + strings.ReplaceAll(ids.New(), "-", "")
	cfg.Timeout = time.Second
	cfg.PoolSize = 8
	if change != nil {
		change(&cfg)
	}
	limiter, err := budget.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := limiter.Ping(ctx); err != nil {
		limiter.Close()
		t.Fatalf("real Redis is required: %v", err)
	}
	t.Cleanup(func() { limiter.Close() })
	return limiter
}

func resetAdmissionBucket(t *testing.T, limiter *budget.Limiter, project, environment string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := limiter.ResetBucket(ctx, project, environment); err != nil {
		t.Errorf("reset bucket %s/%s: %v", project, environment, err)
	}
}

// admissionCollector mirrors collectorFixture but injects the real limiter and
// pins the resolved source to the given project/environment scope.
func admissionCollector(t *testing.T, limiter *budget.Limiter, project, environment string,
) (*Collector, *queueStub, *rejectionSinkStub) {
	t.Helper()
	validator, err := contract.New()
	if err != nil {
		t.Fatal(err)
	}
	source := &sourceStub{value: AdmissionSource{
		Source: Source{
			ID: testSource, ProjectID: project, Kind: SourceKindBrowser,
			Environment: environment, Status: SourceStatusActive,
			AllowedOrigins: []string{"https://website.example"},
		},
		Credential: Credential{ID: testSource},
	}}
	plans := &planStub{value: Plan{
		ProjectID: project, Environment: environment, Revision: 1,
		Ready: true, Active: true, ValidFor: time.Minute,
		Events: map[string]EventRule{"lesson_opened": {
			Key: "lesson_opened", Version: 1,
			AllowedSourceKinds: []string{SourceKindBrowser, SourceKindServer},
			Schema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"lesson_id": map[string]any{"type": "integer", "minimum": 1},
				},
				"required":             []any{"lesson_id"},
				"additionalProperties": false,
			},
		}},
	}}
	queue := &queueStub{}
	collector, err := NewCollector(source, plans, queue, limiter, validator)
	if err != nil {
		t.Fatal(err)
	}
	sink := &rejectionSinkStub{}
	collector.WithRejectionSink(sink)
	return collector, queue, sink
}
