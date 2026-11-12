package ingestion

import (
	"context"
	"testing"
	"time"

	"github.com/howtomakeaname/Evia/backend/internal/platform/fault"
)

type rejectionSinkStub struct {
	calls       int
	project     string
	environment string
	counts      map[string]int
}

func (s *rejectionSinkStub) RecordRejections(_ context.Context, projectID, environment string, counts map[string]int) {
	s.calls++
	s.project, s.environment = projectID, environment
	if s.counts == nil {
		s.counts = map[string]int{}
	}
	for reason, count := range counts {
		s.counts[reason] += count
	}
}

func TestRejectionSinkReceivesAggregatedItemRejections(t *testing.T) {
	collector, _, _, _, _ := collectorFixture(t)
	sink := &rejectionSinkStub{}
	collector.WithRejectionSink(sink)

	invalidProperties := validEvent()
	invalidProperties["properties"] = map[string]any{"lesson_id": "wrong"}
	malformed := map[string]any{"event_id": "broken"}
	_, status, err := collector.Collect(context.Background(), batchOf(
		eventJSON(validEvent()),
		eventJSON(invalidProperties),
		eventJSON(malformed),
	), browserAuth())
	if err != nil || status != 202 {
		t.Fatal(status, err)
	}
	if sink.calls != 1 || sink.project != testProject || sink.environment != "development" {
		t.Fatalf("sink must receive one source-scoped flush, got %+v", sink)
	}
	if sink.counts["PROPERTY_SCHEMA_MISMATCH"] != 1 || sink.counts["INVALID_EVENT"] != 1 {
		t.Fatalf("unexpected rejection counts: %+v", sink.counts)
	}
	if _, ok := sink.counts["COLLECTION_BUDGET_EXCEEDED"]; ok {
		t.Fatal("accepted batches must not record budget rejections")
	}
}

func TestRejectionSinkRecordsBudgetExceeded(t *testing.T) {
	collector, _, _, _, budget := collectorFixture(t)
	sink := &rejectionSinkStub{}
	collector.WithRejectionSink(sink)
	budget.err = fault.New(fault.Limited, "COLLECTION_BUDGET_EXCEEDED", "额度已用尽")

	_, _, err := collector.Collect(context.Background(), batchOf(eventJSON(validEvent())), browserAuth())
	if err == nil || fault.Public(err).Code != "COLLECTION_BUDGET_EXCEEDED" {
		t.Fatalf("expected budget rejection, got %v", err)
	}
	if sink.calls != 1 || sink.counts["COLLECTION_BUDGET_EXCEEDED"] != 1 {
		t.Fatalf("budget rejection must be counted once, got %+v", sink)
	}
}

func TestRejectionSinkSilentOnFullAcceptance(t *testing.T) {
	collector, _, _, _, _ := collectorFixture(t)
	sink := &rejectionSinkStub{}
	collector.WithRejectionSink(sink)

	_, status, err := collector.Collect(context.Background(), batchOf(eventJSON(validEvent())), browserAuth())
	if err != nil || status != 202 {
		t.Fatal(status, err)
	}
	if sink.calls != 0 {
		t.Fatalf("fully accepted batches must not flush the sink, got %+v", sink)
	}
}

func TestRejectionSinkNotRequiredForCollection(t *testing.T) {
	collector, _, _, queue, _ := collectorFixture(t)
	_, status, err := collector.Collect(context.Background(), batchOf(eventJSON(validEvent())), browserAuth())
	if err != nil || status != 202 || len(queue.records) != 1 {
		t.Fatal("nil sink must leave collection behavior unchanged", status, err)
	}
}

func TestPreSourceRejectionsAreNotAttributed(t *testing.T) {
	collector, _, _, _, _ := collectorFixture(t)
	sink := &rejectionSinkStub{}
	collector.WithRejectionSink(sink)

	bad := batchOf(eventJSON(validEvent()))
	bad.SentAt = time.Now().Add(-2 * time.Hour)
	if _, _, err := collector.Collect(context.Background(), bad, browserAuth()); err == nil {
		t.Fatal("expected stale batch rejection")
	}
	if sink.calls != 0 {
		t.Fatalf("pre-source failures have no project scope and must not be attributed, got %+v", sink)
	}
}
