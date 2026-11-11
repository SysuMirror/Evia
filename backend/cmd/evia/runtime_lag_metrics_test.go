package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/howtomakeaname/Evia/backend/internal/pipeline"
	"github.com/howtomakeaname/Evia/backend/internal/platform/observe"
)

func TestIngestLagObserverRendersPartitionGauges(t *testing.T) {
	registry := observe.NewRegistry("ingest-worker")
	observer := newIngestLagObserver(registry)

	observer.PartitionLagObserved(pipeline.PartitionLag{
		Topic:           "evia.events",
		Partition:       2,
		CommittedOffset: 40,
		LogEndOffset:    47,
		Lag:             7,
	})
	// A later sample overwrites the same labeled series rather than adding one.
	observer.PartitionLagObserved(pipeline.PartitionLag{
		Topic:           "evia.events",
		Partition:       2,
		CommittedOffset: 45,
		LogEndOffset:    47,
		Lag:             2,
	})

	var buffer bytes.Buffer
	if _, err := registry.WriteTo(&buffer); err != nil {
		t.Fatalf("render registry: %v", err)
	}
	output := buffer.String()
	for _, expected := range []string{
		`evia_ingest_consumer_lag{topic="evia.events",partition="2"} 2`,
		`evia_ingest_consumer_committed_offset{topic="evia.events",partition="2"} 45`,
		`evia_ingest_consumer_log_end_offset{topic="evia.events",partition="2"} 47`,
		"# TYPE evia_ingest_consumer_lag gauge",
	} {
		if !strings.Contains(output, expected) {
			t.Fatalf("lag metric missing %q:\n%s", expected, output)
		}
	}
	if strings.Count(output, `evia_ingest_consumer_lag{topic="evia.events",partition="2"}`) != 1 {
		t.Fatalf("lag gauge must keep one series per partition:\n%s", output)
	}
}

func TestIngestLagObserverSatisfiesDomainPort(t *testing.T) {
	var _ pipeline.LagObserver = newIngestLagObserver(observe.NewRegistry("ingest-worker"))
}
