package retention

import (
	"context"
	"log/slog"
	"time"

	"github.com/howtomakeaname/Evia/backend/internal/archiving"
	"github.com/howtomakeaname/Evia/backend/internal/platform/ids"
)

// SweepPlan is the deterministic evaluation of one configured policy at a
// specific instant. Cutoff is the exclusive lower bound: facts whose event
// time is strictly older than Cutoff fall outside the retention window.
type SweepPlan struct {
	ProjectID     string
	Environment   string
	RetentionDays int
	Cutoff        time.Time
}

// PlanSweep derives a cutoff from a policy window. The window is measured from
// the supplied clock so tests and replays stay deterministic.
func PlanSweep(policy Policy, now time.Time) SweepPlan {
	return SweepPlan{
		ProjectID:     policy.ProjectID,
		Environment:   policy.Environment,
		RetentionDays: policy.RetentionDays,
		Cutoff:        now.UTC().AddDate(0, 0, -policy.RetentionDays),
	}
}

// SweepStatus values mirror the retention_sweeps CHECK constraint.
const (
	SweepPlanned   = "planned"
	SweepSucceeded = "succeeded"
	SweepFailed    = "failed"
)

// SweepRecord is the durable audit row for one sweep evaluation.
type SweepRecord struct {
	ID             string
	ProjectID      string
	Environment    string
	RetentionDays  int
	Cutoff         time.Time
	DryRun         bool
	RowsCandidates uint64
	Status         string
	Detail         string
	CreatedAt      time.Time
	CompletedAt    *time.Time
}

func newRecord(plan SweepPlan, dryRun bool, now time.Time) SweepRecord {
	return SweepRecord{
		ID:            ids.New(),
		ProjectID:     plan.ProjectID,
		Environment:   plan.Environment,
		RetentionDays: plan.RetentionDays,
		Cutoff:        plan.Cutoff,
		DryRun:        dryRun,
		Status:        SweepPlanned,
		CreatedAt:     now.UTC(),
	}
}

// FactSweeper is the outbound port to the fact store. The ClickHouse adapter
// lives in the pipeline package to keep this domain free of transport details.
type FactSweeper interface {
	// CountExpired returns how many facts are older than the cutoff for the scope.
	CountExpired(ctx context.Context, project, environment string, cutoff time.Time) (uint64, error)
	// DeleteExpired removes facts older than the cutoff and returns the number of
	// rows that were eligible before the mutation (verified to no longer remain).
	DeleteExpired(ctx context.Context, project, environment string, cutoff time.Time) (uint64, error)
}

// ScopeArchiver is the optional archive-before-delete port. When configured,
// an enforced sweep materializes the full expired scope into a checksummed
// artifact before deleting it. An archive error aborts the sweep so facts are
// never deleted without a recoverable copy.
type ScopeArchiver interface {
	ArchiveScope(ctx context.Context, scope archiving.Scope) (archiving.Manifest, error)
}

// SweepStore extends the retention store with worker-scoped, system-level
// operations that have no human actor and therefore skip owner authorization.
type SweepStore interface {
	// ListRetentionPolicies returns every configured policy across all projects.
	ListRetentionPolicies(ctx context.Context) ([]Policy, error)
	RecordSweep(ctx context.Context, record SweepRecord) error
}

// SweepWorker evaluates policies on an interval. Dry run (the default) only
// counts expired rows; enforcement additionally deletes them. A single-scope
// failure is recorded and logged but does not abort the remaining scopes.
type SweepWorker struct {
	store    SweepStore
	sweeper  FactSweeper
	observer SweepObserver
	archiver ScopeArchiver
	enforce  bool
	interval time.Duration
	now      func() time.Time
	logger   *slog.Logger
}

// Option customizes a SweepWorker without growing the positional constructor.
type Option func(*SweepWorker)

// WithScopeArchiver enables archive-before-delete on enforced sweeps.
func WithScopeArchiver(archiver ScopeArchiver) Option {
	return func(w *SweepWorker) { w.archiver = archiver }
}

func NewSweepWorker(
	store SweepStore,
	sweeper FactSweeper,
	enforce bool,
	interval time.Duration,
	logger *slog.Logger,
	observer SweepObserver,
	options ...Option,
) *SweepWorker {
	if interval <= 0 {
		interval = time.Hour
	}
	if logger == nil {
		logger = slog.Default()
	}
	if observer == nil {
		observer = NoopSweepObserver{}
	}
	worker := &SweepWorker{
		store:    store,
		sweeper:  sweeper,
		observer: observer,
		enforce:  enforce,
		interval: interval,
		now:      func() time.Time { return time.Now().UTC() },
		logger:   logger,
	}
	for _, option := range options {
		option(worker)
	}
	return worker
}

