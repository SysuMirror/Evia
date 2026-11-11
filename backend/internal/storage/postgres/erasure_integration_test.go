//go:build integration

package postgres

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/howtomakeaname/Evia/backend/internal/erasure"
	"github.com/howtomakeaname/Evia/backend/internal/platform/ids"
)

// stubFactDeleter stands in for the ClickHouse executor: it records the
// worker's delete/verify scope and always reports zero remaining rows.
type stubFactDeleter struct {
	deleteCalls int
	verifyCalls int
	lastScope   [4]string
}

func (d *stubFactDeleter) DeleteIdentity(_ context.Context, project, environment, kind, value string) error {
	d.deleteCalls++
	d.lastScope = [4]string{project, environment, kind, value}
	return nil
}

func (d *stubFactDeleter) RemainingIdentityRows(_ context.Context, project, environment, kind, value string) (uint64, error) {
	d.verifyCalls++
	d.lastScope = [4]string{project, environment, kind, value}
	return 0, nil
}

func erasureInput(f catalogFixture, idempotencyKey, subjectKind, subject string) erasure.Input {
	return erasure.Input{
		ProjectID:      f.project,
		Environment:    "development",
		ActorID:        f.owner,
		SubjectKind:    subjectKind,
		SubjectValue:   subject,
		Reason:         "user requested account erasure",
		IdempotencyKey: idempotencyKey,
		RequestHash:    strings.Repeat("a", 64),
	}
}

func faultCodeErasure(t *testing.T, e error, code string) {
	t.Helper()
	faultCode(t, e, code)
}

func TestErasureJobLifecycleAuthorityAndIdempotency(t *testing.T) {
	f := catalogDB(t)
	ctx := context.Background()
	in := erasureInput(f, "erase-1", erasure.SubjectUID, "user-to-erase")

	// Non-owners are rejected before any job row exists.
	_, _, e := f.s.CreateJob(ctx, func() erasure.Input {
		blocked := in
		blocked.ActorID = f.viewer
		blocked.IdempotencyKey = "viewer-denied"
		return blocked
	}())
	faultCodeErasure(t, e, "ERASURE_FORBIDDEN")
	_, _, e = f.s.CreateJob(ctx, func() erasure.Input {
		blocked := in
		blocked.ActorID = f.outsider
		blocked.IdempotencyKey = "outsider-denied"
		return blocked
	}())
	faultCodeErasure(t, e, "PROJECT_NOT_FOUND")

	job, created, e := f.s.CreateJob(ctx, in)
	if e != nil || job.State != erasure.StateQueued || job.JobID == "" || job.Attempts != 0 || !created {
		t.Fatal(job, e, created)
	}
	// Identical replay returns the same accepted snapshot.
	replay, replayed, e := f.s.CreateJob(ctx, in)
	if e != nil || replay.JobID != job.JobID || replayed {
		t.Fatal("idempotent replay changed", replay, e, replayed)
	}
	// Same key, different request body must conflict.
	diverged := in
	diverged.RequestHash = strings.Repeat("b", 64)
	_, _, e = f.s.CreateJob(ctx, diverged)
	faultCodeErasure(t, e, "IDEMPOTENCY_CONFLICT")
	// A second in-flight job for the same target identity is rejected.
	second := in
	second.IdempotencyKey = "erase-2"
	_, _, e = f.s.CreateJob(ctx, second)
	faultCodeErasure(t, e, "ERASURE_JOB_ACTIVE")

	// Read paths enforce the same owner-only authority.
	if _, e = f.s.GetJob(ctx, f.viewer, f.project, job.JobID); e == nil || !strings.Contains(e.Error(), "ERASURE_FORBIDDEN") {
		t.Fatal("viewer could read erasure job", e)
	}
	got, e := f.s.GetJob(ctx, f.owner, f.project, job.JobID)
	if e != nil || got.JobID != job.JobID {
		t.Fatal(got, e)
	}
	page, e := f.s.ListJobs(ctx, f.owner, f.project, erasure.ListFilter{Limit: 10})
	if e != nil || len(page.Items) != 1 || page.Items[0].JobID != job.JobID {
		t.Fatal(page, e)
	}
}

