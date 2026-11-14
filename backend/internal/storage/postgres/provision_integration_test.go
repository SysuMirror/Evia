//go:build integration

package postgres

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/howtomakeaname/Evia/backend/internal/access"
	"github.com/howtomakeaname/Evia/backend/internal/platform/fault"
	"github.com/howtomakeaname/Evia/backend/internal/platform/ids"
	"golang.org/x/crypto/bcrypt"
)

func TestAccountProvisionRequiresInitializedEnabledAccount(t *testing.T) {
	s := database(t)
	ctx := context.Background()
	_, err := access.ProvisionAccount(ctx, s, "first", "First", "synthetic-password-42")
	if err == nil || fault.Public(err).Code != "ACCOUNT_PROVISION_REQUIRES_BOOTSTRAP" {
		t.Fatal("provisioning created initial account", err)
	}
	var accounts, events int
	if err = s.Pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM accounts),(SELECT count(*) FROM account_provision_events)`).Scan(&accounts, &events); err != nil || accounts != 0 || events != 0 {
		t.Fatal("failed provisioning left rows", accounts, events, err)
	}
	if err = s.Bootstrap(ctx, "initial", "Initial", "synthetic-password-42"); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Pool.Exec(ctx, `UPDATE accounts SET disabled=true`); err != nil {
		t.Fatal(err)
	}
	if _, err = access.ProvisionAccount(ctx, s, "recovery", "Recovery", "synthetic-password-42"); err == nil || fault.Public(err).Code != "ACCOUNT_PROVISION_REQUIRES_BOOTSTRAP" {
		t.Fatal("provisioning bypassed disabled installation", err)
	}
}

func TestAccountProvisionNoOverwriteAndNoAuthority(t *testing.T) {
	s := database(t)
	ctx := context.Background()
	if err := s.Bootstrap(ctx, "initial", "Initial", "synthetic-password-42"); err != nil {
		t.Fatal(err)
	}
	user, err := access.ProvisionAccount(ctx, s, "  New.Account  ", "Synthetic member", "synthetic-password-42")
	if err != nil {
		t.Fatal(err)
	}
	stored, err := s.FindUser(ctx, "new.account")
	if err != nil || stored.ID != user.ID || stored.Disabled || bcrypt.CompareHashAndPassword([]byte(stored.PasswordHash), []byte("synthetic-password-42")) != nil {
		t.Fatal("new account cannot use normalized credentials", err)
	}
	var kind, actorKind, target string
	var events, memberships, sessions int
	err = s.Pool.QueryRow(ctx, `SELECT p.kind,e.actor_kind,e.target_id::text,(SELECT count(*) FROM account_provision_events),
	(SELECT count(*) FROM project_members WHERE user_id=$1),(SELECT count(*) FROM sessions WHERE user_id=$1)
	FROM principals p JOIN account_provision_events e ON e.target_id=p.id WHERE p.id=$1`, user.ID).Scan(&kind, &actorKind, &target, &events, &memberships, &sessions)
	if err != nil || kind != "human" || actorKind != "operator" || target != user.ID || events != 1 || memberships != 0 || sessions != 0 {
		t.Fatal("provisioning registry/audit/scope boundary failed", kind, actorKind, events, memberships, sessions, err)
	}
	if _, err = access.ProvisionAccount(ctx, s, "NEW.ACCOUNT", "Replacement", "different-password-42"); err == nil || fault.Public(err).Code != "ACCOUNT_USERNAME_EXISTS" {
		t.Fatal("duplicate username accepted", err)
	}
	after, err := s.FindUser(ctx, "new.account")
	if err != nil || after != stored {
		t.Fatal("duplicate changed existing account", err)
	}
	if err = s.Pool.QueryRow(ctx, `SELECT count(*) FROM account_provision_events`).Scan(&events); err != nil || events != 1 {
		t.Fatal("duplicate emitted a successful provision event", events, err)
	}
	if err = s.Bootstrap(ctx, "overwrite", "Overwrite", "different-password-42"); err == nil || fault.Public(err).Code != "BOOTSTRAP_COMPLETE" {
		t.Fatal("bootstrap semantics changed", err)
	}
}

func TestAccountProvisionConcurrentDuplicateAndAuditRollback(t *testing.T) {
	s := database(t)
	ctx := context.Background()
	if err := s.Bootstrap(ctx, "initial", "Initial", "synthetic-password-42"); err != nil {
		t.Fatal(err)
	}
	hash, err := access.PasswordHash("synthetic-password-42")
	if err != nil {
		t.Fatal(err)
	}
	var successes atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Go(func() {
			err := s.ProvisionAccount(ctx, access.User{ID: ids.New(), Username: "concurrent", DisplayName: "Concurrent", PasswordHash: hash})
			if err == nil {
				successes.Add(1)
			} else if fault.Public(err).Code != "ACCOUNT_USERNAME_EXISTS" {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	var accounts, events int
	if err = s.Pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM accounts WHERE username='concurrent'),(SELECT count(*) FROM account_provision_events)`).Scan(&accounts, &events); err != nil || successes.Load() != 1 || accounts != 1 || events != 1 {
		t.Fatal("concurrent duplicate was not consumed once", successes.Load(), accounts, events, err)
	}
	if _, err = s.Pool.Exec(ctx, `CREATE FUNCTION deny_account_provision_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'synthetic audit failure'; END $$;
	CREATE TRIGGER deny_account_provision_audit BEFORE INSERT ON account_provision_events FOR EACH ROW EXECUTE FUNCTION deny_account_provision_audit()`); err != nil {
		t.Fatal(err)
	}
	id := ids.New()
	if err = s.ProvisionAccount(ctx, access.User{ID: id, Username: "rollback", DisplayName: "Rollback", PasswordHash: hash}); err == nil {
		t.Fatal("audit failure accepted")
	}
	if _, err = s.FindUser(ctx, "rollback"); !errors.Is(err, access.ErrMissing) {
		t.Fatal("audit failure left account", err)
	}
	var principals int
	if err = s.Pool.QueryRow(ctx, `SELECT count(*) FROM principals WHERE id=$1`, id).Scan(&principals); err != nil || principals != 0 {
		t.Fatal("audit failure left principal", principals, err)
	}
	if err = s.Pool.QueryRow(ctx, `SELECT count(*) FROM account_provision_events`).Scan(&events); err != nil || events != 1 {
		t.Fatal("audit failure changed event history", events, err)
	}
}

