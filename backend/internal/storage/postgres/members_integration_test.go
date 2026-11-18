//go:build integration

package postgres

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/howtomakeaname/Evia/backend/internal/access"
	"github.com/howtomakeaname/Evia/backend/internal/analysis"
	"github.com/howtomakeaname/Evia/backend/internal/catalog"
	"github.com/howtomakeaname/Evia/backend/internal/platform/fault"
	"github.com/howtomakeaname/Evia/backend/internal/platform/ids"
)

func memberInput(revision int64, key, role string) access.MemberInput {
	return access.MemberInput{Revision: revision, IdempotencyKey: key, Role: role}
}
func TestMembershipLifecycleAuthoritySnapshotAndReplay(t *testing.T) {
	f := catalogDB(t)
	svc := access.NewMemberships(f.s)
	ctx := context.Background()
	page, e := svc.List(ctx, f.owner, f.project, access.MemberListFilter{Limit: 1})
	if e != nil || page.Revision != 1 || len(page.Items) != 2 || page.Items[0].UserID >= page.Items[1].UserID {
		t.Fatal(page, e)
	}
	for _, actor := range []string{f.viewer, f.outsider} {
		_, e = svc.List(ctx, actor, f.project, access.MemberListFilter{Limit: 10})
		if actor == f.viewer {
			faultCode(t, e, "MEMBER_MANAGEMENT_FORBIDDEN")
		} else {
			faultCode(t, e, "PROJECT_NOT_FOUND")
		}
		_, e = svc.Add(ctx, actor, f.project, access.MemberInput{Username: "does-not-exist", Role: "owner", Revision: 1, IdempotencyKey: "no-enumeration"})
		if actor == f.viewer {
			faultCode(t, e, "MEMBER_MANAGEMENT_FORBIDDEN")
		} else {
			faultCode(t, e, "PROJECT_NOT_FOUND")
		}
	}
	in := access.MemberInput{Username: "  ACCOUNT2 ", Role: "editor", Revision: 1, IdempotencyKey: "add"}
	added, e := svc.Add(ctx, f.owner, f.project, in)
	if e != nil || added.Revision != 2 || added.Member.UserID != f.outsider || added.Member.Username != "account2" || added.Member.Role != "editor" {
		t.Fatal(added, e)
	}
	replay, e := svc.Add(ctx, f.owner, f.project, in)
	if e != nil || !reflect.DeepEqual(added, replay) {
		t.Fatal("response-loss replay changed", replay, e)
	}
	changed := in
	changed.Role = "owner"
	_, e = svc.Add(ctx, f.owner, f.project, changed)
	faultCode(t, e, "IDEMPOTENCY_CONFLICT")
	_, e = svc.List(ctx, f.owner, f.project, access.MemberListFilter{After: page.Items[0].UserID, Revision: page.Revision, Limit: 1})
	faultCode(t, e, "MEMBERSHIP_REVISION_CONFLICT")
	_, e = svc.Update(ctx, f.owner, f.project, f.outsider, memberInput(1, "stale", "viewer"))
	faultCode(t, e, "MEMBERSHIP_REVISION_CONFLICT")
	updated, e := svc.Update(ctx, f.owner, f.project, f.outsider, memberInput(2, "update", "viewer"))
	if e != nil || updated.Revision != 3 || updated.Member.Role != "viewer" {
		t.Fatal(updated, e)
	}
	removed, e := svc.Remove(ctx, f.owner, f.project, f.outsider, memberInput(3, "remove", ""))
	if e != nil || removed.Revision != 4 || removed.Member.UserID != f.outsider {
		t.Fatal(removed, e)
	}
	replay, e = svc.Remove(ctx, f.owner, f.project, f.outsider, memberInput(3, "remove", ""))
	if e != nil || !reflect.DeepEqual(removed, replay) {
		t.Fatal("deleted membership lost replay", replay, e)
	}
	// Replay returns the accepted snapshot even after another accepted mutation.
	replay, e = svc.Add(ctx, f.owner, f.project, in)
	if e != nil || !reflect.DeepEqual(added, replay) {
		t.Fatal("add replay rewrote membership", replay, e)
	}
	if _, e = svc.Add(ctx, f.owner, f.project, access.MemberInput{Username: "account2", Role: "viewer", Revision: 4, IdempotencyKey: "readd"}); e != nil {
		t.Fatal(e)
	}
	_, e = svc.Update(ctx, f.owner, f.project, f.outsider, memberInput(2, "aba", "owner"))
	faultCode(t, e, "MEMBERSHIP_REVISION_CONFLICT")
	_, e = svc.Remove(ctx, f.owner, f.otherProject, f.outsider, memberInput(1, "other-scope", ""))
	faultCode(t, e, "MEMBER_NOT_FOUND")
	var audits, records int
	if e = f.s.Pool.QueryRow(ctx, `SELECT count(*) FROM audit_entries WHERE project_id=$1 AND action LIKE 'project.member_%'`, f.project).Scan(&audits); e != nil || audits != 4 {
		t.Fatal(audits, e)
	}
	if e = f.s.Pool.QueryRow(ctx, `SELECT count(*) FROM project_governance_mutations WHERE project_id=$1`, f.project).Scan(&records); e != nil || records != 4 {
		t.Fatal(records, e)
	}
	// Demoting the initiating owner makes even its original replay inaccessible.
	if _, e = svc.Update(ctx, f.owner, f.project, f.outsider, memberInput(5, "promote", "owner")); e != nil {
		t.Fatal(e)
	}
	if _, e = svc.Update(ctx, f.outsider, f.project, f.owner, memberInput(6, "demote-actor", "viewer")); e != nil {
		t.Fatal(e)
	}
	_, e = svc.Add(ctx, f.owner, f.project, in)
	faultCode(t, e, "MEMBER_MANAGEMENT_FORBIDDEN")
}

