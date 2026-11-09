//go:build integration

package postgres

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/howtomakeaname/Evia/backend/internal/access"
	"github.com/howtomakeaname/Evia/backend/internal/platform/fault"
	"github.com/howtomakeaname/Evia/backend/internal/platform/ids"
)

func settingsFixture(t *testing.T) (*Store, *access.Settings, string, string, string, string, string) {
	t.Helper()
	s := database(t)
	ctx := context.Background()
	owner, editor, viewer, outsider, project := ids.New(), ids.New(), ids.New(), ids.New(), ids.New()
	for i, actor := range []string{owner, editor, viewer, outsider} {
		if _, e := s.Pool.Exec(ctx, `INSERT INTO accounts(id,username,display_name,password_hash) VALUES($1,$2,'Synthetic settings account','unused')`, actor, fmt.Sprintf("settings%d", i)); e != nil {
			t.Fatal(e)
		}
	}
	if e := s.CreateProject(ctx, owner, access.Project{ID: project, Name: "Initial", Timezone: "UTC"}); e != nil {
		t.Fatal(e)
	}
	if _, e := s.Pool.Exec(ctx, `INSERT INTO project_members(project_id,user_id,role) VALUES($1,$2,'editor'),($1,$3,'viewer')`, project, editor, viewer); e != nil {
		t.Fatal(e)
	}
	return s, access.NewSettings(s), owner, editor, viewer, outsider, project
}

func settingsError(t *testing.T, e error, code string) {
	t.Helper()
	if e == nil || fault.Public(e).Code != code {
		t.Fatalf("wanted %s, got %v", code, e)
	}
}

