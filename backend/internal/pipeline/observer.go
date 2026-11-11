package pipeline

import "time"

// Fixed batch outcome label values.
const (
	BatchOutcomeSucceeded = "succeeded"
	BatchOutcomeFailed    = "failed"

	RecordOutcomeIngested    = "ingested"
	RecordOutcomeQuarantined = "quarantined"
)

// IngestObserver receives fact-ingestion telemetry without influencing the
// pipeline. Implementations must be safe for concurrent use and must never
// return an error: a metrics outage cannot block or fail message durability.
type IngestObserver interface {
	// MessagesPolled reports the number of Kafka messages in one non-empty poll.
	MessagesPolled(count int)
	// BatchProcessed reports one durable batch outcome and its wall-clock
	// duration. Shutdown cancellation is not reported as a failed batch.
	BatchProcessed(succeeded bool, duration time.Duration)
	// RecordsIngested reports valid records durably written to the fact sink.
	RecordsIngested(count int)
	// RecordsQuarantined reports undecodable messages durably sent to quarantine.
	RecordsQuarantined(count int)
}

// NoopIngestObserver discards every event. It keeps the worker constructible
// in tests and in deployments that do not wire telemetry.
type NoopIngestObserver struct{}

func (NoopIngestObserver) MessagesPolled(int)                 {}
func (NoopIngestObserver) BatchProcessed(bool, time.Duration) {}
func (NoopIngestObserver) RecordsIngested(int)                {}
func (NoopIngestObserver) RecordsQuarantined(int)             {}

func batchResultLabel(succeeded bool) string {
	if succeeded {
		return BatchOutcomeSucceeded
	}
	return BatchOutcomeFailed
}
