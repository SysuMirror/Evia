package backfilling

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/howtomakeaname/Evia/backend/internal/archiving"
)

// Worker drains queued backfill jobs using lease-based claiming, mirroring the
// export worker. A single scope failure is recorded as a failed terminal while
// the queue keeps being serviced.
type Worker struct {
	ID        string
	Store     Store
	Manifests ManifestReader
	Archives  ArchiveSource
	Sink      FactSink
	Logger    *slog.Logger
}

// Run polls until the context is cancelled.
func (w *Worker) Run(ctx context.Context) error {
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		worked, e := w.RunOnce(ctx)
		if e != nil {
			if errors.Is(e, context.Canceled) {
				return e
			}
			w.logger().Warn("backfill worker cycle failed", "error", e.Error())
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

// RunOnce leases the oldest queued job and executes it.
func (w *Worker) RunOnce(ctx context.Context) (bool, error) {
	claim, e := w.Store.ClaimBackfillJob(ctx, w.ID, Lease)
	if e != nil {
		return false, e
	}
	if claim == nil {
		return false, nil
	}
	w.logger().Info("backfill job claimed", "job", claim.JobID, "manifest", claim.ManifestID)
	execution, cancel := context.WithTimeout(ctx, executionLimit)
	defer cancel()
	if code := w.execute(execution, claim); code != "" {
		w.logger().Warn("backfill job failed", "job", claim.JobID, "code", code)
		if failErr := w.Store.FailBackfillJob(ctx, *claim, code); failErr != nil {
			return true, failErr
		}
	}
	return true, nil
}

func (w *Worker) execute(ctx context.Context, claim *Claim) string {
	manifest, err := w.Manifests.GetSystemManifest(ctx, claim.ProjectID, claim.ManifestID)
	if err != nil {
		return "BACKFILL_MANIFEST_UNAVAILABLE"
	}
	if manifest.Status != archiving.StatusMaterialized || manifest.ArtifactPath == nil {
		return "BACKFILL_MANIFEST_NOT_MATERIALIZED"
	}
	if manifest.Environment != claim.Environment {
		return "BACKFILL_SCOPE_MISMATCH"
	}
	handle, err := w.Archives.OpenArtifact(ctx, *manifest.ArtifactPath)
	if err != nil {
		return "BACKFILL_ARTIFACT_UNREADABLE"
	}
	defer handle.Close()
	inserted, err := w.Sink.InsertFacts(ctx, claim.ProjectID, claim.Environment, handle)
	if err != nil {
		return "BACKFILL_INSERT_FAILED"
	}
	if inserted != manifest.RowCount || inserted != claim.RowsExpected {
		return "BACKFILL_ROW_COUNT_DRIFT"
	}
	if err = w.Store.CompleteBackfillJob(ctx, *claim, inserted); err != nil {
		w.logger().Warn("completing backfill job failed", "job", claim.JobID, "error", err.Error())
		return ""
	}
	w.logger().Info("backfill job completed", "job", claim.JobID, "rows", inserted)
	return ""
}

func (w *Worker) logger() *slog.Logger {
	if w.Logger == nil {
		return slog.Default()
	}
	return w.Logger
}
