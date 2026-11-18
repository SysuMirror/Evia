//go:build integration

package postgres

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/howtomakeaname/Evia/backend/internal/access"
	"github.com/howtomakeaname/Evia/backend/internal/platform/fault"
)

func machineInput() access.MachineCreate {
	return access.MachineCreate{Name: "Synthetic agent", Environment: "development", Permissions: []string{access.PermissionProjectRead, access.PermissionQueriesRead, access.PermissionQueriesRun}, ExpiresAt: time.Now().UTC().Add(time.Hour)}
}
func machineFault(t *testing.T, e error, code string) {
	t.Helper()
	var problem *fault.Error
	if !errors.As(e, &problem) || problem.Code != code {
		t.Fatalf("want %s, got %v", code, e)
	}
}

func TestMachineLifecycleIsolationAndHashOnlyRecovery(t *testing.T) {
	f := catalogDB(t)
	ctx := context.Background()
	svc := access.NewMachines(f.s)
	in := machineInput()
	issued, e := svc.CreatePrincipal(ctx, f.owner, f.project, "create", in)
	if e != nil {
		t.Fatal(e)
	}
	if issued.SecretOnce == "" || issued.Principal.ID == f.owner || issued.Principal.Revision != 1 {
		t.Fatal("machine did not get independent identity")
	}
	identity, e := svc.AuthenticateMachine(ctx, issued.SecretOnce)
	if e != nil {
		t.Fatal(e)
	}
	if identity.PrincipalID != issued.Principal.ID || identity.CredentialID != issued.Credential.ID || identity.ProjectID != f.project {
		t.Fatal("credential identity mismatch")
	}
	var kind, digest string
	var accounts int
	if e = f.s.Pool.QueryRow(ctx, `SELECT kind FROM principals WHERE id=$1`, identity.PrincipalID).Scan(&kind); e != nil || kind != "machine" {
		t.Fatal("missing registry machine", e)
	}
	if e = f.s.Pool.QueryRow(ctx, `SELECT count(*) FROM accounts WHERE id=$1`, identity.PrincipalID).Scan(&accounts); e != nil || accounts != 0 {
		t.Fatal("machine impersonates human", e)
	}
	if e = f.s.Pool.QueryRow(ctx, `SELECT secret_digest FROM machine_credentials WHERE id=$1`, identity.CredentialID).Scan(&digest); e != nil || digest != access.Digest(issued.SecretOnce) {
		t.Fatal("credential digest mismatch", e)
	}
	replay, e := svc.CreatePrincipal(ctx, f.owner, f.project, "create", in)
	if e != nil || replay.SecretOnce != "" || replay.Credential.ID != issued.Credential.ID {
		t.Fatal("unsafe issuance replay", e)
	}
	canonicalRetry := in
	canonicalRetry.ExpiresAt = in.ExpiresAt.In(time.FixedZone("Synthetic offset", 3600))
	canonicalRetry.Permissions = []string{access.PermissionQueriesRun, access.PermissionQueriesRead, access.PermissionProjectRead}
	canonicalReplay, e := svc.CreatePrincipal(ctx, f.owner, f.project, "create", canonicalRetry)
	if e != nil || canonicalReplay.Principal.ID != issued.Principal.ID || canonicalReplay.SecretOnce != "" {
		t.Fatal("equivalent issuance did not replay", e)
	}
	changed := in
	changed.Name = "Changed"
	_, e = svc.CreatePrincipal(ctx, f.owner, f.project, "create", changed)
	machineFault(t, e, "IDEMPOTENCY_CONFLICT")
	_, e = svc.CreatePrincipal(ctx, f.viewer, f.project, "viewer", in)
	machineFault(t, e, "MACHINE_MANAGEMENT_FORBIDDEN")
	_, e = svc.GetPrincipal(ctx, f.owner, f.otherProject, issued.Principal.ID)
	machineFault(t, e, "MACHINE_PRINCIPAL_NOT_FOUND")
	mctx := access.WithIdentity(ctx, identity)
	_, e = svc.CreatePrincipal(mctx, identity.PrincipalID, f.project, "machine-escalation", in)
	machineFault(t, e, "MACHINE_FORBIDDEN")
	project, e := f.s.GetProject(mctx, identity.PrincipalID, f.project)
	if e != nil || project.Role != "machine" {
		t.Fatal("machine project discovery failed", e)
	}
	for _, change := range []string{`UPDATE machine_principals SET environment='production',revision=revision+1 WHERE id=$1`, `UPDATE machine_principals SET permissions=ARRAY['queries.raw.read'],revision=revision+1 WHERE id=$1`} {
		if _, e = f.s.Pool.Exec(ctx, change, identity.PrincipalID); e == nil {
			t.Fatal("immutable grant changed")
		}
	}
	rotation := access.MachineRotate{ExpiresAt: time.Now().Add(2 * time.Hour), OverlapSeconds: 0}
	rotated, e := svc.RotateCredential(ctx, f.owner, f.project, identity.PrincipalID, 1, "rotate", rotation)
	if e != nil || rotated.SecretOnce == "" || rotated.Principal.Revision != 2 {
		t.Fatal("rotation failed", e)
	}
	_, e = svc.AuthenticateMachine(ctx, issued.SecretOnce)
	machineFault(t, e, "MACHINE_CREDENTIAL_REQUIRED")
	retry, e := svc.RotateCredential(ctx, f.owner, f.project, identity.PrincipalID, 1, "rotate", rotation)
	if e != nil || retry.SecretOnce != "" || retry.Credential.ID != rotated.Credential.ID {
		t.Fatal("rotation retry minted secret", e)
	}
	_, e = svc.RotateCredential(ctx, f.owner, f.project, identity.PrincipalID, 1, "stale", rotation)
	machineFault(t, e, "MACHINE_REVISION_CONFLICT")
	p, e := svc.RevokeCredential(ctx, f.owner, f.project, identity.PrincipalID, rotated.Credential.ID, 2)
	if e != nil || p.Revision != 3 {
		t.Fatal("credential revoke failed", e)
	}
	_, e = svc.AuthenticateMachine(ctx, rotated.SecretOnce)
	machineFault(t, e, "MACHINE_CREDENTIAL_REQUIRED")
	p, e = svc.RevokeCredential(ctx, f.owner, f.project, identity.PrincipalID, rotated.Credential.ID, 2)
	if e != nil || p.Revision != 3 {
		t.Fatal("revoke replay changed revision", e)
	}
	p, e = svc.RevokePrincipal(ctx, f.owner, f.project, identity.PrincipalID, 3)
	if e != nil || p.Revision != 4 || p.RevokedAt == nil {
		t.Fatal("principal revoke failed", e)
	}
	_, e = svc.RotateCredential(ctx, f.owner, f.project, identity.PrincipalID, 4, "revived", rotation)
	machineFault(t, e, "MACHINE_PRINCIPAL_REVOKED")
	if _, e = f.s.Pool.Exec(ctx, `UPDATE machine_principals SET revoked_at=NULL,revision=revision+1 WHERE id=$1`, identity.PrincipalID); e == nil {
		t.Fatal("principal revived")
	}
	detail, e := svc.GetPrincipal(ctx, f.owner, f.project, identity.PrincipalID)
	if e != nil || len(detail.Credentials) != 2 {
		t.Fatal("bounded credential metadata missing", e)
	}
}

