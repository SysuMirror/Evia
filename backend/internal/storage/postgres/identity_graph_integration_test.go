//go:build integration

package postgres

import (
	"context"
	"testing"
	"time"

	"github.com/howtomakeaname/Evia/backend/internal/identity"
)

type identityFixture struct {
	catalogFixture
	scope identity.Scope
}

func identityDB(t *testing.T) identityFixture {
	t.Helper()
	f := catalogDB(t)
	ctx := context.Background()
	// A scope becomes foldable once a release activation exists for it.
	if _, e := f.s.Pool.Exec(ctx,
		`INSERT INTO catalog_releases(id,project_id,environment,revision,state,manifest_hash,manifest,created_by)
		 VALUES($1,$2,'development',1,'published',repeat('a',64),'{}'::jsonb,$3)`,
		f.project, f.project, f.owner); e != nil {
		t.Fatal(e)
	}
	if _, e := f.s.Pool.Exec(ctx,
		`INSERT INTO catalog_release_activations(project_id,environment,release_id,revision)
		 VALUES($1,'development',$2,1)`,
		f.project, f.project); e != nil {
		t.Fatal(e)
	}
	return identityFixture{catalogFixture: f, scope: identity.Scope{ProjectID: f.project, Environment: "development"}}
}

func (f identityFixture) foldOnce(t *testing.T, scans []identity.LinkScan) identity.FoldOutcome {
	t.Helper()
	ctx := context.Background()
	claim, ok, e := f.s.ClaimScope(ctx, f.scope, "identity-test", time.Minute)
	if e != nil || !ok || claim == nil {
		t.Fatalf("claim failed: %v %v", ok, e)
	}
	outcome := identity.Fold(f.scope, claim.Graph, scans, claim.NextRevision, time.Now().UTC(), identity.NewPersonID)
	if outcome.Changed {
		if e = f.s.CommitFold(ctx, claim, outcome); e != nil {
			t.Fatalf("commit failed: %v", e)
		}
	} else if e = f.s.ReleaseFold(ctx, claim); e != nil {
		t.Fatalf("release failed: %v", e)
	}
	return outcome
}

func linkScan(uid, anon string, at time.Time) identity.LinkScan {
	return identity.LinkScan{UID: uid, AnonymousID: anon, FirstSeenAt: at, LastSeenAt: at}
}