func TestSetAccountDisabledGuardAndRecovery(t *testing.T) {
	s := database(t)
	ctx := context.Background()
	if err := s.Bootstrap(ctx, "initial", "Initial", "synthetic-password-42"); err != nil {
		t.Fatal(err)
	}

	if _, err := access.SetAccountDisabled(ctx, s, "ghost", true); err == nil || fault.Public(err).Code != "ACCOUNT_NOT_FOUND" {
		t.Fatalf("unknown account must be not-found: %v", err)
	}

	// The bootstrapped account is the only enabled account and cannot be disabled.
	if _, err := access.SetAccountDisabled(ctx, s, "initial", true); err == nil || fault.Public(err).Code != "LAST_ENABLED_ACCOUNT" {
		t.Fatalf("disabling the last enabled account must be rejected: %v", err)
	}

	second, err := access.ProvisionAccount(ctx, s, "second", "Second", "synthetic-password-42")
	if err != nil {
		t.Fatal(err)
	}
	disabled, err := access.SetAccountDisabled(ctx, s, "second", true)
	if err != nil || !disabled.Disabled || disabled.PasswordHash != "" {
		t.Fatalf("disable with a second enabled account must succeed: %+v %v", disabled, err)
	}
	stored, err := s.FindUser(ctx, "second")
	if err != nil || !stored.Disabled || stored.ID != second.ID {
		t.Fatal("disabled state was not persisted", err)
	}

	enabled, err := access.SetAccountDisabled(ctx, s, "SECOND", false)
	if err != nil || enabled.Disabled {
		t.Fatalf("recovery must re-enable the account: %+v %v", enabled, err)
	}
	recovered, err := s.FindUser(ctx, "second")
	if err != nil || recovered.Disabled {
		t.Fatal("account was not recovered", err)
	}
}

