//go:build integration

package postgres

import (
	"context"
	"strconv"
	"testing"
	"time"

	"github.com/howtomakeaname/Evia/backend/internal/access"
	"github.com/howtomakeaname/Evia/backend/internal/catalog"
)

func auditLogInsert(t *testing.T, f catalogFixture, actor, project, action, resource string, occurred time.Time) string {
	t.Helper()
	var id string
	if e := f.s.Pool.QueryRow(context.Background(), `INSERT INTO audit_entries(project_id,actor_id,action,resource_key,occurred_at) VALUES($1,$2,$3,$4,$5) RETURNING id::text`, project, actor, action, resource, occurred).Scan(&id); e != nil {
		t.Fatal(e)
	}
	return id
}

func TestAuditLogAuthorizationAndCurrentMembership(t *testing.T) {
	f := catalogDB(t)
	ctx := context.Background()
	svc := access.NewAuditLogs(f.s)
	filter := access.AuditFilter{From: time.Now().Add(-time.Hour), To: time.Now().Add(time.Hour)}
	if _, e := f.s.Pool.Exec(ctx, `INSERT INTO project_members(project_id,user_id,role) VALUES($1,$2,'editor')`, f.project, f.outsider); e != nil {
		t.Fatal(e)
	}
	for _, actor := range []string{f.viewer, f.outsider} {
		_, e := svc.List(ctx, actor, f.project, filter, nil, 10)
		faultCode(t, e, "AUDIT_FORBIDDEN")
	}
	_, e := svc.List(ctx, f.viewer, f.otherProject, filter, nil, 10)
	faultCode(t, e, "PROJECT_NOT_FOUND")
	items, e := svc.List(ctx, f.owner, f.project, filter, nil, 10)
	if e != nil || len(items) != 1 || items[0].ProjectID != f.project || items[0].ActorKind != "human" || items[0].CredentialID != nil {
		t.Fatalf("owner read: %#v, %v", items, e)
	}
	for _, identity := range []access.Identity{{}, {PrincipalID: f.owner, ProjectID: f.project, Permissions: []string{access.PermissionProjectRead}}} {
		_, e = f.s.ListAuditLogs(access.WithIdentity(ctx, identity), f.owner, f.project, filter, nil, 10)
		faultCode(t, e, "MACHINE_FORBIDDEN")
	}
	if _, e = f.s.Pool.Exec(ctx, `UPDATE project_members SET role='editor' WHERE project_id=$1 AND user_id=$2`, f.project, f.owner); e != nil {
		t.Fatal(e)
	}
	_, e = svc.List(ctx, f.owner, f.project, filter, nil, 10)
	faultCode(t, e, "AUDIT_FORBIDDEN")
	if _, e = f.s.Pool.Exec(ctx, `DELETE FROM project_members WHERE project_id=$1 AND user_id=$2`, f.project, f.owner); e != nil {
		t.Fatal(e)
	}
	_, e = svc.List(ctx, f.owner, f.project, filter, nil, 10)
	faultCode(t, e, "PROJECT_NOT_FOUND")
	if _, e = f.s.Pool.Exec(ctx, `UPDATE accounts SET disabled=true WHERE id=$1`, f.owner); e != nil {
		t.Fatal(e)
	}
	_, e = svc.List(ctx, f.owner, f.otherProject, filter, nil, 10)
	faultCode(t, e, "PROJECT_NOT_FOUND")
}

