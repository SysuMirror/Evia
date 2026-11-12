//go:build integration

package postgres

import (
	"context"
	"strings"
	"testing"

	"github.com/howtomakeaname/Evia/backend/internal/access"
	"github.com/howtomakeaname/Evia/backend/internal/platform/fault"
	"github.com/howtomakeaname/Evia/backend/internal/quotas"
)

func quotaInput(f catalogFixture, key, environment string, rate float64, burst int) quotas.Input {
	return quotas.Input{
		ProjectID:      f.project,
		ActorID:        f.owner,
		Environment:    environment,
		Rate:           rate,
		Burst:          burst,
		IdempotencyKey: key,
		RequestHash:    strings.Repeat("a", 64),
	}
}

func TestCollectionQuotaAuthority(t *testing.T) {
	f := catalogDB(t)
	ctx := context.Background()
	svc := quotas.NewService(f.s)

	machine := access.WithIdentity(ctx, access.Identity{PrincipalID: f.owner, CredentialID: "cred-1"})
	if _, _, e := svc.Get(machine, f.owner, f.project, "development"); e == nil || faultPublicCode(e) != "MACHINE_FORBIDDEN" {
		t.Fatalf("machine read must be forbidden, got %v", e)
	}
	if _, _, e := svc.Put(machine, quotaInput(f, "m1", "development", 10, 50), quotas.CreateRevision); e == nil || faultPublicCode(e) != "MACHINE_FORBIDDEN" {
		t.Fatalf("machine write must be forbidden, got %v", e)
	}

	viewerInput := quotaInput(f, "viewer-denied", "development", 10, 50)
	viewerInput.ActorID = f.viewer
	if _, _, e := svc.Put(ctx, viewerInput, quotas.CreateRevision); e == nil || faultPublicCode(e) != "QUOTA_FORBIDDEN" {
		t.Fatalf("viewer write must be forbidden, got %v", e)
	}
	if _, _, e := svc.Get(ctx, f.outsider, f.project, "development"); e == nil || faultPublicCode(e) != "PROJECT_NOT_FOUND" {
		t.Fatalf("outsider read must be invisible, got %v", e)
	}
}

func TestCollectionQuotaRevisionIdempotencyAndAudit(t *testing.T) {
	f := catalogDB(t)
	ctx := context.Background()
	svc := quotas.NewService(f.s)
	env := "staging"

	if _, configured, e := svc.Get(ctx, f.owner, f.project, env); e != nil || configured {
		t.Fatalf("unconfigured scope: configured=%v err=%v", configured, e)
	}

	first, replayed, e := svc.Put(ctx, quotaInput(f, "key-1", env, 12.5, 200), quotas.CreateRevision)
	if e != nil || replayed {
		t.Fatalf("first write must not be a replay: %v", e)
	}
	if e != nil || first.Revision != 1 || first.Rate != 12.5 || first.Burst != 200 || first.Environment != env || first.CreatedAt == "" || first.UpdatedAt == "" {
		t.Fatal(first, e)
	}
	if got, configured, e := svc.Get(ctx, f.owner, f.project, env); e != nil || !configured || got.Burst != 200 || got.Revision != 1 {
		t.Fatalf("read after create wrong: %+v configured=%v err=%v", got, configured, e)
	}

	if _, _, e = svc.Put(ctx, quotaInput(f, "key-2", env, 13, 200), quotas.CreateRevision); e == nil || faultPublicCode(e) != "QUOTA_REVISION_CONFLICT" {
		t.Fatalf("duplicate create must conflict, got %v", e)
	}

	replay, wasReplay, e := svc.Put(ctx, quotaInput(f, "key-1", env, 12.5, 200), quotas.CreateRevision)
	if e != nil || !wasReplay || replay != first {
		t.Fatalf("idempotent replay changed snapshot: %+v vs %+v err=%v", replay, first, e)
	}
	divergent := quotaInput(f, "key-1", env, 99, 200)
	divergent.RequestHash = strings.Repeat("b", 64)
	if _, _, e = svc.Put(ctx, divergent, 1); e == nil || faultPublicCode(e) != "IDEMPOTENCY_CONFLICT" {
		t.Fatalf("divergent idempotency key must conflict, got %v", e)
	}

	updated, updatedReplay, e := svc.Put(ctx, quotaInput(f, "key-3", env, 30, 400), 1)
	if e != nil || updatedReplay {
		t.Fatalf("update must not be a replay: %v", e)
	}
	if e != nil || updated.Revision != 2 || updated.Rate != 30 || updated.Burst != 400 {
		t.Fatalf("optimistic update wrong: %+v err=%v", updated, e)
	}
	if _, _, e = svc.Put(ctx, quotaInput(f, "key-4", env, 31, 400), 1); e == nil || faultPublicCode(e) != "QUOTA_REVISION_CONFLICT" {
		t.Fatalf("stale revision must conflict, got %v", e)
	}

	var auditCount int
	if e = f.s.Pool.QueryRow(ctx, `SELECT count(*) FROM audit_entries
 WHERE project_id=$1 AND action='collection.quota_updated' AND resource_key=$2`, f.project, env).Scan(&auditCount); e != nil {
		t.Fatal(e)
	}
	if auditCount != 2 {
		t.Fatalf("expected two audit entries, got %d", auditCount)
	}
}

func TestCollectionQuotaListLoaderSeesEveryScope(t *testing.T) {
	f := catalogDB(t)
	ctx := context.Background()
	svc := quotas.NewService(f.s)
	if _, _, e := svc.Put(ctx, quotaInput(f, "d1", "development", 5, 50), quotas.CreateRevision); e != nil {
		t.Fatal(e)
	}
	if _, _, e := svc.Put(ctx, quotaInput(f, "p1", "production", 50, 500), quotas.CreateRevision); e != nil {
		t.Fatal(e)
	}
	rows, e := f.s.ListQuotas(ctx)
	if e != nil {
		t.Fatal(e)
	}
	scopes := map[string]quotas.Policy{}
	for _, row := range rows {
		if row.ProjectID == f.project {
			scopes[row.Environment] = row
		}
	}
	if len(scopes) != 2 || scopes["development"].Burst != 50 || scopes["production"].Rate != 50 {
		t.Fatalf("global loader missed scopes: %+v", scopes)
	}
}

func TestCollectionQuotaRejectsBoundsAtServiceLayer(t *testing.T) {
	f := catalogDB(t)
	svc := quotas.NewService(f.s)
	for _, in := range []quotas.Input{
		quotaInput(f, "bad-rate", "development", 0, 50),
		quotaInput(f, "bad-burst", "development", 10, 49),
	} {
		if _, _, e := svc.Put(context.Background(), in, quotas.CreateRevision); e == nil || fault.Public(e).Kind != fault.Invalid {
			t.Fatalf("out-of-bounds quota must be invalid: %+v err=%v", in, e)
		}
	}
}