func TestProjectSettingsPermissionsAndReplay(t *testing.T) {
	s, service, owner, editor, viewer, outsider, project := settingsFixture(t)
	ctx := context.Background()
	for _, actor := range []string{owner, editor, viewer} {
		out, e := service.Get(ctx, actor, project)
		if e != nil || out.ProjectID != project || out.Name != "Initial" || out.Timezone != "UTC" || out.Revision != 1 {
			t.Fatal(out, e)
		}
	}
	for _, actor := range []string{editor, viewer} {
		_, e := service.Update(ctx, actor, project, 1, "denied", access.SettingsUpdate{Name: "Denied", Timezone: "UTC"})
		settingsError(t, e, "PROJECT_SETTINGS_FORBIDDEN")
	}
	_, e := service.Get(ctx, outsider, project)
	settingsError(t, e, "PROJECT_NOT_FOUND")
	_, e = service.Update(ctx, outsider, project, 1, "denied", access.SettingsUpdate{Name: "Denied", Timezone: "UTC"})
	settingsError(t, e, "PROJECT_NOT_FOUND")
	other := ids.New()
	if e = s.CreateProject(ctx, outsider, access.Project{ID: other, Name: "Other", Timezone: "UTC"}); e != nil {
		t.Fatal(e)
	}
	_, e = service.Get(ctx, owner, other)
	settingsError(t, e, "PROJECT_NOT_FOUND")
	for _, identity := range []access.Identity{{}, {PrincipalID: owner, ProjectID: project, Permissions: []string{access.PermissionProjectRead}}} {
		machine := access.WithIdentity(ctx, identity)
		_, e = s.GetProjectSettings(machine, owner, project)
		settingsError(t, e, "MACHINE_FORBIDDEN")
		_, e = s.UpdateProjectSettings(machine, owner, project, 1, "machine", "hash", access.SettingsUpdate{Name: "Denied", Timezone: "UTC"})
		settingsError(t, e, "MACHINE_FORBIDDEN")
	}
	input := access.SettingsUpdate{Name: "Renamed", Timezone: "Asia/Shanghai"}
	first, e := service.Update(ctx, owner, project, 1, "response-loss", input)
	if e != nil || first.Revision != 2 || first.Name != input.Name || first.Timezone != input.Timezone {
		t.Fatal(first, e)
	}
	second, e := service.Update(ctx, owner, project, 2, "unchanged", input)
	if e != nil || second.Revision != 3 {
		t.Fatal("unchanged replacement must advance revision", second, e)
	}
	replay, e := service.Update(ctx, owner, project, 1, "response-loss", input)
	if e != nil || replay != first {
		t.Fatal("response-loss retry lost original snapshot", replay, first, e)
	}
	_, e = service.Update(ctx, owner, project, 2, "response-loss", input)
	settingsError(t, e, "IDEMPOTENCY_CONFLICT")
	_, e = service.Update(ctx, owner, project, 1, "response-loss", access.SettingsUpdate{Name: "Different", Timezone: input.Timezone})
	settingsError(t, e, "IDEMPOTENCY_CONFLICT")
	_, e = service.Update(ctx, owner, project, 2, "stale", input)
	settingsError(t, e, "PROJECT_SETTINGS_REVISION_CONFLICT")
	var count int
	if e = s.Pool.QueryRow(ctx, `SELECT count(*) FROM audit_entries WHERE project_id=$1 AND action='project.settings_updated'`, project).Scan(&count); e != nil || count != 2 {
		t.Fatal("replay/stale request duplicated audit", count, e)
	}
	if e = s.Pool.QueryRow(ctx, `SELECT count(*) FROM project_governance_mutations WHERE project_id=$1 AND operation='settings.update'`, project).Scan(&count); e != nil || count != 2 {
		t.Fatal("failed request persisted replay", count, e)
	}
	current, e := service.Get(ctx, owner, project)
	if e != nil || current != second {
		t.Fatal("retry rewound latest settings", current, e)
	}
	// The same actor/key on another authorized project is a separate operation.
	if _, e = s.Pool.Exec(ctx, `INSERT INTO project_members(project_id,user_id,role) VALUES($1,$2,'owner')`, other, owner); e != nil {
		t.Fatal(e)
	}
	if out, e := service.Update(ctx, owner, other, 1, "response-loss", input); e != nil || out.ProjectID != other || out.Revision != 2 {
		t.Fatal(out, e)
	}
	// Every retry must recheck current authority before revealing its saved response.
	if _, e = s.Pool.Exec(ctx, `UPDATE project_members SET role='viewer' WHERE project_id=$1 AND user_id=$2`, project, owner); e != nil {
		t.Fatal(e)
	}
	_, e = service.Update(ctx, owner, project, 1, "response-loss", input)
	settingsError(t, e, "PROJECT_SETTINGS_FORBIDDEN")
	if _, e = s.Pool.Exec(ctx, `DELETE FROM project_members WHERE project_id=$1 AND user_id=$2`, project, owner); e != nil {
		t.Fatal(e)
	}
	_, e = service.Update(ctx, owner, project, 1, "response-loss", input)
	settingsError(t, e, "PROJECT_NOT_FOUND")
	if _, e = s.Pool.Exec(ctx, `UPDATE accounts SET disabled=true WHERE id=$1`, editor); e != nil {
		t.Fatal(e)
	}
	_, e = service.Get(ctx, editor, project)
	settingsError(t, e, "PROJECT_NOT_FOUND")
	if _, e = s.Pool.Exec(ctx, `UPDATE accounts SET disabled=true WHERE id=$1`, owner); e != nil {
		t.Fatal(e)
	}
	_, e = service.Update(ctx, owner, other, 1, "response-loss", input)
	settingsError(t, e, "PROJECT_NOT_FOUND")
}