func TestAuditLogDescendingCursorFiltersAndNanosecondBoundaries(t *testing.T) {
	f := catalogDB(t)
	ctx := context.Background()
	svc := access.NewAuditLogs(f.s)
	at := time.Date(2026, 11, 1, 12, 0, 0, 123456000, time.UTC)
	first := auditLogInsert(t, f, f.owner, f.project, "synthetic.audit", "first", at)
	second := auditLogInsert(t, f, f.owner, f.project, "synthetic.audit", "second", at)
	third := auditLogInsert(t, f, f.viewer, f.project, "synthetic.audit", "third", at)
	_ = auditLogInsert(t, f, f.owner, f.otherProject, "synthetic.audit", "other_project", at)
	_ = auditLogInsert(t, f, f.owner, f.project, "synthetic.other", "other_action", at)
	_ = auditLogInsert(t, f, f.owner, f.project, "synthetic.audit", "excluded_upper", at.Add(time.Microsecond))
	filter := access.AuditFilter{From: at.Add(-time.Nanosecond), To: at.Add(time.Microsecond), Action: "synthetic.audit"}
	items, e := svc.List(ctx, f.owner, f.project, filter, nil, 1)
	if e != nil || len(items) != 2 || items[0].ID != third || items[1].ID != second {
		t.Fatalf("lookahead/tie order: %#v %v", items, e)
	}
	after := &access.AuditPosition{OccurredAt: items[0].OccurredAt, ID: items[0].ID}
	items, e = svc.List(ctx, f.owner, f.project, filter, after, 1)
	if e != nil || len(items) != 2 || items[0].ID != second || items[1].ID != first {
		t.Fatalf("second page: %#v %v", items, e)
	}
	after = &access.AuditPosition{OccurredAt: items[0].OccurredAt, ID: items[0].ID}
	items, e = svc.List(ctx, f.owner, f.project, filter, after, 1)
	if e != nil || len(items) != 1 || items[0].ID != first {
		t.Fatalf("last page: %#v %v", items, e)
	}
	filter.ActorID = f.owner
	items, e = svc.List(ctx, f.owner, f.project, filter, nil, 100)
	if e != nil || len(items) != 2 || items[0].ID != second || items[1].ID != first {
		t.Fatalf("actor filter: %#v %v", items, e)
	}
	filter.From, filter.To = at.Add(time.Nanosecond), at.Add(time.Microsecond+time.Nanosecond)
	items, e = svc.List(ctx, f.owner, f.project, filter, nil, 100)
	if e != nil || len(items) != 1 || items[0].ResourceKey != "excluded_upper" {
		t.Fatalf("nanosecond boundary: %#v %v", items, e)
	}
	filter.From, filter.To = at.Add(-time.Nanosecond), at
	items, e = svc.List(ctx, f.owner, f.project, filter, nil, 100)
	if e != nil || len(items) != 0 {
		t.Fatalf("exclusive upper bound: %#v %v", items, e)
	}
	filter.From, filter.To = at, at.Add(time.Nanosecond)
	items, e = svc.List(ctx, f.owner, f.project, filter, nil, 100)
	if e != nil || len(items) != 2 {
		t.Fatalf("inclusive lower bound: %#v %v", items, e)
	}
	for _, entry := range items {
		if _, e := strconv.ParseInt(entry.ID, 10, 64); e != nil || entry.OccurredAt.Location() != time.UTC {
			t.Fatal("invalid safe ID or UTC", e)
		}
	}
}