func TestMachineOperationAuthorizationAndRevocationSerialize(t *testing.T) {
	f := catalogDB(t)
	ctx := context.Background()
	svc := access.NewMachines(f.s)
	issued, e := svc.CreatePrincipal(ctx, f.owner, f.project, "create", machineInput())
	if e != nil {
		t.Fatal(e)
	}
	identity, e := svc.AuthenticateMachine(ctx, issued.SecretOnce)
	if e != nil {
		t.Fatal(e)
	}
	mctx := access.WithIdentity(ctx, identity)
	tx, e := f.s.catalogTx(mctx, identity.PrincipalID, f.project, false)
	if e != nil {
		t.Fatal(e)
	}
	if e = f.s.authorizeOperation(mctx, tx, identity.PrincipalID, f.project, "production", access.PermissionQueriesRead); e == nil {
		t.Fatal("cross-environment operation accepted")
	}
	if e = f.s.authorizeOperation(mctx, tx, identity.PrincipalID, f.project, "", access.PermissionQueriesRead); e == nil {
		t.Fatal("unscoped operation accepted")
	}
	if e = f.s.authorizeOperation(mctx, tx, identity.PrincipalID, f.project, "development", access.PermissionQueriesRawRead); e == nil {
		t.Fatal("raw scope widened")
	}
	if e = f.s.authorizeOperation(mctx, tx, identity.PrincipalID, f.project, "development", access.PermissionQueriesRead); e != nil {
		t.Fatal(e)
	}
	if e = machineAudit(mctx, tx, f.project, identity.PrincipalID, "synthetic.read", "synthetic"); e != nil {
		t.Fatal(e)
	}
	if e = tx.Commit(ctx); e != nil {
		t.Fatal(e)
	}
	var credential string
	if e = f.s.Pool.QueryRow(ctx, `SELECT credential_id::text FROM audit_entries WHERE actor_id=$1 AND action='synthetic.read'`, identity.PrincipalID).Scan(&credential); e != nil || credential != identity.CredentialID {
		t.Fatal("audit attribution lost", e)
	}
	tx, e = f.s.catalogTx(mctx, identity.PrincipalID, f.project, false)
	if e != nil {
		t.Fatal(e)
	}
	if e = f.s.authorizeOperation(mctx, tx, identity.PrincipalID, f.project, "development", access.PermissionQueriesRead); e != nil {
		t.Fatal(e)
	}
	deadline, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
	defer cancel()
	_, e = svc.RevokePrincipal(deadline, f.owner, f.project, identity.PrincipalID, 1)
	if e == nil {
		t.Fatal("revocation crossed authorized transaction lock")
	}
	if e = tx.Rollback(ctx); e != nil {
		t.Fatal(e)
	}
	if _, e = svc.RevokePrincipal(ctx, f.owner, f.project, identity.PrincipalID, 1); e != nil {
		t.Fatal("revocation did not recover", e)
	}
	if _, e = f.s.catalogTx(mctx, identity.PrincipalID, f.project, false); e == nil {
		t.Fatal("old trusted context revived revoked credential")
	}
	if _, e = f.s.catalogTx(ctx, identity.PrincipalID, f.project, false); e == nil {
		t.Fatal("machine actor string established authority")
	}
}

