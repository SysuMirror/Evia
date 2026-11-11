package main

import (
	"time"

	"github.com/howtomakeaname/Evia/backend/internal/pipeline"
	"github.com/howtomakeaname/Evia/backend/internal/platform/observe"
)

// ingestMetricsObserver adapts fact-ingestion events to Prometheus instruments
// on the ingest-worker registry. Label values are fixed enums, so cardinality
// stays bounded regardless of traffic volume.
type ingestMetricsObserver struct {
	messages *observe.Counter
	batches  *observe.Counter
	records  *observe.Counter
	duration *observe.Histogram
}

func newIngestMetricsObserver(registry *observe.Registry) pipeline.IngestObserver {
	return &ingestMetricsObserver{
		messages: registry.Counter("evia_ingest_messages_polled_total",
			"Kafka messages delivered in non-empty polls."),
		batches: registry.Counter("evia_ingest_batches_total",
			"Durably processed message batches, partitioned by result.", "result"),
		records: registry.Counter("evia_ingest_records_total",
			"Records handled per batch, partitioned into ingested facts and quarantined poison messages.",
			"outcome"),
		duration: registry.Histogram("evia_ingest_batch_duration_seconds",
			"Wall-clock duration of one durable batch.",
			observe.DefaultDurationBuckets, "result"),
	}
}

func (o *ingestMetricsObserver) MessagesPolled(count int) {
	o.messages.Add(float64(count))
}

func (o *ingestMetricsObserver) BatchProcessed(succeeded bool, duration time.Duration) {
	result := pipeline.BatchOutcomeSucceeded
	if !succeeded {
		result = pipeline.BatchOutcomeFailed
	}
	o.batches.Inc(result)
	o.duration.Observe(duration.Seconds(), result)
}

func (o *ingestMetricsObserver) RecordsIngested(count int) {
	o.records.Add(float64(count), pipeline.RecordOutcomeIngested)
}

func (o *ingestMetricsObserver) RecordsQuarantined(count int) {
	o.records.Add(float64(count), pipeline.RecordOutcomeQuarantined)
}
