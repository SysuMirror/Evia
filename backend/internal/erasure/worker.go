package erasure

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/howtomakeaname/Evia/backend/internal/platform/fault"
)

// FactDeleter hides and verifies identity rows in the fact store. Deletions
// must be synchronous from the caller's perspective (mutations completed) so
// that rebuilding can immediately prove zero matching rows remain.
type FactDeleter interface {
	DeleteIdentity(ctx context.Context, project, environment, subjectKind, subjectValue string) error
	RemainingIdentityRows(ctx context.Context, project, environment, subjectKind, subjectValue string) (uint64, error)
}

// Worker drives claimed jobs through the hiding -> purging -> rebuilding ->
// completed lifecycle. A step error parks the job as failed; a lost lease is
// left to the claim expiry machinery rather than overwritten by this worker.
type Worker struct {
	Store    Store
	Deleter  FactDeleter
	Observer ErasureObserver
	ID       string
	Lease    time.Duration
	Logger   *slog.Logger
}

func (w *Worker) observer() ErasureObserver {
	if w.Observer == nil {
		return NoopErasureObserver{}
	}
	return w.Observer
}

const defaultPollInterval = 250 * time.Millisecond

// Run polls until the context is canceled.
func (w *Worker) Run(ctx context.Context) error {
	for {
		if e := ctx.Err(); e != nil {
			return e
		}
		worked, e := w.RunOnce(ctx)
		if e != nil && !errors.Is(e, context.Canceled) {
			return e
		}
		if !worked {
			timer := time.NewTimer(defaultPollInterval)
			select {
			case <-ctx.Done():
				timer.Stop()
				return ctx.Err()
			case <-timer.C:
			}
		}
	}
}

// RunOnce leases one queued job and drives it to a terminal state.
func (w *Worker) RunOnce(ctx context.Context) (bool, error) {
	if w.Deleter == nil {
		return false, fault.New(fault.Unavailable, "ERASURE_STORAGE_UNAVAILABLE", "删除执行器尚未配置")
	}
	observer := w.observer()
	claim, e := w.Store.ClaimJob(ctx, w.ID, w.Lease)
	if e != nil {
		observer.ClaimPollCompleted(ClaimOutcomeError)
		return false, e
	}
	if claim == nil {
		observer.ClaimPollCompleted(ClaimOutcomeEmpty)
		return false, nil
	}
	observer.ClaimPollCompleted(ClaimOutcomeClaimed)
	started := time.Now()
	if e = w.process(ctx, claim); e != nil {
		if isLeaseLost(e) {
			w.warn("erasure job lease lost; leaving job to expiry handling", claim, e)
			observer.JobProcessed(JobOutcomeLeaseLost, "", time.Since(started))
			return true, nil
		}
		code := "ERASURE_STEP_FAILED"
		var known *fault.Error
		if errors.As(e, &known) && known.Code != "" {
			code = known.Code
		}
		if failErr := w.Store.Fail(ctx, *claim, code); failErr != nil {
			w.warn("erasure job failure could not be recorded", claim, failErr)
			observer.JobProcessed(JobOutcomeFailureRecordFail, code, time.Since(started))
			return true, failErr
		}
		w.warn("erasure job failed", claim, e)
		observer.JobProcessed(JobOutcomeFailed, code, time.Since(started))
		return true, nil
	}
	observer.JobProcessed(JobOutcomeCompleted, "", time.Since(started))
	return true, nil
}

func (w *Worker) process(ctx context.Context, claim *Claim) error {
	// 1. Hiding: persist the resurrection barrier before touching facts.
	if e := w.Store.BeginHiding(ctx, *claim); e != nil {
		return e
	}
	// 2. Purging: issue the synchronous fact delete.
	if e := w.Deleter.DeleteIdentity(ctx, claim.ProjectID, claim.Environment, claim.SubjectKind, claim.SubjectValue); e != nil {
		return fault.New(fault.Unavailable, "ERASURE_DELETE_FAILED", "事实删除执行失败")
	}
	// Advance owns the hiding -> purging guard; only adopt the new state after
	// the durable transition succeeds so retries present the current state.
	if e := w.Store.Advance(ctx, *claim, StatePurging); e != nil {
		return e
	}
	claim.State = StatePurging
	// 3. Rebuilding: verify the identity no longer appears in the fact store.
	if e := w.Store.Advance(ctx, *claim, StateRebuilding); e != nil {
		return e
	}
	claim.State = StateRebuilding
	remaining, e := w.Deleter.RemainingIdentityRows(ctx, claim.ProjectID, claim.Environment, claim.SubjectKind, claim.SubjectValue)
	if e != nil {
		return fault.New(fault.Unavailable, "ERASURE_VERIFY_FAILED", "删除结果校验失败")
	}
	if remaining != 0 {
		return fault.New(fault.Conflict, "ERASURE_ROWS_REMAIN", "删除后仍存在匹配的事实数据")
	}
	// 4. Derived state: the identity graph must forget the erased subject in
	// the same job; surviving component members re-group under fresh person
	// ids on the next fold instead of being re-merged by inference.
	if e := w.Store.RetireIdentitySubject(ctx, *claim); e != nil {
		return e
	}
	return w.Store.Complete(ctx, *claim)
}

func isLeaseLost(e error) bool {
	var known *fault.Error
	return errors.As(e, &known) && known.Code == erasureLeaseLostCode
}

const erasureLeaseLostCode = "ERASURE_LEASE_LOST"

func (w *Worker) warn(message string, claim *Claim, err error) {
	if w.Logger != nil {
		w.Logger.Warn(message, "job_id", claim.JobID, "subject", claim.SubjectKind, "error", err)
	}
}