func TestIdentityFoldBuildsMergesAndServesRevisions(t *testing.T) {
	f := identityDB(t)
	ctx := context.Background()
	scopes, e := f.s.FoldScopes(ctx)
	if e != nil || len(scopes) != 1 || scopes[0] != f.scope {
		t.Fatalf("scope discovery wrong: %v %v", scopes, e)
	}
	observed := time.Now().UTC().Add(-time.Minute)
	first := f.foldOnce(t, []identity.LinkScan{
		linkScan("u1", "11111111-1111-4111-8111-111111111111", observed),
		linkScan("u2", "22222222-2222-4222-8222-222222222222", observed),
	})
	if !first.Changed || first.EdgeCount != 2 || first.ActivePersonCount != 2 || first.MemberCount != 4 {
		t.Fatalf("first fold wrong: %+v", first)
	}
	// Overlap rescan of identical pairs at identical reception times must not
	// mint a revision.
	if again := f.foldOnce(t, []identity.LinkScan{
		linkScan("u1", "11111111-1111-4111-8111-111111111111", observed),
		linkScan("u2", "22222222-2222-4222-8222-222222222222", observed),
	}); again.Changed {
		t.Fatalf("overlap rescan changed the graph: %+v", again)
	}
	// A bridge pair joins both components: the older person survives.
	bridge := f.foldOnce(t, []identity.LinkScan{
		linkScan("u2", "11111111-1111-4111-8111-111111111111", observed.Add(time.Minute)),
	})
	if !bridge.Changed || len(bridge.Retire) != 1 || bridge.Retire[0].SupersededBy == nil {
		t.Fatalf("merge fold wrong: %+v", bridge)
	}
	survivor := *bridge.Retire[0].SupersededBy

	page, e := f.s.ListRevisions(ctx, f.owner, f.project, "development", identity.ReadFilter{Limit: 10})
	if e != nil || len(page.Items) != 2 {
		t.Fatalf("revision list wrong: %v %v", page.Items, e)
	}
	if page.Items[0].Revision != 2 || page.Items[0].State != identity.RevisionCompleted {
		t.Fatalf("newest revision wrong: %+v", page.Items[0])
	}
	if page.Items[0].ActivePersonCount != 1 || page.Items[0].MemberCount != 4 {
		t.Fatalf("post-merge counts wrong: %+v", page.Items[0])
	}
	detail, e := f.s.GetRevision(ctx, f.owner, f.project, "development", 2)
	if e != nil || detail.Revision != 2 || detail.Watermark == nil {
		t.Fatalf("revision detail wrong: %+v %v", detail, e)
	}

	// Resolve against the latest completed revision follows the redirect and
	// returns the merged membership.
	resolution, e := f.s.ResolveMember(ctx, f.owner, f.project, "development", 2, identity.MemberUID, "u2")
	if e != nil || resolution.ResolvedPersonID != survivor || len(resolution.Members) != 4 {
		t.Fatalf("resolution wrong: %+v %v", resolution, e)
	}
	// Historical revision pins fail closed until member snapshots exist.
	if _, e = f.s.ResolveMember(ctx, f.owner, f.project, "development", 1, identity.MemberUID, "u2"); e == nil {
		t.Fatal("old revision must not resolve")
	}

	// The watermark only moves with a committed revision.
	var watermark *time.Time
	if e = f.s.Pool.QueryRow(ctx, `SELECT scan_watermark FROM identity_fold_state WHERE project_id=$1 AND environment='development'`, f.project).Scan(&watermark); e != nil {
		t.Fatal(e)
	}
	if watermark == nil || watermark.Before(bridge.Watermark.Add(-time.Minute)) {
		t.Fatalf("watermark not advanced: %v", watermark)
	}
}

func TestIdentityClaimIsExclusiveAndRecoverable(t *testing.T) {
	f := identityDB(t)
	ctx := context.Background()
	first, ok, e := f.s.ClaimScope(ctx, f.scope, "worker-a", time.Minute)
	if e != nil || !ok {
		t.Fatalf("first claim failed: %v %v", ok, e)
	}
	if _, second, e := f.s.ClaimScope(ctx, f.scope, "worker-b", time.Minute); e != nil || second {
		t.Fatalf("second concurrent claim must be refused: %v %v", second, e)
	}
	// A stale commit after release loses the lease guard.
	if e = f.s.ReleaseFold(ctx, first); e != nil {
		t.Fatal(e)
	}
	scans := []identity.LinkScan{linkScan("u1", "11111111-1111-4111-8111-111111111111", time.Now().UTC())}
	outcome := identity.Fold(f.scope, first.Graph, scans, first.NextRevision, time.Now().UTC(), identity.NewPersonID)
	if e = f.s.CommitFold(ctx, first, outcome); e == nil {
		t.Fatal("commit after release must fail closed")
	}
	// The lease is claimable again after release.
	if _, ok, e := f.s.ClaimScope(ctx, f.scope, "worker-c", time.Minute); e != nil || !ok {
		t.Fatalf("reclaim after release failed: %v %v", ok, e)
	}
}

func TestIdentityRecordFoldFailureParksDiagnostics(t *testing.T) {
	f := identityDB(t)
	ctx := context.Background()
	claim, ok, e := f.s.ClaimScope(ctx, f.scope, "identity-test", time.Minute)
	if e != nil || !ok {
		t.Fatalf("claim failed: %v", e)
	}
	if e = f.s.RecordFoldFailure(ctx, claim, "IDENTITY_FOLD_COMMIT_FAILED"); e != nil {
		t.Fatal(e)
	}
	revision, e := f.s.GetRevision(ctx, f.owner, f.project, "development", 1)
	if e != nil || revision.State != identity.RevisionFailed || revision.ErrorCode == nil || *revision.ErrorCode != "IDENTITY_FOLD_COMMIT_FAILED" {
		t.Fatalf("failed revision wrong: %+v %v", revision, e)
	}
	var watermark *time.Time
	if e = f.s.Pool.QueryRow(ctx, `SELECT scan_watermark FROM identity_fold_state WHERE project_id=$1`, f.project).Scan(&watermark); e != nil || watermark != nil {
		t.Fatalf("failed fold must not move the watermark: %v %v", watermark, e)
	}
}

