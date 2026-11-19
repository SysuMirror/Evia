//go:build integration

package postgres

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"
	"time"

	"github.com/howtomakeaname/Evia/backend/internal/analysis"
	"github.com/howtomakeaname/Evia/backend/internal/exporting"
)

func exportInput(f catalogFixture, key string) exporting.Input {
	return exporting.Input{
		ProjectID:      f.project,
		Environment:    "development",
		ActorID:        f.owner,
		Name:           "weekly export " + key,
		IdempotencyKey: key,
		RequestHash:    strings.Repeat("a", 64),
		Request: analysis.Request{
			Kind:        "events",
			Environment: "development",
			TimeRange: analysis.TimeRange{
				From: time.Now().Add(-24 * time.Hour),
				To:   time.Now(),
			},
		},
	}
}

func TestExportJobAuthorityIdempotencyAndLifecycle(t *testing.T) {
	f := catalogDB(t)
	ctx := context.Background()
	in := exportInput(f, "export-1")

	// Viewers and outsiders cannot create exports.
	blocked := in
	blocked.ActorID = f.viewer
	blocked.IdempotencyKey = "viewer-denied"
	if _, _, e := f.s.CreateExportJob(ctx, blocked); e == nil {
		t.Fatal("viewer export creation must be rejected")
	}
	blocked.ActorID = f.outsider
	blocked.IdempotencyKey = "outsider-denied"
	if _, _, e := f.s.CreateExportJob(ctx, blocked); e == nil {
		t.Fatal("outsider export creation must be rejected")
	}

	job, created, e := f.s.CreateExportJob(ctx, in)
	if e != nil || !created || job.State != exporting.StateQueued || job.Request.Kind != analysis.QueryKindEvents {
		t.Fatal(job, e, created)
	}
	// Idempotent replay and conflict on reused key with another body.
	replay, replayed, e := f.s.CreateExportJob(ctx, in)
	if e != nil || replay.JobID != job.JobID || replayed {
		t.Fatal("idempotent replay changed", replay, e, replayed)
	}
	diverged := in
	diverged.RequestHash = strings.Repeat("b", 64)
	if _, _, e = f.s.CreateExportJob(ctx, diverged); e == nil {
		t.Fatal("idempotency conflict not detected")
	}

	// Cancellation only applies to queued jobs.
	cancelled, e := f.s.CancelExportJob(ctx, f.owner, f.project, job.JobID)
	if e != nil || cancelled.State != exporting.StateCancelled || cancelled.FinishedAt == nil ||
		cancelled.Request.Kind != analysis.QueryKindEvents {
		t.Fatal(cancelled, e)
	}
	if _, e = f.s.CancelExportJob(ctx, f.owner, f.project, job.JobID); e == nil {
		t.Fatal("cancelling a terminal job must fail")
	}
}

func TestExportCSVFormatPersistedAndReplayed(t *testing.T) {
	f := catalogDB(t)
	ctx := context.Background()
	in := exportInput(f, "export-csv")
	in.Format = exporting.FormatCSV
	job, created, e := f.s.CreateExportJob(ctx, in)
	if e != nil || !created || job.Format != exporting.FormatCSV {
		t.Fatalf("csv job not persisted: %+v err=%v", job, e)
	}
	// Idempotent replay keeps the same format snapshot.
	replay, _, e := f.s.CreateExportJob(ctx, in)
	if e != nil || replay.Format != exporting.FormatCSV {
		t.Fatalf("csv replay mismatch: %+v err=%v", replay, e)
	}
	claim, e := f.s.ClaimExportJob(ctx, "export-worker-csv", time.Minute)
	if e != nil || claim == nil || claim.Format != exporting.FormatCSV {
		t.Fatalf("claimed csv job lost format: %+v err=%v", claim, e)
	}
	// The closed vocabulary is enforced at the storage boundary.
	bad := exportInput(f, "export-bad-format")
	bad.Format = "parquet"
	if _, _, e = f.s.CreateExportJob(ctx, bad); e == nil {
		t.Fatal("unsupported format must be rejected")
	}
}