func TestMachineConcurrentIssuanceHasOneSecretAndExpiryBounds(t *testing.T) {
	f := catalogDB(t)
	ctx := context.Background()
	svc := access.NewMachines(f.s)
	in := machineInput()
	var wg sync.WaitGroup
	out := make(chan access.MachineIssued, 2)
	errs := make(chan error, 2)
	for range 2 {
		wg.Go(func() {
			issued, e := svc.CreatePrincipal(ctx, f.owner, f.project, "same-key", in)
			out <- issued
			errs <- e
		})
	}
	wg.Wait()
	close(out)
	close(errs)
	for e := range errs {
		if e != nil {
			t.Fatal(e)
		}
	}
	var id string
	secrets := 0
	for issued := range out {
		if id != "" && id != issued.Principal.ID {
			t.Fatal("duplicate principal")
		}
		id = issued.Principal.ID
		if issued.SecretOnce != "" {
			secrets++
		}
	}
	if secrets != 1 {
		t.Fatalf("one-time secrets=%d", secrets)
	}
	for _, expiry := range []time.Time{time.Now().Add(30 * time.Second), time.Now().Add(91 * 24 * time.Hour)} {
		bad := in
		bad.ExpiresAt = expiry
		_, e := svc.CreatePrincipal(ctx, f.owner, f.project, expiry.Format("20060102T150405"), bad)
		machineFault(t, e, "INVALID_CREDENTIAL_EXPIRY")
	}
	rotation := access.MachineRotate{ExpiresAt: time.Now().Add(time.Hour), OverlapSeconds: 300}
	out2 := make(chan access.MachineIssued, 2)
	errs2 := make(chan error, 2)
	for range 2 {
		wg.Go(func() {
			issued, e := svc.RotateCredential(ctx, f.owner, f.project, id, 1, "same-rotation", rotation)
			out2 <- issued
			errs2 <- e
		})
	}
	wg.Wait()
	close(out2)
	close(errs2)
	for e := range errs2 {
		if e != nil {
			t.Fatal(e)
		}
	}
	secrets = 0
	var credential string
	for issued := range out2 {
		if credential != "" && credential != issued.Credential.ID {
			t.Fatal("duplicate rotated credential")
		}
		credential = issued.Credential.ID
		if issued.SecretOnce != "" {
			secrets++
		}
		if issued.Principal.Revision != 2 {
			t.Fatal("rotation revision mismatch")
		}
	}
	if secrets != 1 {
		t.Fatalf("rotation one-time secrets=%d", secrets)
	}
	var overlap bool
	if e := f.s.Pool.QueryRow(ctx, `SELECT expires_at>clock_timestamp() AND expires_at<=clock_timestamp()+interval '300 seconds' FROM machine_credentials WHERE principal_id=$1 AND id<>$2`, id, credential).Scan(&overlap); e != nil || !overlap {
		t.Fatal("bounded overlap missing", e)
	}
}

// TestMachineGovernanceGrantsPersistAndAuthenticate closes the contract loop
// for the two data governance permissions: a principal carrying data.erasure
// and data.export must survive creation and authentication with the exact
// permission set, matching the 26-entry access.machinePermissions closed set.
func TestMachineGovernanceGrantsPersistAndAuthenticate(t *testing.T) {
	f := catalogDB(t)
	ctx := context.Background()
	svc := access.NewMachines(f.s)
	in := machineInput()
	in.Name = "Governance agent"
	in.Permissions = []string{access.PermissionDataErasure, access.PermissionDataExport}
	issued, e := svc.CreatePrincipal(ctx, f.owner, f.project, "governance-create", in)
	if e != nil {
		t.Fatal(e)
	}
	if len(issued.Principal.Permissions) != 2 {
		t.Fatalf("governance grants not persisted: %+v", issued.Principal.Permissions)
	}
	identity, e := svc.AuthenticateMachine(ctx, issued.SecretOnce)
	if e != nil {
		t.Fatal(e)
	}
	grants := map[string]bool{}
	for _, permission := range identity.Permissions {
		grants[permission] = true
	}
	if !grants[access.PermissionDataErasure] || !grants[access.PermissionDataExport] {
		t.Fatalf("authenticated identity lost governance grants: %+v", identity.Permissions)
	}
}