func TestSetAccountDisabledImmediatelyRevokesAndRestoresSession(t *testing.T) {
	s := database(t)
	ctx := context.Background()
	if err := s.Bootstrap(ctx, "initial", "Initial", "synthetic-password-42"); err != nil {
		t.Fatal(err)
	}
	target, err := access.ProvisionAccount(ctx, s, "session-user", "Session User", "synthetic-password-42")
	if err != nil {
		t.Fatal(err)
	}

	secret := ids.Secret()
	session := access.Session{
		UserID:     target.ID,
		SecretHash: access.Digest(secret),
		CSRF:       ids.Secret(),
		ExpiresAt:  time.Now().Add(time.Hour),
	}
	if err = s.InsertSession(ctx, session); err != nil {
		t.Fatal(err)
	}
	if _, err = s.FindSession(ctx, session.SecretHash); err != nil {
		t.Fatalf("active account session must resolve: %v", err)
	}

	// Through the real operator path, disabling must reject the existing session.
	if _, err = access.SetAccountDisabled(ctx, s, "session-user", true); err != nil {
		t.Fatal(err)
	}
	if _, err = s.FindSession(ctx, session.SecretHash); !errors.Is(err, access.ErrMissing) {
		t.Fatalf("disabled account retained session access: %v", err)
	}

	// Re-enabling restores the same still-unexpired session.
	if _, err = access.SetAccountDisabled(ctx, s, "SESSION-USER", false); err != nil {
		t.Fatal(err)
	}
	resolved, err := s.FindSession(ctx, session.SecretHash)
	if err != nil || resolved.UserID != target.ID {
		t.Fatalf("re-enabled account session must resolve again: %v %v", resolved, err)
	}
}

func TestResetAccountPasswordReplacesHashAndRevokesSessions(t *testing.T) {
	s := database(t)
	ctx := context.Background()
	if err := s.Bootstrap(ctx, "initial", "Initial", "synthetic-password-42"); err != nil {
		t.Fatal(err)
	}
	target, err := access.ProvisionAccount(ctx, s, "recovery-user", "Recovery User", "synthetic-old-password-42")
	if err != nil {
		t.Fatal(err)
	}

	secret := ids.Secret()
	if err = s.InsertSession(ctx, access.Session{
		UserID: target.ID, SecretHash: access.Digest(secret), CSRF: ids.Secret(),
		ExpiresAt: time.Now().Add(time.Hour),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err = s.FindSession(ctx, access.Digest(secret)); err != nil {
		t.Fatalf("session must resolve before reset: %v", err)
	}

	if _, err = access.ResetAccountPassword(ctx, s, "RECOVERY-USER", "synthetic-new-password-42"); err != nil {
		t.Fatal(err)
	}

	// The old credential no longer verifies; the new one does, at bcrypt cost 12.
	stored, err := s.FindUser(ctx, "recovery-user")
	if err != nil {
		t.Fatal(err)
	}
	if bcrypt.CompareHashAndPassword([]byte(stored.PasswordHash), []byte("synthetic-old-password-42")) == nil {
		t.Fatal("old password still verifies after reset")
	}
	if bcrypt.CompareHashAndPassword([]byte(stored.PasswordHash), []byte("synthetic-new-password-42")) != nil {
		t.Fatal("new password does not verify after reset")
	}
	if cost, err := bcrypt.Cost([]byte(stored.PasswordHash)); err != nil || cost != 12 {
		t.Fatalf("bcrypt cost changed: %d %v", cost, err)
	}

	// Existing sessions are revoked in the same transaction.
	if _, err = s.FindSession(ctx, access.Digest(secret)); !errors.Is(err, access.ErrMissing) {
		t.Fatalf("reset must revoke existing sessions: %v", err)
	}

	if _, err = access.ResetAccountPassword(ctx, s, "ghost", "synthetic-new-password-42"); err == nil ||
		fault.Public(err).Code != "ACCOUNT_NOT_FOUND" {
		t.Fatalf("unknown account must be not-found: %v", err)
	}
}