func TestMembershipEnabledOwnerAndAccountBoundaries(t *testing.T) {
	f := catalogDB(t)
	svc := access.NewMemberships(f.s)
	ctx := context.Background()
	_, e := svc.Remove(ctx, f.owner, f.project, f.owner, memberInput(1, "remove-last", ""))
	faultCode(t, e, "LAST_OWNER_REQUIRED")
	_, e = svc.Update(ctx, f.owner, f.project, f.owner, memberInput(1, "demote-last", "editor"))
	faultCode(t, e, "LAST_OWNER_REQUIRED")
	_, e = svc.Add(ctx, f.owner, f.project, access.MemberInput{Username: "missing", Role: "viewer", Revision: 1, IdempotencyKey: "missing"})
	faultCode(t, e, "ACCOUNT_NOT_FOUND")
	if _, e = f.s.Pool.Exec(ctx, `UPDATE accounts SET disabled=true WHERE id=$1`, f.outsider); e != nil {
		t.Fatal(e)
	}
	_, e = svc.Add(ctx, f.owner, f.project, access.MemberInput{Username: "account2", Role: "owner", Revision: 1, IdempotencyKey: "disabled"})
	faultCode(t, e, "ACCOUNT_DISABLED")
	if _, e = f.s.Pool.Exec(ctx, `INSERT INTO project_members(project_id,user_id,role) VALUES($1,$2,'owner')`, f.project, f.outsider); e != nil {
		t.Fatal(e)
	}
	_, e = svc.Remove(ctx, f.owner, f.project, f.owner, memberInput(1, "disabled-not-owner", ""))
	faultCode(t, e, "LAST_OWNER_REQUIRED")
	_, e = svc.Update(ctx, f.owner, f.project, f.outsider, memberInput(1, "disabled-promotion", "owner"))
	faultCode(t, e, "ACCOUNT_DISABLED")
	// An enabled owner may remove a disabled owner, restoring a healthy collection.
	if _, e = svc.Remove(ctx, f.owner, f.project, f.outsider, memberInput(1, "remove-disabled", "")); e != nil {
		t.Fatal(e)
	}
	if _, e = f.s.Pool.Exec(ctx, `UPDATE accounts SET disabled=true WHERE id=$1`, f.owner); e != nil {
		t.Fatal(e)
	}
	_, e = svc.List(ctx, f.owner, f.project, access.MemberListFilter{Limit: 10})
	faultCode(t, e, "PROJECT_NOT_FOUND")
	mctx := access.WithIdentity(ctx, access.Identity{PrincipalID: f.owner, CredentialID: ids.New()})
	_, e = f.s.ListProjectMembers(mctx, f.owner, f.project, access.MemberListFilter{Limit: 10})
	faultCode(t, e, "MACHINE_FORBIDDEN")
}

