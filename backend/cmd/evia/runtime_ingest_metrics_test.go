package main

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/howtomakeaname/Evia/backend/internal/pipeline"
	"github.com/howtomakeaname/Evia/backend/internal/platform/observe"
)

func TestIngestMetricsObserverMapsEventsToMetrics(t *testing.T) {
	registry := observe.NewRegistry("ingest-worker")
	observer := newIngestMetricsObserver(registry)

	observer.MessagesPolled(5)
	observer.MessagesPolled(3)
	observer.RecordsIngested(6)
	observer.RecordsQuarantined(2)
	observer.BatchProcessed(true, 40*time.Millisecond)
	observer.BatchProcessed(false, 12*time.Millisecond)

	var buffer bytes.Buffer
	if _, err := registry.WriteTo(&buffer); err != nil {
		t.Fatalf("render registry: %v", err)
	}
	output := buffer.String()
	for _, expected := range []string{
		"evia_ingest_messages_polled_total 8",
		`evia_ingest_batches_total{result="succeeded"} 1`,
		`evia_ingest_batches_total{result="failed"} 1`,
		`evia_ingest_records_total{outcome="ingested"} 6`,
		`evia_ingest_records_total{outcome="quarantined"} 2`,
		"# TYPE evia_ingest_batch_duration_seconds histogram",
		`evia_ingest_batch_duration_seconds_count{result="succeeded"} 1`,
		`evia_ingest_batch_duration_seconds_count{result="failed"} 1`,
	} {
		if !strings.Contains(output, expected) {
			t.Fatalf("ingest metric missing %q:\n%s", expected, output)
		}
	}
}

func TestIngestMetricsObserverSatisfiesDomainPort(t *testing.T) {
	var _ pipeline.IngestObserver = newIngestMetricsObserver(observe.NewRegistry("ingest-worker"))
}