func TestExportNDJSONFormatPersistedAndReplayed(t *testing.T) {
	f := catalogDB(t)
	ctx := context.Background()
	in := exportInput(f, "export-ndjson")
	in.Format = exporting.FormatNDJSON
	job, created, e := f.s.CreateExportJob(ctx, in)
	if e != nil || !created || job.Format != exporting.FormatNDJSON {
		t.Fatalf("ndjson job not persisted through migration CHECK: %+v err=%v", job, e)
	}
	replay, _, e := f.s.CreateExportJob(ctx, in)
	if e != nil || replay.Format != exporting.FormatNDJSON {
		t.Fatalf("ndjson replay mismatch: %+v err=%v", replay, e)
	}
	claim, e := f.s.ClaimExportJob(ctx, "export-worker-ndjson", time.Minute)
	if e != nil || claim == nil || claim.Format != exporting.FormatNDJSON {
		t.Fatalf("claimed ndjson job lost format: %+v err=%v", claim, e)
	}
}

func TestExportClaimCompleteFailAndLeaseGuard(t *testing.T) {
	f := catalogDB(t)
	ctx := context.Background()
	job, _, e := f.s.CreateExportJob(ctx, exportInput(f, "export-claim"))
	if e != nil {
		t.Fatal(e)
	}
	claim, e := f.s.ClaimExportJob(ctx, "export-worker-1", time.Minute)
	if e != nil || claim == nil || claim.JobID != job.JobID || claim.State != exporting.StateRunning || claim.Attempts != 1 {
		t.Fatal(claim, e)
	}
	// 回归：claim 扫描必须还原 request_json，否则 worker 会以零值请求编译失败。
	if claim.Request.Kind != analysis.QueryKindEvents || claim.Request.Environment != "development" || claim.Request.TimeRange.From.IsZero() {
		t.Fatalf("claimed job lost bounded request payload: %+v", claim.Request)
	}
	// Empty queue reports nil without an error.
	empty, e := f.s.ClaimExportJob(ctx, "export-worker-1", time.Minute)
	if e != nil || empty != nil {
		t.Fatal(empty, e)
	}
	// A stale generation cannot complete the job.
	stale := *claim
	stale.Generation = claim.Generation + 99
	err := f.s.CompleteExportJob(ctx, stale, exporting.Artifact{Path: "/tmp/x.json", SHA256: strings.Repeat("c", 64), Bytes: 12, Rows: 1}, nil)
	if err == nil {
		t.Fatal("stale lease must not complete an export job")
	}
	expiry := time.Now().Add(time.Hour)
	artifact := exporting.Artifact{Path: "/var/exports/" + job.JobID + ".json", SHA256: strings.Repeat("d", 64), Bytes: 2048, Rows: 7}
	if e = f.s.CompleteExportJob(ctx, *claim, artifact, &expiry); e != nil {
		t.Fatal(e)
	}
	finished, e := f.s.GetExportJob(ctx, f.owner, f.project, job.JobID)
	if e != nil || finished.State != exporting.StateCompleted || finished.ArtifactPath == nil ||
		*finished.ArtifactSHA256 != artifact.SHA256 || *finished.ResultRows != 7 || finished.ExpiresAt == nil ||
		finished.Request.Kind != analysis.QueryKindEvents {
		t.Fatalf("completed job wrong: %+v", finished)
	}
}

func TestExportFailJobRecordsCode(t *testing.T) {
	f := catalogDB(t)
	ctx := context.Background()
	job, _, e := f.s.CreateExportJob(ctx, exportInput(f, "export-fail"))
	if e != nil {
		t.Fatal(e)
	}
	claim, e := f.s.ClaimExportJob(ctx, "export-worker-2", time.Minute)
	if e != nil || claim.JobID != job.JobID {
		t.Fatal(claim, e)
	}
	if e = f.s.FailExportJob(ctx, *claim, "QUERY_EXECUTION_FAILED"); e != nil {
		t.Fatal(e)
	}
	failed, e := f.s.GetExportJob(ctx, f.owner, f.project, job.JobID)
	if e != nil || failed.State != exporting.StateFailed || failed.ErrorCode == nil || *failed.ErrorCode != "QUERY_EXECUTION_FAILED" {
		t.Fatalf("failed job wrong: %+v", failed)
	}
}

