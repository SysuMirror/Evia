package erasure

import "time"

// Fixed telemetry label values. They are enums rather than free-form input so
// every adapter exposes the same bounded label set.
const (
	ClaimOutcomeClaimed = "claimed"
	ClaimOutcomeEmpty   = "empty"
	ClaimOutcomeError   = "error"

	JobOutcomeCompleted         = "completed"
	JobOutcomeFailed            = "failed"
	JobOutcomeLeaseLost         = "lease-lost"
	JobOutcomeFailureRecordFail = "failure-record-error"
)

// ErasureObserver receives worker telemetry without influencing the erasure
// state machine. Implementations must be safe for concurrent use and must never
// return an error: a metrics outage cannot block a data deletion job.
type ErasureObserver interface {
	// ClaimPollCompleted reports one ClaimJob attempt. outcome is one of the
	// ClaimOutcome* constants.
	ClaimPollCompleted(outcome string)
	// JobProcessed reports the terminal handling of one claimed job. outcome is
	// one of the JobOutcome* constants; code carries the durable failure code
	// for JobOutcomeFailed and is empty otherwise.
	JobProcessed(outcome, code string, duration time.Duration)
}

// NoopErasureObserver discards every event. It keeps the worker constructible
// in tests and in deployments that do not wire telemetry.
type NoopErasureObserver struct{}

func (NoopErasureObserver) ClaimPollCompleted(string)                  {}
func (NoopErasureObserver) JobProcessed(string, string, time.Duration) {}
