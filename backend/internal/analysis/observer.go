package analysis

import "time"

// Fixed telemetry label values. They are enums rather than free-form input so
// every adapter exposes the same bounded label set.
const (
	ClaimOutcomeClaimed = "claimed"
	ClaimOutcomeEmpty   = "empty"
	ClaimOutcomeError   = "error"

	// QueryOutcomeOK labels successfully executed and persisted queries.
	QueryOutcomeOK = "ok"
)

// QueryObserver receives query worker telemetry without influencing query
// execution. Implementations must be safe for concurrent use — one registry is
// shared by the configured worker pool — and must never return an error: a
// metrics outage cannot block or fail an analysis job.
type QueryObserver interface {
	// ClaimPollCompleted reports one ClaimQuery attempt. outcome is one of the
	// ClaimOutcome* constants.
	ClaimPollCompleted(outcome string)
	// QueryProcessed reports one durably completed query. code is QueryOutcomeOK
	// for success or the durable JobError code, and retryable mirrors the job's
	// retry classification so alerting can distinguish transient storage faults
	// from rejected requests.
	QueryProcessed(code string, retryable bool, duration time.Duration)
}

// NoopQueryObserver discards every event. It keeps the worker constructible in
// tests and in deployments that do not wire telemetry.
type NoopQueryObserver struct{}

func (NoopQueryObserver) ClaimPollCompleted(string)                  {}
func (NoopQueryObserver) QueryProcessed(string, bool, time.Duration) {}

func queryResultCode(outcome Outcome) string {
	if outcome.Error == nil {
		return QueryOutcomeOK
	}
	return outcome.Error.Code
}