func TestErasureClaimTombstoneBarrierAndStateMachine(t *testing.T) {
	f := catalogDB(t)
	ctx := context.Background()
	anonymous := ids.New()
	in := erasureInput(f, "erase-anon", erasure.SubjectAnonymousID, anonymous)
	job, created, e := f.s.CreateJob(ctx, in)
	if e != nil || !created {
		t.Fatal(e, created)
	}
	claim, e := f.s.ClaimJob(ctx, "erasure-worker-1", time.Minute)
	if e != nil || claim == nil || claim.JobID != job.JobID || claim.State != erasure.StateHiding || claim.Attempts != 1 {
		t.Fatal(claim, e)
	}
	// The tombstone is inserted while entering hiding, before any delete.
	if e = f.s.BeginHiding(ctx, *claim); e != nil {
		t.Fatal(e)
	}
	set, e := f.s.ErasedSubjects(ctx, f.project, "development", []string{"other-user"}, []string{anonymous, ids.New()})
	if e != nil {
		t.Fatal(e)
	}
	if !set.Contains(erasure.SubjectAnonymousID, anonymous) || len(set.AnonymousIDs) != 1 || len(set.UIDs) != 0 {
		t.Fatal("tombstone barrier did not resolve the erased identity", set)
	}
	// Invalid progression order is rejected against the observed claim state.
	faultCodeErasure(t, f.s.Advance(ctx, *claim, erasure.StateRebuilding), "INVALID_ERASURE_INPUT")
	if e = f.s.Advance(ctx, *claim, erasure.StatePurging); e != nil {
		t.Fatal(e)
	}
	claim.State = erasure.StatePurging
	if e = f.s.Advance(ctx, *claim, erasure.StateRebuilding); e != nil {
		t.Fatal(e)
	}
	claim.State = erasure.StateRebuilding
	// A stale lease generation cannot drive transitions.
	stale := *claim
	stale.Generation = claim.Generation + 99
	faultCodeErasure(t, f.s.Complete(ctx, stale), erasureLeaseLost)
	if e = f.s.Complete(ctx, *claim); e != nil {
		t.Fatal(e)
	}
	finished, e := f.s.GetJob(ctx, f.owner, f.project, job.JobID)
	if e != nil || finished.State != erasure.StateCompleted || finished.FinishedAt == nil {
		t.Fatal(finished, e)
	}
	// After completion a new job for the same identity is allowed; its hiding
	// step re-points the existing tombstone at the new job.
	retry := in
	retry.IdempotencyKey = "erase-anon-retry"
	retry.RequestHash = strings.Repeat("c", 64)
	retryJob, retryCreated, e := f.s.CreateJob(ctx, retry)
	if e != nil || !retryCreated {
		t.Fatal(e, retryCreated)
	}
	retryClaim, e := f.s.ClaimJob(ctx, "erasure-worker-1", time.Minute)
	if e != nil || retryClaim.JobID != retryJob.JobID {
		t.Fatal(retryClaim, e)
	}
	if e = f.s.BeginHiding(ctx, *retryClaim); e != nil {
		t.Fatal(e)
	}
	var tombstoneJob string
	if e = f.s.Pool.QueryRow(ctx, `SELECT job_id::text FROM erasure_tombstones
 WHERE project_id=$1 AND environment='development' AND subject_kind='anonymous_id' AND subject_value=$2`,
		f.project, anonymous).Scan(&tombstoneJob); e != nil || tombstoneJob != retryJob.JobID {
		t.Fatal("tombstone was not re-pointed", tombstoneJob, e)
	}
}

func TestErasureLeaseExpiryRequeueAndAttemptCap(t *testing.T) {
	f := catalogDB(t)
	ctx := context.Background()
	in := erasureInput(f, "erase-lease", erasure.SubjectUID, "lease-user")
	job, created, e := f.s.CreateJob(ctx, in)
	if e != nil || !created {
		t.Fatal(e, created)
	}
	claim, e := f.s.ClaimJob(ctx, "erasure-worker-2", time.Minute)
	if e != nil || claim.JobID != job.JobID {
		t.Fatal(claim, e)
	}
	// Simulate a dead worker: expire the lease and burn the remaining attempts.
	for i := 0; i < erasure.MaxAttempts; i++ {
		if _, e = f.s.Pool.Exec(ctx, `UPDATE erasure_jobs SET lease_until=clock_timestamp()-interval '1 second' WHERE id=$1`, job.JobID); e != nil {
			t.Fatal(e)
		}
		next, e := f.s.ClaimJob(ctx, "erasure-worker-3", time.Minute)
		if e != nil {
			t.Fatal(e)
		}
		if i == erasure.MaxAttempts-1 {
			if next != nil {
				t.Fatal("exhausted job was re-leased instead of failing")
			}
		} else if next == nil || next.JobID != job.JobID {
			t.Fatal("live lease was not requeued to a worker", next)
		}
	}
	failed, e := f.s.GetJob(ctx, f.owner, f.project, job.JobID)
	if e != nil || failed.State != erasure.StateFailed || failed.ErrorCode == nil || *failed.ErrorCode != "ERASURE_ATTEMPTS_EXHAUSTED" {
		t.Fatal(failed, e)
	}
}

