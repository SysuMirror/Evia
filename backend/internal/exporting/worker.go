package exporting

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/howtomakeaname/Evia/backend/internal/analysis"
	"github.com/howtomakeaname/Evia/backend/internal/platform/fault"
)

// QueryEngine is the bounded analysis execution surface the worker needs. It
// matches analysis.Engine so the production ClickHouse adapter plugs in
// directly.
type QueryEngine interface {
	Execute(ctx context.Context, scope analysis.Scope, request analysis.Request) (analysis.Result, error)
}

// Worker drains queued export jobs, executes their bounded analysis requests,
// materializes JSON artifacts and records the durable outcome.
type Worker struct {
	Store     Store
	Engine    QueryEngine
	Artifacts ArtifactStore
	ID        string
	Lease     time.Duration
	Logger    *slog.Logger
}

const (
	defaultLease   = 2 * time.Minute
	executionLimit = 60 * time.Second
	pollInterval   = 250 * time.Millisecond
)

func (w *Worker) logger() *slog.Logger {
	if w.Logger == nil {
		return slog.Default()
	}
	return w.Logger
}

func (w *Worker) lease() time.Duration {
	if w.Lease == 0 {
		return defaultLease
	}
	return w.Lease
}

// Run polls until the context is cancelled. Lease-loss errors are transient:
// another worker owns the job, so the loop continues rather than crashing.
func (w *Worker) Run(ctx context.Context) error {
	if w.Store == nil || w.Engine == nil || w.Artifacts == nil {
		return errors.New("export worker requires store, engine and artifact store")
	}
	for {
		if e := ctx.Err(); e != nil {
			return e
		}
		worked, e := w.RunOnce(ctx)
		if e != nil && !errors.Is(e, context.Canceled) {
			var known *fault.Error
			if !errors.As(e, &known) || known.Code != LeaseLostCode {
				return e
			}
		}
		if !worked {
			timer := time.NewTimer(pollInterval)
			select {
			case <-ctx.Done():
				timer.Stop()
				return ctx.Err()
			case <-timer.C:
			}
		}
	}
}

func (w *Worker) RunOnce(ctx context.Context) (bool, error) {
	claim, e := w.Store.ClaimExportJob(ctx, w.ID, w.lease())
	if e != nil {
		return false, e
	}
	if claim == nil {
		return false, nil
	}
	execution, cancel := context.WithTimeout(ctx, executionLimit)
	defer cancel()
	result, e := w.Engine.Execute(execution, analysis.Scope{ProjectID: claim.ProjectID, Environment: claim.Environment}, claim.Request)
	cancel()
	if ctx.Err() != nil {
		return true, ctx.Err()
	}
	if e != nil {
		code := "QUERY_EXECUTION_FAILED"
		var known *fault.Error
		if errors.As(e, &known) {
			code = known.Code
		}
		if errors.Is(e, context.DeadlineExceeded) {
			code = "QUERY_TIMEOUT"
		}
		w.logger().Warn("export query failed", "job", claim.JobID, "code", code)
		if failErr := w.Store.FailExportJob(ctx, *claim, code); failErr != nil {
			return true, failErr
		}
		return true, nil
	}
	artifact, e := w.Artifacts.Write(ctx, claim.JobID, claim.Format, result)
	if e != nil {
		code := "EXPORT_ARTIFACT_WRITE_FAILED"
		var known *fault.Error
		if errors.As(e, &known) {
			code = known.Code
		}
		if failErr := w.Store.FailExportJob(ctx, *claim, code); failErr != nil {
			return true, failErr
		}
		return true, nil
	}
	if e = w.Store.CompleteExportJob(ctx, *claim, artifact, claim.ExpiresAt); e != nil {
		return true, e
	}
	w.logger().Info("export job completed", "job", claim.JobID, "rows", artifact.Rows, "bytes", artifact.Bytes)
	return true, nil
}