func TestExportDownloadGrantOneTimeRedemption(t *testing.T) {
	f := catalogDB(t)
	ctx := context.Background()
	job, _, e := f.s.CreateExportJob(ctx, exportInput(f, "export-grant"))
	if e != nil {
		t.Fatal(e)
	}
	// Grants require a completed job.
	claim, e := f.s.ClaimExportJob(ctx, "export-worker-3", time.Minute)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = f.s.CreateExportGrant(ctx, f.owner, f.project, job.JobID, strings.Repeat("a", 64), time.Now().Add(time.Minute)); e == nil {
		t.Fatal("grant for a running job must be rejected")
	}
	if e = f.s.CompleteExportJob(ctx, *claim, exporting.Artifact{Path: "/tmp/g.json", SHA256: strings.Repeat("e", 64), Bytes: 9, Rows: 1}, nil); e != nil {
		t.Fatal(e)
	}
	token := strings.Repeat("f", 64)
	grant, e := f.s.CreateExportGrant(ctx, f.owner, f.project, job.JobID, token, time.Now().Add(15*time.Minute))
	if e != nil || grant.JobID != job.JobID || grant.RedeemedAt != nil {
		t.Fatal(grant, e)
	}
	// System-level job read used by the download endpoint works without actor.
	systemJob, e := f.s.GetSystemExportJob(ctx, f.project, job.JobID)
	if e != nil || systemJob.State != exporting.StateCompleted || systemJob.Request.Kind != analysis.QueryKindEvents {
		t.Fatal(systemJob, e)
	}
	redeemed, e := f.s.RedeemExportGrant(ctx, token)
	if e != nil || redeemed == nil || redeemed.RedeemedAt == nil {
		t.Fatal(redeemed, e)
	}
	// Second redemption of the same one-time token must return nil.
	again, e := f.s.RedeemExportGrant(ctx, token)
	if e != nil || again != nil {
		t.Fatal("one-time grant redeemed twice", again, e)
	}
	// Unknown and malformed tokens never resolve.
	unknown := sha256.Sum256([]byte("never-issued"))
	if unknownGrant, e := f.s.RedeemExportGrant(ctx, hex.EncodeToString(unknown[:])); e != nil || unknownGrant != nil {
		t.Fatal("unknown token resolved", unknownGrant, e)
	}
	if malformed, e := f.s.RedeemExportGrant(ctx, "short"); e != nil || malformed != nil {
		t.Fatal("malformed token resolved", malformed, e)
	}
	// Every grant lifecycle event is audited.
	var auditCount int
	if e = f.s.Pool.QueryRow(ctx, `SELECT count(*) FROM audit_entries
 WHERE project_id=$1 AND action LIKE 'data.export%'`, f.project).Scan(&auditCount); e != nil || auditCount < 3 {
		t.Fatalf("audit entries missing: count=%d err=%v", auditCount, e)
	}
}

func TestExportListPaginationAndAuthority(t *testing.T) {
	f := catalogDB(t)
	ctx := context.Background()
	for i, key := range []string{"p1", "p2", "p3"} {
		in := exportInput(f, key)
		in.Name = "export " + key
		if _, _, e := f.s.CreateExportJob(ctx, in); e != nil {
			t.Fatalf("seed %d: %v", i, e)
		}
	}
	page, e := f.s.ListExportJobs(ctx, f.owner, f.project, exporting.ListFilter{Limit: 2})
	if e != nil || len(page.Items) != 2 || page.After == "" {
		t.Fatal(page, e)
	}
	next, e := f.s.ListExportJobs(ctx, f.owner, f.project, exporting.ListFilter{Limit: 2, After: page.After})
	if e != nil || len(next.Items) != 1 {
		t.Fatal(next, e)
	}
	if _, e = f.s.ListExportJobs(ctx, f.viewer, f.project, exporting.ListFilter{Limit: 2}); e == nil {
		t.Fatal("viewer list must be rejected")
	}
}