func TestErasureFailKeepsTombstoneAndBarrierSetEmptyInput(t *testing.T) {
	f := catalogDB(t)
	ctx := context.Background()
	// Empty candidate sets must return an empty set without a database error.
	set, e := f.s.ErasedSubjects(ctx, f.project, "development", nil, nil)
	if e != nil || len(set.UIDs) != 0 || len(set.AnonymousIDs) != 0 {
		t.Fatal(set, e)
	}
	in := erasureInput(f, "erase-fail", erasure.SubjectUID, "fail-user")
	job, created, e := f.s.CreateJob(ctx, in)
	if e != nil || !created {
		t.Fatal(e, created)
	}
	claim, e := f.s.ClaimJob(ctx, "erasure-worker-4", time.Minute)
	if e != nil {
		t.Fatal(e)
	}
	if e = f.s.BeginHiding(ctx, *claim); e != nil {
		t.Fatal(e)
	}
	if e = f.s.Fail(ctx, *claim, "ERASURE_CLICKHOUSE_UNAVAILABLE"); e != nil {
		t.Fatal(e)
	}
	failed, e := f.s.GetJob(ctx, f.owner, f.project, job.JobID)
	if e != nil || failed.State != erasure.StateFailed || failed.FinishedAt == nil {
		t.Fatal(failed, e)
	}
	// The barrier survives failure so retries and replays stay fail-closed.
	set, e = f.s.ErasedSubjects(ctx, f.project, "development", []string{"fail-user"}, nil)
	if e != nil || !set.Contains(erasure.SubjectUID, "fail-user") {
		t.Fatal("tombstone lost after job failure", set, e)
	}
}

// TestErasureWorkerDrivesJobToCompletionOnPostgres pins the worker/store state
// contract end to end: claim.State must still hold the current lifecycle state
// when Advance is called, and only adopt the next state after it succeeds.
func TestErasureWorkerDrivesJobToCompletionOnPostgres(t *testing.T) {
	f := catalogDB(t)
	ctx := context.Background()
	in := erasureInput(f, "erase-worker", erasure.SubjectUID, "worker-user")
	job, created, e := f.s.CreateJob(ctx, in)
	if e != nil || !created {
		t.Fatal(e, created)
	}
	deleter := &stubFactDeleter{}
	worker := &erasure.Worker{
		Store:   f.s,
		Deleter: deleter,
		ID:      "erasure-worker-it",
		Lease:   time.Minute,
		Logger:  slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	worked, e := worker.RunOnce(ctx)
	if e != nil || !worked {
		t.Fatal(worked, e)
	}
	finished, e := f.s.GetJob(ctx, f.owner, f.project, job.JobID)
	if e != nil || finished.State != erasure.StateCompleted || finished.FinishedAt == nil || finished.ErrorCode != nil {
		t.Fatal(finished, e)
	}
	if deleter.deleteCalls != 1 || deleter.verifyCalls != 1 {
		t.Fatalf("deleter call counts wrong: %+v", deleter)
	}
	if deleter.lastScope != [4]string{f.project, "development", erasure.SubjectUID, "worker-user"} {
		t.Fatalf("deleter scope wrong: %v", deleter.lastScope)
	}
	// The durable barrier remains in place after successful completion.
	set, e := f.s.ErasedSubjects(ctx, f.project, "development", []string{"worker-user"}, nil)
	if e != nil || !set.Contains(erasure.SubjectUID, "worker-user") {
		t.Fatal("tombstone missing after worker completion", set, e)
	}
	// The queue is now drained.
	worked, e = worker.RunOnce(ctx)
	if e != nil || worked {
		t.Fatal("drained queue should report no work", worked, e)
	}
}