func TestMembershipConcurrentOwnersCannotRevokeEachOther(t *testing.T) {
	for _, operation := range []string{"demote", "remove"} {
		t.Run(operation, func(t *testing.T) {
			f := catalogDB(t)
			svc := access.NewMemberships(f.s)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if _, e := svc.Update(ctx, f.owner, f.project, f.viewer, memberInput(1, "second-owner", "owner")); e != nil {
				t.Fatal(e)
			}
			start := make(chan struct{})
			results := make(chan error, 2)
			var wg sync.WaitGroup
			for _, pair := range [][2]string{{f.owner, f.viewer}, {f.viewer, f.owner}} {
				wg.Add(1)
				go func(actor, target string) {
					defer wg.Done()
					<-start
					var e error
					if operation == "remove" {
						_, e = svc.Remove(ctx, actor, f.project, target, memberInput(2, "race", ""))
					} else {
						_, e = svc.Update(ctx, actor, f.project, target, memberInput(2, "race", "viewer"))
					}
					results <- e
				}(pair[0], pair[1])
			}
			close(start)
			wg.Wait()
			close(results)
			wins := 0
			for e := range results {
				if e == nil {
					wins++
					continue
				}
				var p *fault.Error
				if !errors.As(e, &p) || (p.Code != "MEMBER_MANAGEMENT_FORBIDDEN" && p.Code != "PROJECT_NOT_FOUND" && p.Code != "MEMBERSHIP_REVISION_CONFLICT") {
					t.Fatal("unexpected concurrent result", e)
				}
			}
			if wins != 1 {
				t.Fatal("expected exactly one accepted owner mutation", wins)
			}
			var owners int
			if e := f.s.Pool.QueryRow(ctx, `SELECT count(*) FROM project_members m JOIN accounts a ON a.id=m.user_id WHERE m.project_id=$1 AND m.role='owner' AND NOT a.disabled`, f.project).Scan(&owners); e != nil || owners != 1 {
				t.Fatal("enabled owner invariant lost", owners, e)
			}
		})
	}
}