// SweepResult reports what happened to one policy scope in a RunOnce pass.
type SweepResult struct {
	Plan       SweepPlan
	DryRun     bool
	Candidates uint64
	Err        error
}

// RunOnce evaluates every configured policy exactly once. It is the unit used
// both by the periodic Run loop and by tests that need deterministic control.
func (w *SweepWorker) RunOnce(ctx context.Context) ([]SweepResult, error) {
	passStart := time.Now()
	policies, err := w.store.ListRetentionPolicies(ctx)
	if err != nil {
		w.observer.SweepPassCompleted(false, time.Since(passStart))
		return nil, err
	}
	results := make([]SweepResult, 0, len(policies))
	for _, policy := range policies {
		if ctx.Err() != nil {
			// Shutdown cancellation is not an operational pass failure.
			return results, ctx.Err()
		}
		results = append(results, w.sweepOne(ctx, policy))
	}
	w.observer.SweepPassCompleted(true, time.Since(passStart))
	return results, nil
}

func (w *SweepWorker) sweepOne(ctx context.Context, policy Policy) SweepResult {
	scopeStart := time.Now()
	now := w.now()
	plan := PlanSweep(policy, now)
	dryRun := !w.enforce
	record := newRecord(plan, dryRun, now)
	result := SweepResult{Plan: plan, DryRun: dryRun}
	defer func() {
		w.observer.ScopeSwept(plan.Environment, sweepMode(dryRun),
			result.Err == nil, result.Candidates, time.Since(scopeStart))
	}()

	candidates, countErr := w.sweeper.CountExpired(ctx, plan.ProjectID, plan.Environment, plan.Cutoff)
	if countErr != nil {
		result.Err = countErr
		record.Status = SweepFailed
		record.Detail = "count expired facts failed"
		_ = w.store.RecordSweep(ctx, record)
		w.logger.Error("retention sweep count failed",
			"project_id", plan.ProjectID, "environment", plan.Environment, "error", countErr.Error())
		return result
	}
	record.RowsCandidates = candidates
	result.Candidates = candidates

	if dryRun {
		// Dry run records the candidate count without touching facts.
		record.Status = SweepSucceeded
		completed := w.now()
		record.CompletedAt = &completed
		if err := w.store.RecordSweep(ctx, record); err != nil {
			result.Err = err
			return result
		}
		w.logger.Info("retention dry-run planned",
			"project_id", plan.ProjectID, "environment", plan.Environment,
			"retention_days", plan.RetentionDays, "candidates", candidates)
		return result
	}

	if w.archiver != nil && candidates > 0 {
		if _, archiveErr := w.archiver.ArchiveScope(ctx, archiving.Scope{
			ProjectID:     plan.ProjectID,
			Environment:   plan.Environment,
			RetentionDays: plan.RetentionDays,
			Cutoff:        plan.Cutoff,
			SweepID:       record.ID,
		}); archiveErr != nil {
			result.Err = archiveErr
			record.Status = SweepFailed
			record.Detail = "archive before delete failed"
			_ = w.store.RecordSweep(ctx, record)
			w.logger.Error("retention archive failed; deletion skipped",
				"project_id", plan.ProjectID, "environment", plan.Environment, "error", archiveErr.Error())
			return result
		}
	}
	deleted, deleteErr := w.sweeper.DeleteExpired(ctx, plan.ProjectID, plan.Environment, plan.Cutoff)
	completed := w.now()
	record.CompletedAt = &completed
	if deleteErr != nil {
		result.Err = deleteErr
		record.Status = SweepFailed
		record.Detail = "delete expired facts failed"
		_ = w.store.RecordSweep(ctx, record)
		w.logger.Error("retention sweep delete failed",
			"project_id", plan.ProjectID, "environment", plan.Environment, "error", deleteErr.Error())
		return result
	}
	record.RowsCandidates = deleted
	result.Candidates = deleted
	record.Status = SweepSucceeded
	if err := w.store.RecordSweep(ctx, record); err != nil {
		result.Err = err
		return result
	}
	w.logger.Info("retention sweep enforced",
		"project_id", plan.ProjectID, "environment", plan.Environment,
		"retention_days", plan.RetentionDays, "deleted", deleted)
	return result
}

// Run executes RunOnce on the configured interval until the context is done.
// A whole-pass failure (e.g. policy listing unavailable) is logged and retried
// on the next tick rather than terminating the worker.
func (w *SweepWorker) Run(ctx context.Context) error {
	ticker := time.NewTicker(w.interval)
	defer ticker.Stop()
	for {
		if _, err := w.RunOnce(ctx); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			w.logger.Error("retention sweep pass failed", "error", err.Error())
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}
