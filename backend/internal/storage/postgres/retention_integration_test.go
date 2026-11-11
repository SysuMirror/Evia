//go:build integration

package postgres

import (
	"context"
	"strings"
	"testing"

	"github.com/howtomakeaname/Evia/backend/internal/access"
	"github.com/howtomakeaname/Evia/backend/internal/platform/fault"
	"github.com/howtomakeaname/Evia/backend/internal/retention"
)

func retentionInput(f catalogFixture, key, environment string, days int) retention.Input {
	return retention.Input{
		ProjectID:      f.project,
		ActorID:        f.owner,
		Environment:    environment,
		RetentionDays:  days,
		IdempotencyKey: key,
		RequestHash:    strings.Repeat("a", 64),
	}
}

func TestRetentionPolicyAuthority(t *testing.T) {
	f := catalogDB(t)
	ctx := context.Background()
	svc := retention.NewService(f.s)

	// Machines can never manage human lifecycle policy.
	machine := access.WithIdentity(ctx, access.Identity{PrincipalID: f.owner, CredentialID: "cred-1"})
	if _, _, e := svc.Get(machine, f.owner, f.project, "development"); e == nil || faultPublicCode(e) != "MACHINE_FORBIDDEN" {
		t.Fatalf("machine read must be forbidden, got %v", e)
	}
	if _, e := svc.Put(machine, retentionInput(f, "m1", "development", 90), retention.CreateRevision); e == nil || faultPublicCode(e) != "MACHINE_FORBIDDEN" {
		t.Fatalf("machine write must be forbidden, got %v", e)
	}

	// Non-owners and outsiders are rejected before any policy row exists.
	viewerInput := retentionInput(f, "viewer-denied", "development", 90)
	viewerInput.ActorID = f.viewer
	if _, e := svc.Put(ctx, viewerInput, retention.CreateRevision); e == nil || faultPublicCode(e) != "RETENTION_FORBIDDEN" {
		t.Fatalf("viewer write must be forbidden, got %v", e)
	}
	if _, _, e := svc.Get(ctx, f.outsider, f.project, "development"); e == nil || faultPublicCode(e) != "PROJECT_NOT_FOUND" {
		t.Fatalf("outsider read must be invisible, got %v", e)
	}
}

func TestRetentionPolicyRevisionIdempotencyAndAudit(t *testing.T) {
	f := catalogDB(t)
	ctx := context.Background()
	svc := retention.NewService(f.s)
	env := "production"

	// An unconfigured scope reports absence rather than a 404.
	if _, configured, e := svc.Get(ctx, f.owner, f.project, env); e != nil || configured {
		t.Fatalf("unconfigured scope: configured=%v err=%v", configured, e)
	}

	// First publish uses revision 0 (expect-absent) and starts at revision 1.
	first, e := svc.Put(ctx, retentionInput(f, "key-1", env, 90), retention.CreateRevision)
	if e != nil || first.Revision != 1 || first.RetentionDays != 90 || first.Environment != env || first.CreatedAt == "" || first.UpdatedAt == "" {
		t.Fatal(first, e)
	}
	if got, configured, e := svc.Get(ctx, f.owner, f.project, env); e != nil || !configured || got.RetentionDays != 90 || got.Revision != 1 {
		t.Fatalf("read after create wrong: %+v configured=%v err=%v", got, configured, e)
	}

	// A concurrent create against the now-existing scope must conflict.
	if _, e = svc.Put(ctx, retentionInput(f, "key-2", env, 120), retention.CreateRevision); e == nil || faultPublicCode(e) != "RETENTION_REVISION_CONFLICT" {
		t.Fatalf("duplicate create must conflict, got %v", e)
	}

	// Identical idempotent replay returns the original snapshot without a new revision.
	replay, e := svc.Put(ctx, retentionInput(f, "key-1", env, 90), retention.CreateRevision)
	if e != nil || replay != first {
		t.Fatalf("idempotent replay changed snapshot: %+v vs %+v err=%v", replay, first, e)
	}
	// The same key with a different request body is a hard idempotency conflict.
	divergent := retentionInput(f, "key-1", env, 120)
	divergent.RequestHash = strings.Repeat("b", 64)
	if _, e = svc.Put(ctx, divergent, 1); e == nil || faultPublicCode(e) != "IDEMPOTENCY_CONFLICT" {
		t.Fatalf("divergent idempotency key must conflict, got %v", e)
	}

	// A stale expected revision is rejected; the matching revision advances to 2.
	if _, e = svc.Put(ctx, retentionInput(f, "key-stale", env, 60), 99); e == nil || faultPublicCode(e) != "RETENTION_REVISION_CONFLICT" {
		t.Fatalf("stale revision must conflict, got %v", e)
	}
	updated, e := svc.Put(ctx, retentionInput(f, "key-3", env, 365), 1)
	if e != nil || updated.Revision != 2 || updated.RetentionDays != 365 {
		t.Fatal(updated, e)
	}

	// Bounds are enforced at the domain boundary.
	tooShort := retentionInput(f, "key-short", env, retention.MinRetentionDays-1)
	if _, e = svc.Put(ctx, tooShort, 2); e == nil || faultPublicCode(e) != "INVALID_RETENTION_INPUT" {
		t.Fatalf("sub-floor retention must be rejected, got %v", e)
	}

	// Environments are independent scopes with their own revision lifecycle.
	dev, e := svc.Put(ctx, retentionInput(f, "key-dev", "development", 30), retention.CreateRevision)
	if e != nil || dev.Revision != 1 || dev.RetentionDays != 30 {
		t.Fatal(dev, e)
	}

	// Only accepted, non-replayed writes produce audit and mutation records.
	var audits, mutations int
	if e = f.s.Pool.QueryRow(ctx, `SELECT count(*) FROM audit_entries
 WHERE project_id=$1 AND action='retention.policy_updated'`, f.project).Scan(&audits); e != nil || audits != 3 {
		t.Fatalf("audit count=%d err=%v (expect 3: prod create, prod update, dev create)", audits, e)
	}
	if e = f.s.Pool.QueryRow(ctx, `SELECT count(*) FROM retention_policy_mutations WHERE project_id=$1`, f.project).Scan(&mutations); e != nil || mutations != 3 {
		t.Fatalf("mutation count=%d err=%v", mutations, e)
	}
}

func faultPublicCode(e error) string {
	if e == nil {
		return ""
	}
	return fault.Public(e).Code
}