func TestMembershipMutationAndBusinessWriteLockOrder(t *testing.T) {
	f := catalogDB(t)
	svc := access.NewMemberships(f.s)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, e := svc.Update(ctx, f.owner, f.project, f.viewer, memberInput(1, "make-editor", "editor")); e != nil {
		t.Fatal(e)
	}
	// Pause a real business transaction after it pins the target's authority.
	// Membership revocation must wait without owning the project lock.
	business, e := f.s.catalogTx(ctx, f.viewer, f.project, false)
	if e != nil {
		t.Fatal(e)
	}
	defer business.Rollback(context.Background())
	var schema string
	if e = business.QueryRow(ctx, `SELECT current_schema()`).Scan(&schema); e != nil {
		t.Fatal(e)
	}
	done := make(chan error, 1)
	go func() {
		_, e := svc.Remove(ctx, f.owner, f.project, f.viewer, memberInput(2, "revoke-editor", ""))
		done <- e
	}()
	ticker := time.NewTicker(5 * time.Millisecond)
	defer ticker.Stop()
	for {
		var waiting bool
		e = f.s.Pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity a JOIN pg_locks l ON l.pid=a.pid JOIN pg_locks held ON held.locktype='relation' AND held.pid=a.pid WHERE a.wait_event_type='Lock' AND a.query LIKE '%ORDER BY m.user_id FOR UPDATE OF m%' AND held.relation=to_regclass($1))`, schema+".project_members").Scan(&waiting)
		if e != nil {
			t.Fatal(e)
		}
		if waiting {
			break
		}
		select {
		case e = <-done:
			t.Fatal("revocation did not wait for in-flight authority", e)
		case <-ctx.Done():
			t.Fatal("did not observe membership lock wait", ctx.Err())
		case <-ticker.C:
		}
	}
	if _, e = business.Exec(ctx, `SELECT id FROM projects WHERE id=$1 FOR UPDATE`, f.project); e != nil {
		t.Fatal("revocation held project while awaiting target membership", e)
	}
	if _, e = business.Exec(ctx, `INSERT INTO catalog_events(project_id,event_key,display_name,description,draft_definition) VALUES($1,'before_revocation','Before revocation','','{}')`, f.project); e != nil {
		t.Fatal(e)
	}
	if e = business.Commit(ctx); e != nil {
		t.Fatal(e)
	}
	if e = <-done; e != nil {
		t.Fatal(e)
	}
	if _, e = f.svc.CreateEvent(ctx, f.viewer, f.project, catalogEventInput()); e == nil {
		t.Fatal("removed editor retained write authority")
	}
}

func catalogEventInput() catalog.EventInput {
	return catalog.EventInput{Key: "after_revocation", DisplayName: "After revocation"}
}

func TestMembershipAuditFailureRollsBackAcceptedMutation(t *testing.T) {
	f := catalogDB(t)
	svc := access.NewMemberships(f.s)
	ctx := context.Background()
	if _, e := f.s.Pool.Exec(ctx, `CREATE FUNCTION reject_member_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action LIKE 'project.member_%' THEN RAISE EXCEPTION 'synthetic audit failure'; END IF; RETURN NEW; END $$; CREATE TRIGGER reject_member_audit BEFORE INSERT ON audit_entries FOR EACH ROW EXECUTE FUNCTION reject_member_audit()`); e != nil {
		t.Fatal(e)
	}
	if _, e := svc.Update(ctx, f.owner, f.project, f.viewer, memberInput(1, "audit-retry", "editor")); e == nil {
		t.Fatal("audit failure accepted membership write")
	}
	var revision int64
	var role string
	var records int
	if e := f.s.Pool.QueryRow(ctx, `SELECT membership_revision,(SELECT role FROM project_members WHERE project_id=$1 AND user_id=$2),(SELECT count(*) FROM project_governance_mutations WHERE project_id=$1) FROM projects WHERE id=$1`, f.project, f.viewer).Scan(&revision, &role, &records); e != nil || revision != 1 || role != "viewer" || records != 0 {
		t.Fatal("audit failure left partial state", revision, role, records, e)
	}
	if _, e := f.s.Pool.Exec(ctx, `DROP TRIGGER reject_member_audit ON audit_entries`); e != nil {
		t.Fatal(e)
	}
	if result, e := svc.Update(ctx, f.owner, f.project, f.viewer, memberInput(1, "audit-retry", "editor")); e != nil || result.Revision != 2 {
		t.Fatal("rolled-back key could not be retried", result, e)
	}
}

func TestMembershipCapacityAndRevisionCeiling(t *testing.T) {
	f := catalogDB(t)
	svc := access.NewMemberships(f.s)
	ctx := context.Background()
	if _, e := f.s.Pool.Exec(ctx, `INSERT INTO accounts(id,username,display_name,password_hash) SELECT md5('synthetic-member-'||n)::uuid,'member-'||n,'Synthetic','unused' FROM generate_series(1,998) n`); e != nil {
		t.Fatal(e)
	}
	if _, e := f.s.Pool.Exec(ctx, `INSERT INTO project_members(project_id,user_id,role) SELECT $1,id,'viewer' FROM accounts WHERE username LIKE 'member-%'`, f.project); e != nil {
		t.Fatal(e)
	}
	_, e := svc.Add(ctx, f.owner, f.project, access.MemberInput{Username: "account2", Role: "viewer", Revision: 1, IdempotencyKey: "capacity"})
	faultCode(t, e, "MEMBER_LIMIT_REACHED")
	if _, e = svc.Remove(ctx, f.owner, f.project, f.viewer, memberInput(1, "make-room", "")); e != nil {
		t.Fatal(e)
	}
	if _, e = svc.Add(ctx, f.owner, f.project, access.MemberInput{Username: "account2", Role: "viewer", Revision: 2, IdempotencyKey: "capacity"}); e != nil {
		t.Fatal("capacity rejection reserved the idempotency key", e)
	}
	if _, e = f.s.Pool.Exec(ctx, `UPDATE projects SET membership_revision=$2 WHERE id=$1`, f.project, access.MaxMembershipRevision); e != nil {
		t.Fatal(e)
	}
	_, e = svc.Update(ctx, f.owner, f.project, f.outsider, memberInput(access.MaxMembershipRevision, "ceiling", "editor"))
	faultCode(t, e, "MEMBERSHIP_REVISION_EXHAUSTED")
	for _, bad := range []int64{0, access.MaxMembershipRevision + 1} {
		if _, e = f.s.Pool.Exec(ctx, `UPDATE projects SET membership_revision=$2 WHERE id=$1`, f.project, bad); e == nil {
			t.Fatalf("database accepted unsafe revision %d", bad)
		}
	}
}

func TestMembershipWritesInvalidateRawAndAggregateQueryAuthority(t *testing.T) {
	for _, phase := range []string{"queued-raw", "running-raw", "running-aggregate"} {
		t.Run(phase, func(t *testing.T) {
			f := catalogDB(t)
			svc := access.NewMemberships(f.s)
			ctx := context.Background()
			result, e := svc.Update(ctx, f.owner, f.project, f.viewer, memberInput(1, "grant-editor", "editor"))
			if e != nil {
				t.Fatal(e)
			}
			kind := "events"
			if phase == "running-aggregate" {
				kind = "trend"
			}
			request := queryRequest(t, kind)
			job, e := f.s.EnqueueQuery(ctx, f.viewer, f.project, request)
			if e != nil {
				t.Fatal(e)
			}
			var claim *analysis.Claim
			if phase != "queued-raw" {
				claim, e = f.s.ClaimQuery(ctx, "member-query-worker", time.Minute)
				if e != nil || claim == nil {
					t.Fatal(claim, e)
				}
			}
			if phase == "running-aggregate" {
				_, e = svc.Remove(ctx, f.owner, f.project, f.viewer, memberInput(result.Revision, "remove-query-actor", ""))
			} else {
				_, e = svc.Update(ctx, f.owner, f.project, f.viewer, memberInput(result.Revision, "demote-query-actor", "viewer"))
			}
			if e != nil {
				t.Fatal(e)
			}
			if claim == nil {
				claim, e = f.s.ClaimQuery(ctx, "member-query-worker", time.Minute)
				if e != nil || claim != nil {
					t.Fatal("queued raw query survived demotion", claim, e)
				}
			} else {
				if valid, e := f.s.QueryLeaseValid(ctx, *claim); e != nil || valid {
					t.Fatal("revoked execution lease remained valid", valid, e)
				}
				faultCode(t, f.s.CompleteQuery(ctx, *claim, queryOutcome(job.ID, request)), "QUERY_LEASE_LOST")
			}
			var state string
			if e = f.s.Pool.QueryRow(ctx, `SELECT state FROM analysis_query_jobs WHERE id=$1`, job.ID).Scan(&state); e != nil || state != analysis.JobStateCanceled {
				t.Fatal("revoked query was not canceled", state, e)
			}
		})
	}
}

func TestMembershipConcurrentRetryUsesOneRevisionAndAudit(t *testing.T) {
	f := catalogDB(t)
	svc := access.NewMemberships(f.s)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	in := access.MemberInput{Username: "account2", Role: "editor", Revision: 1, IdempotencyKey: "concurrent-retry"}
	results := make(chan access.MemberResult, 8)
	errs := make(chan error, 8)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			result, e := svc.Add(ctx, f.owner, f.project, in)
			results <- result
			errs <- e
		}()
	}
	wg.Wait()
	close(results)
	close(errs)
	for e := range errs {
		if e != nil {
			t.Fatal(e)
		}
	}
	for result := range results {
		if result.Revision != 2 || result.Member.UserID != f.outsider {
			t.Fatal("concurrent retry diverged", result)
		}
	}
	var audits int
	if e := f.s.Pool.QueryRow(ctx, `SELECT count(*) FROM audit_entries WHERE project_id=$1 AND action='project.member_added'`, f.project).Scan(&audits); e != nil || audits != 1 {
		t.Fatal(fmt.Sprintf("accepted retries emitted %d audits", audits), e)
	}
}