func TestProjectSettingsConcurrentRevisionAndAuditRollback(t *testing.T) {
	s, service, owner, _, _, _, project := settingsFixture(t)
	ctx := context.Background()
	var successes, conflicts atomic.Int32
	var wg sync.WaitGroup
	for i := range 8 {
		wg.Go(func() {
			_, e := service.Update(ctx, owner, project, 1, fmt.Sprintf("race-%d", i), access.SettingsUpdate{Name: fmt.Sprintf("Winner %d", i), Timezone: "Europe/Paris"})
			if e == nil {
				successes.Add(1)
			} else if fault.Public(e).Code == "PROJECT_SETTINGS_REVISION_CONFLICT" {
				conflicts.Add(1)
			} else {
				t.Error(e)
			}
		})
	}
	wg.Wait()
	if successes.Load() != 1 || conflicts.Load() != 7 {
		t.Fatal("revision race", successes.Load(), conflicts.Load())
	}
	prior, e := service.Get(ctx, owner, project)
	if e != nil || prior.Revision != 2 {
		t.Fatal(prior, e)
	}
	if _, e = s.Pool.Exec(ctx, `CREATE FUNCTION reject_settings_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action='project.settings_updated' THEN RAISE EXCEPTION 'synthetic audit failure'; END IF; RETURN NEW; END $$;
 CREATE TRIGGER reject_settings_audit BEFORE INSERT ON audit_entries FOR EACH ROW EXECUTE FUNCTION reject_settings_audit()`); e != nil {
		t.Fatal(e)
	}
	input := access.SettingsUpdate{Name: "Rolled back", Timezone: "America/New_York"}
	if _, e = service.Update(ctx, owner, project, 2, "audit-failure", input); e == nil {
		t.Fatal("audit failure accepted settings")
	}
	current, e := service.Get(ctx, owner, project)
	if e != nil || current != prior {
		t.Fatal("audit failure persisted settings", current, e)
	}
	var count int
	if e = s.Pool.QueryRow(ctx, `SELECT count(*) FROM project_governance_mutations WHERE project_id=$1 AND idempotency_key='audit-failure'`, project).Scan(&count); e != nil || count != 0 {
		t.Fatal("audit failure consumed retry key", count, e)
	}
	if _, e = s.Pool.Exec(ctx, `DROP TRIGGER reject_settings_audit ON audit_entries; DROP FUNCTION reject_settings_audit()`); e != nil {
		t.Fatal(e)
	}
	if out, e := service.Update(ctx, owner, project, 2, "audit-failure", input); e != nil || out.Revision != 3 {
		t.Fatal("retry after audit recovery", out, e)
	}
	// Replay contention yields one audit and the same durable snapshot to everyone.
	var replaySuccess atomic.Int32
	for range 8 {
		wg.Go(func() {
			out, e := service.Update(ctx, owner, project, 3, "same-request", input)
			if e != nil || out.Revision != 4 {
				t.Error(out, e)
			} else {
				replaySuccess.Add(1)
			}
		})
	}
	wg.Wait()
	if replaySuccess.Load() != 8 {
		t.Fatal(replaySuccess.Load())
	}
	if e = s.Pool.QueryRow(ctx, `SELECT count(*) FROM audit_entries WHERE project_id=$1 AND action='project.settings_updated'`, project).Scan(&count); e != nil || count != 3 {
		t.Fatal("concurrent replay duplicated audit", count, e)
	}
	if _, e = s.Pool.Exec(ctx, `UPDATE projects SET settings_revision=9007199254740991 WHERE id=$1`, project); e != nil {
		t.Fatal(e)
	}
	_, e = service.Update(ctx, owner, project, 9007199254740991, "exhausted", input)
	settingsError(t, e, "PROJECT_SETTINGS_REVISION_CONFLICT")
}

func TestProjectSettingsRechecksAuthorityAfterLockWait(t *testing.T) {
	s, service, owner, _, _, _, project := settingsFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	input := access.SettingsUpdate{Name: "Saved", Timezone: "UTC"}
	if _, e := service.Update(ctx, owner, project, 1, "replay", input); e != nil {
		t.Fatal(e)
	}
	block, e := s.Pool.Begin(ctx)
	if e != nil {
		t.Fatal(e)
	}
	defer block.Rollback(context.Background())
	if _, e = block.Exec(ctx, `DELETE FROM project_members WHERE project_id=$1 AND user_id=$2`, project, owner); e != nil {
		t.Fatal(e)
	}
	done := make(chan error, 1)
	go func() { _, e := service.Update(ctx, owner, project, 1, "replay", input); done <- e }()
	// Observe the actual PostgreSQL row-lock wait before committing revocation.
	for {
		var waiting bool
		e = s.Pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE datname=current_database() AND wait_event_type='Lock' AND query LIKE 'SELECT m.role FROM project_members m JOIN accounts a ON a.id=m.user_id%')`).Scan(&waiting)
		if e != nil {
			t.Fatal(e)
		}
		if waiting {
			break
		}
		select {
		case e := <-done:
			t.Fatal("retry did not wait for membership change", e)
		case <-ctx.Done():
			t.Fatal("membership lock wait was not observed", ctx.Err())
		case <-time.After(5 * time.Millisecond):
		}
	}
	if e = block.Commit(ctx); e != nil {
		t.Fatal(e)
	}
	settingsError(t, <-done, "PROJECT_NOT_FOUND")
}