func TestAuditLogMachineOperationSafeAttributionAndNoReadAudit(t *testing.T) {
	f := catalogDB(t)
	ctx := context.Background()
	machines := access.NewMachines(f.s)
	in := machineInput()
	in.Permissions = append(in.Permissions, access.PermissionCatalogDraftsWrite)
	issued, e := machines.CreatePrincipal(ctx, f.owner, f.project, "audit-machine", in)
	if e != nil {
		t.Fatal(e)
	}
	identity, e := machines.AuthenticateMachine(ctx, issued.SecretOnce)
	if e != nil {
		t.Fatal(e)
	}
	mctx := access.WithIdentity(ctx, identity)
	if _, e = f.svc.CreateEvent(mctx, identity.PrincipalID, f.project, catalog.EventInput{Key: "audit_operation", DisplayName: "Synthetic operation"}); e != nil {
		t.Fatal(e)
	}
	filter := access.AuditFilter{From: time.Now().Add(-time.Hour), To: time.Now().Add(time.Hour), ActorID: identity.PrincipalID, Action: "event.created"}
	items, e := access.NewAuditLogs(f.s).List(ctx, f.owner, f.project, filter, nil, 10)
	if e != nil || len(items) != 1 {
		t.Fatalf("machine operation not returned: %#v %v", items, e)
	}
	entry := items[0]
	if entry.ActorKind != "machine" || entry.ActorID != identity.PrincipalID || entry.CredentialID == nil || *entry.CredentialID != issued.Credential.ID || entry.ResourceKey != "audit_operation" {
		t.Fatalf("incorrect safe attribution: %#v", entry)
	}
	_, e = f.s.ListAuditLogs(mctx, identity.PrincipalID, f.project, filter, nil, 10)
	faultCode(t, e, "MACHINE_FORBIDDEN")
	var before, after int
	if e = f.s.Pool.QueryRow(ctx, `SELECT count(*) FROM audit_entries`).Scan(&before); e != nil {
		t.Fatal(e)
	}
	filter.Action, filter.ActorID = "", ""
	for range 2 {
		if _, e = access.NewAuditLogs(f.s).List(ctx, f.owner, f.project, filter, nil, 100); e != nil {
			t.Fatal(e)
		}
	}
	if e = f.s.Pool.QueryRow(ctx, `SELECT count(*) FROM audit_entries`).Scan(&after); e != nil || after != before {
		t.Fatal("reads inflated audit log", before, after, e)
	}
}

func TestAuditLogAuthorityLocksUntilReadCompletes(t *testing.T) {
	f := catalogDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	blocker, e := f.s.Pool.Begin(ctx)
	if e != nil {
		t.Fatal(e)
	}
	defer blocker.Rollback(context.Background())
	if _, e = blocker.Exec(ctx, `LOCK TABLE audit_entries IN ACCESS EXCLUSIVE MODE`); e != nil {
		t.Fatal(e)
	}
	done := make(chan error, 1)
	go func() {
		_, err := access.NewAuditLogs(f.s).List(ctx, f.owner, f.project, access.AuditFilter{From: time.Now().Add(-time.Hour), To: time.Now().Add(time.Hour)}, nil, 10)
		done <- err
	}()
	// A blocked audit SELECT proves authorization has finished and its row locks
	// must remain held while the protected data is being read.
	for {
		var blocked bool
		e = f.s.Pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_locks WHERE relation='audit_entries'::regclass AND mode='AccessShareLock' AND NOT granted)`).Scan(&blocked)
		if e != nil {
			t.Fatal(e)
		}
		if blocked {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal("audit read did not reach data query")
		case err := <-done:
			t.Fatal("audit read ended before lock", err)
		case <-time.After(time.Millisecond * 5):
		}
	}
	for _, statement := range []string{`UPDATE project_members SET role='editor' WHERE project_id=$1 AND user_id=$2`, `DELETE FROM project_members WHERE project_id=$1 AND user_id=$2`, `UPDATE accounts SET disabled=true WHERE id=$2 AND $1::uuid IS NOT NULL`} {
		short, stop := context.WithTimeout(ctx, 75*time.Millisecond)
		_, err := f.s.Pool.Exec(short, statement, f.project, f.owner)
		stop()
		if err == nil {
			t.Fatal("authority changed while audit read was in progress")
		}
	}
	if e = blocker.Commit(ctx); e != nil {
		t.Fatal(e)
	}
	if e = <-done; e != nil {
		t.Fatal(e)
	}
	if _, e = f.s.Pool.Exec(ctx, `DELETE FROM project_members WHERE project_id=$1 AND user_id=$2`, f.project, f.owner); e != nil {
		t.Fatal(e)
	}
	_, e = access.NewAuditLogs(f.s).List(ctx, f.owner, f.project, access.AuditFilter{From: time.Now().Add(-time.Hour), To: time.Now().Add(time.Hour)}, nil, 10)
	faultCode(t, e, "PROJECT_NOT_FOUND")
}
