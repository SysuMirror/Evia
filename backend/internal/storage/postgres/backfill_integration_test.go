//go:build integration

package postgres

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/howtomakeaname/Evia/backend/internal/backfilling"
	"github.com/howtomakeaname/Evia/backend/internal/platform/ids"
)

// seedMaterializedManifest inserts a completed archive manifest the backfill
// jobs can reference.
func seedMaterializedManifest(t *testing.T, f catalogFixture, environment string) string {
	t.Helper()
	now := time.Now().UTC()
	cutoff := now.AddDate(0, 0, -90)
	path := "/var/lib/evia/archives/" + ids.New() + ".jsonl"
	sha := strings.Repeat("b", 64)
	manifestID := ids.New()
	_, e := f.s.Pool.Exec(context.Background(), `INSERT INTO archive_manifests(
 id,project_id,environment,retention_days,cutoff_at,window_start_at,window_end_at,
 row_count,artifact_path,artifact_sha256,artifact_bytes,status,created_at,completed_at)
 VALUES($1,$2,$3,90,$4,$5,$6,12,$7,$8,2048,'materialized',$9,$9)`,
		manifestID, f.project, environment, cutoff, now.Add(-90*24*time.Hour), now.Add(-89*24*time.Hour),
		path, sha, now)
	if e != nil {
		t.Fatal(e)
	}
	return manifestID
}

func backfillInput(f catalogFixture, manifestID, key string) backfilling.Input {
	return backfilling.Input{
		ProjectID:      f.project,
		Environment:    "development",
		ActorID:        f.owner,
		ManifestID:     manifestID,
		Name:           "restore november",
		IdempotencyKey: key,
		RequestHash:    strings.Repeat("a", 64),
	}
}

func TestBackfillCreateRequiresMaterializedManifestAndReplays(t *testing.T) {
	f := catalogDB(t)
	ctx := context.Background()
	manifestID := seedMaterializedManifest(t, f, "development")
	in := backfillInput(f, manifestID, "bf-1")
	job, created, e := f.s.CreateBackfillJob(ctx, in)
	if e != nil || !created || job.State != backfilling.StateQueued || job.RowsExpected != 12 {
		t.Fatalf("create wrong: %+v err=%v", job, e)
	}
	replay, again, e := f.s.CreateBackfillJob(ctx, in)
	if e != nil || again || replay.JobID != job.JobID {
		t.Fatalf("idempotent replay wrong: %+v created=%v err=%v", replay, again, e)
	}
	// A foreign or non-materialized manifest is rejected at the storage boundary.
	bad := backfillInput(f, ids.New(), "bf-2")
	if _, _, e = f.s.CreateBackfillJob(ctx, bad); e == nil {
		t.Fatal("unknown manifest must be rejected")
	}
	other := backfillInput(f, manifestID, "bf-3")
	other.Environment = "staging"
	if _, _, e = f.s.CreateBackfillJob(ctx, other); e == nil {
		t.Fatal("cross-environment manifest must be rejected")
	}
}

func TestBackfillClaimCompleteFailAndLeaseGuard(t *testing.T) {
	f := catalogDB(t)
	ctx := context.Background()
	manifestID := seedMaterializedManifest(t, f, "development")
	job, _, e := f.s.CreateBackfillJob(ctx, backfillInput(f, manifestID, "bf-life"))
	if e != nil {
		t.Fatal(e)
	}
	claim, e := f.s.ClaimBackfillJob(ctx, "backfill-worker-1", time.Minute)
	if e != nil || claim == nil || claim.JobID != job.JobID || claim.State != backfilling.StateRunning || claim.RowsExpected != 12 {
		t.Fatalf("claim wrong: %+v err=%v", claim, e)
	}
	if e = f.s.CompleteBackfillJob(ctx, *claim, 12); e != nil {
		t.Fatalf("complete failed: %v", e)
	}
	got, e := f.s.GetBackfillJob(ctx, f.owner, f.project, job.JobID)
	if e != nil || got.State != backfilling.StateCompleted || got.RowsInserted != 12 || got.FinishedAt == nil {
		t.Fatalf("completed job wrong: %+v err=%v", got, e)
	}
	// A stale generation cannot complete the same job twice.
	stale := *claim
	stale.Generation++
	if e = f.s.CompleteBackfillJob(ctx, stale, 12); e == nil {
		t.Fatal("stale lease completion must be rejected")
	}
}

func TestBackfillCancelOnlyQueued(t *testing.T) {
	f := catalogDB(t)
	ctx := context.Background()
	manifestID := seedMaterializedManifest(t, f, "development")
	job, _, e := f.s.CreateBackfillJob(ctx, backfillInput(f, manifestID, "bf-cancel"))
	if e != nil {
		t.Fatal(e)
	}
	cancelled, e := f.s.CancelBackfillJob(ctx, f.owner, f.project, job.JobID)
	if e != nil || cancelled.State != backfilling.StateCancelled || cancelled.FinishedAt == nil {
		t.Fatalf("cancel wrong: %+v err=%v", cancelled, e)
	}
	if _, e = f.s.CancelBackfillJob(ctx, f.owner, f.project, job.JobID); e == nil {
		t.Fatal("terminal job cannot be cancelled twice")
	}
}

func TestBackfillListPaginationAndAuthority(t *testing.T) {
	f := catalogDB(t)
	ctx := context.Background()
	manifestID := seedMaterializedManifest(t, f, "development")
	for _, key := range []string{"bf-p1", "bf-p2", "bf-p3"} {
		in := backfillInput(f, manifestID, key)
		in.Name = "restore " + key
		if _, _, e := f.s.CreateBackfillJob(ctx, in); e != nil {
			t.Fatal(e)
		}
	}
	page, e := f.s.ListBackfillJobs(ctx, f.owner, f.project, backfilling.ListFilter{Limit: 2})
	if e != nil || len(page.Items) != 2 || page.After == "" {
		t.Fatalf("pagination wrong: %+v err=%v", page, e)
	}
	next, e := f.s.ListBackfillJobs(ctx, f.owner, f.project, backfilling.ListFilter{Limit: 2, After: page.After})
	if e != nil || len(next.Items) != 1 {
		t.Fatalf("cursor page wrong: %+v err=%v", next, e)
	}
	if _, e = f.s.ListBackfillJobs(ctx, f.viewer, f.project, backfilling.ListFilter{Limit: 10}); e == nil {
		t.Fatal("viewer must not list backfill jobs")
	}
	if _, e = f.s.ListBackfillJobs(ctx, f.outsider, f.project, backfilling.ListFilter{Limit: 10}); e == nil {
		t.Fatal("outsider must not list backfill jobs")
	}
}
