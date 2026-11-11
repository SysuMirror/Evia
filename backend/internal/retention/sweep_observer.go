package retention

import "time"

// Telemetry mode and result label values. They are fixed strings rather than
// free-form input, so every adapter exposes the same bounded label set.
const (
	ModeDryRun   = "dry-run"
	ModeEnforce  = "enforce"
	ResultOK     = "succeeded"
	ResultFailed = "failed"
)

// SweepObserver receives retention sweep telemetry without influencing the
// sweep path. Implementations must be safe for concurrent use and must never
// return an error: a metrics outage cannot block or fail a data lifecycle job.
type SweepObserver interface {
	// SweepPassCompleted reports one RunOnce evaluation over every policy.
	// succeeded is false when the policy list could not be read. Context
	// cancellation during shutdown is not reported as a pass.
	SweepPassCompleted(succeeded bool, duration time.Duration)
	// ScopeSwept reports one policy scope evaluation. candidates is the number
	// of expired rows counted (dry-run) or deleted (enforcement), zero on failure.
	ScopeSwept(environment, mode string, succeeded bool, candidates uint64, duration time.Duration)
}

// NoopSweepObserver discards every event. It keeps the worker constructible in
// tests and in deployments that do not wire telemetry.
type NoopSweepObserver struct{}

func (NoopSweepObserver) SweepPassCompleted(bool, time.Duration)                 {}
func (NoopSweepObserver) ScopeSwept(string, string, bool, uint64, time.Duration) {}

func sweepMode(dryRun bool) string {
	if dryRun {
		return ModeDryRun
	}
	return ModeEnforce
}

// ResultLabel maps a boolean sweep outcome to the fixed metric label value.
func ResultLabel(succeeded bool) string {
	if succeeded {
		return ResultOK
	}
	return ResultFailed
}