func TestIdentityReadAuthorizationBoundaries(t *testing.T) {
	f := identityDB(t)
	ctx := context.Background()
	f.foldOnce(t, []identity.LinkScan{linkScan("u1", "11111111-1111-4111-8111-111111111111", time.Now().UTC())})
	if _, e := f.s.ListRevisions(ctx, f.viewer, f.project, "development", identity.ReadFilter{}); e != nil {
		t.Fatalf("member viewer must read revisions: %v", e)
	}
	if _, e := f.s.ListRevisions(ctx, f.outsider, f.project, "development", identity.ReadFilter{}); e == nil {
		t.Fatal("outsider must not see the project")
	}
	// Raw identity resolution requires the raw-read authority: a viewer-role
	// human is refused, the owner resolves.
	if _, e := f.s.ResolveMember(ctx, f.viewer, f.project, "development", 1, identity.MemberUID, "u1"); e == nil {
		t.Fatal("viewer must not resolve raw identities")
	}
	if _, e := f.s.ResolveMember(ctx, f.owner, f.project, "development", 1, identity.MemberUID, "u1"); e != nil {
		t.Fatalf("owner must resolve raw identities: %v", e)
	}
}

func TestIdentityFoldBarrierSkipsTombstonedIdentities(t *testing.T) {
	f := identityDB(t)
	ctx := context.Background()
	// One durable tombstone through the real erasure tables.
	if _, e := f.s.Pool.Exec(ctx, `INSERT INTO erasure_jobs(id,project_id,environment,actor_id,subject_kind,subject_value,reason,state,idempotency_key,request_hash)
		 VALUES($1,$2,'development',$3,'uid','blocked-user','synthetic','completed','identity-barrier-1',repeat('c',64))`,
		"33333333-3333-4333-8333-333333333333", f.project, f.owner); e != nil {
		t.Fatal(e)
	}
	if _, e := f.s.Pool.Exec(ctx, `INSERT INTO erasure_tombstones(project_id,environment,subject_kind,subject_value,job_id)
		 VALUES($1,'development','uid','blocked-user',$2)`, f.project, "33333333-3333-4333-8333-333333333333"); e != nil {
		t.Fatal(e)
	}
	erased, e := f.s.Erased(ctx, f.scope, []string{"blocked-user", "u1"}, nil)
	if e != nil || !erased.Contains(identity.MemberUID, "blocked-user") || erased.Contains(identity.MemberUID, "u1") {
		t.Fatalf("barrier predicate wrong: %+v %v", erased, e)
	}
	// The barrier is enforced by the fold worker, not the pure fold, so drive
	// the real store through the worker with synthetic warehouse rows.
	facts := &fixedLinkFacts{links: []identity.LinkScan{
		linkScan("blocked-user", "44444444-4444-4444-8444-444444444444", time.Now().UTC()),
		linkScan("u1", "11111111-1111-4111-8111-111111111111", time.Now().UTC()),
	}}
	worker := identity.NewWorker(f.s, facts, f.s, identity.NoopObserver{}, identity.WorkerOptions{Worker: "barrier-test"})
	worker.RunOnce(ctx)
	var edges int
	if e := f.s.Pool.QueryRow(ctx, `SELECT count(*) FROM identity_edges WHERE project_id=$1`, f.project).Scan(&edges); e != nil {
		t.Fatal(e)
	}
	if edges != 1 {
		t.Fatalf("only the surviving pair may enter the graph, got %d edges", edges)
	}
}

type fixedLinkFacts struct {
	links []identity.LinkScan
}

func (f *fixedLinkFacts) ScanLinks(_ context.Context, _ identity.Scope, _ time.Time, _ int) ([]identity.LinkScan, bool, error) {
	return f.links, true, nil
}
