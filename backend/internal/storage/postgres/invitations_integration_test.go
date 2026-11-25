//go:build integration

package postgres

import (
	"context"
	"testing"
	"time"

	"github.com/howtomakeaname/Evia/backend/internal/access"
)

func TestProjectInvitationsLifecycleAndAccountRegistration(t *testing.T) {
	f := catalogDB(t)
	ctx := context.Background()
	invites := access.NewInvitations(f.s)
	authSvc, err := access.New(f.s)
	if err != nil {
		t.Fatal(err)
	}

	// Non-owners cannot list or issue invitations.
	if _, err := invites.List(ctx, f.viewer, f.project, nil, 20); err == nil {
		t.Fatal("viewer must not list invitations")
	} else {
		faultCode(t, err, "MEMBER_MANAGEMENT_FORBIDDEN")
	}
	if _, err := invites.Create(ctx, f.outsider, f.project, access.InvitationCreateInput{Role: access.RoleViewer}); err == nil {
		t.Fatal("outsider must not create invitation")
	} else {
		faultCode(t, err, "PROJECT_NOT_FOUND")
	}

	// Owner creates an invitation for a new editor.
	issued, err := invites.Create(ctx, f.owner, f.project, access.InvitationCreateInput{
		Role:           access.RoleEditor,
		Note:           "前端同学",
		ExpiresInHours: 48,
	})
	if err != nil {
		t.Fatal(err)
	}
	if issued.InviteToken == "" || issued.Invitation.Status != access.InvitationStatusPending || issued.Invitation.Role != access.RoleEditor {
		t.Fatalf("unexpected issued invitation: %+v", issued)
	}

	// Preview works with the raw token and hides invalid tokens uniformly.
	preview, err := invites.Preview(ctx, issued.InviteToken, "127.0.0.1")
	if err != nil || preview.ProjectID != f.project || preview.Role != access.RoleEditor || preview.Note != "前端同学" {
		t.Fatalf("preview mismatch: %+v err=%v", preview, err)
	}
	if _, err := invites.Preview(ctx, "invalid-token-value-1234567890", "127.0.0.1"); err == nil {
		t.Fatal("unknown token should fail preview")
	} else {
		faultCode(t, err, "INVITATION_INVALID")
	}

	// Username collision returns USERNAME_TAKEN without consuming the invitation.
	_, err = invites.Accept(ctx, access.InvitationAcceptInput{
		Token:       issued.InviteToken,
		Username:    "account0",
		DisplayName: "Dup",
		Password:    "strong-password-1234",
		RemoteIP:    "127.0.0.1",
	})
	faultCode(t, err, "USERNAME_TAKEN")

	// New account registers cleanly via the same invitation and receives an active session.
	accepted, err := invites.Accept(ctx, access.InvitationAcceptInput{
		Token:       issued.InviteToken,
		Username:    "new.member",
		DisplayName: "新成员",
		Password:    "strong-password-1234",
		RemoteIP:    "127.0.0.1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if accepted.ProjectID != f.project || accepted.Role != access.RoleEditor || accepted.SessionSecret == "" {
		t.Fatalf("unexpected accept result: %+v", accepted)
	}
	// Session works immediately and role is granted on the project.
	sess, err := authSvc.Authenticate(ctx, accepted.SessionSecret)
	if err != nil || sess.UserID != accepted.Session.UserID {
		t.Fatalf("new account session failed authentication: %+v err=%v", sess, err)
	}
	proj, err := authSvc.GetProject(ctx, sess.UserID, f.project)
	if err != nil || proj.Role != access.RoleEditor {
		t.Fatalf("new account missing project membership: %+v err=%v", proj, err)
	}

	// One-time token cannot be replayed or revoked after acceptance.
	if _, err := invites.Preview(ctx, issued.InviteToken, "127.0.0.1"); err == nil {
		t.Fatal("accepted invitation should fail preview")
	} else {
		faultCode(t, err, "INVITATION_INVALID")
	}
	if _, err := invites.Revoke(ctx, f.owner, f.project, issued.Invitation.ID); err == nil {
		t.Fatal("accepted invitation must not be revocable")
	} else {
		faultCode(t, err, "INVITATION_ALREADY_ACCEPTED")
	}

	// Existing logged-in user (f.outsider) accepts a second invitation.
	issued2, err := invites.Create(ctx, f.owner, f.project, access.InvitationCreateInput{
		Role: access.RoleViewer,
		Note: "跨项目协助",
	})
	if err != nil {
		t.Fatal(err)
	}
	// Existing member (f.viewer) gets MEMBER_EXISTS and invitation remains valid.
	_, err = invites.Accept(ctx, access.InvitationAcceptInput{
		Token:    issued2.InviteToken,
		RemoteIP: "127.0.0.1",
		Session:  &access.Session{UserID: f.viewer, DisplayName: "Viewer", SecretHash: "x", CSRF: "c"},
	})
	faultCode(t, err, "MEMBER_EXISTS")

	// Outsider joins cleanly.
	joined, err := invites.Accept(ctx, access.InvitationAcceptInput{
		Token:    issued2.InviteToken,
		RemoteIP: "127.0.0.1",
		Session:  &access.Session{UserID: f.outsider, DisplayName: "Outsider", SecretHash: "y", CSRF: "c2"},
	})
	if err != nil || joined.Role != access.RoleViewer {
		t.Fatalf("existing account acceptance failed: %+v err=%v", joined, err)
	}

	// Revocation invalidates a pending invitation immediately.
	issued3, err := invites.Create(ctx, f.owner, f.project, access.InvitationCreateInput{Role: access.RoleViewer})
	if err != nil {
		t.Fatal(err)
	}
	revoked, err := invites.Revoke(ctx, f.owner, f.project, issued3.Invitation.ID)
	if err != nil || revoked.Status != access.InvitationStatusRevoked {
		t.Fatalf("revoke failed: %+v err=%v", revoked, err)
	}
	if _, err := invites.Preview(ctx, issued3.InviteToken, "127.0.0.1"); err == nil {
		t.Fatal("revoked invitation should fail preview")
	} else {
		faultCode(t, err, "INVITATION_INVALID")
	}

	// Listing shows all three invitations in newest-first order with pagination.
	page1, err := invites.List(ctx, f.owner, f.project, nil, 2)
	if err != nil || len(page1) != 3 {
		t.Fatalf("expected 3 items (limit+1), got %d err=%v", len(page1), err)
	}
	page2, err := invites.List(ctx, f.owner, f.project, &access.InvitationPosition{CreatedAt: page1[1].CreatedAt, ID: page1[1].ID}, 2)
	if err != nil || len(page2) != 1 || page2[0].ID != issued.Invitation.ID || page2[0].Status != access.InvitationStatusAccepted {
		t.Fatalf("second page mismatch: %+v err=%v", page2, err)
	}
	if page2[0].AcceptedUsername == nil || *page2[0].AcceptedUsername != "new.member" {
		t.Fatalf("expected accepted_username new.member, got %+v", page2[0].AcceptedUsername)
	}

	// Audit entries recorded for created, accepted, and revoked actions.
	var auditCount int
	if err := f.s.Pool.QueryRow(ctx, `SELECT count(*) FROM audit_entries WHERE project_id=$1 AND action IN ('project.invitation_created','project.invitation_accepted','project.invitation_revoked')`, f.project).Scan(&auditCount); err != nil || auditCount != 6 {
		t.Fatalf("expected 6 invitation audit entries (3 created, 2 accepted, 1 revoked), got %d err=%v", auditCount, err)
	}
}

func TestAccountSelfServiceProfileAndPasswordRotation(t *testing.T) {
	f := catalogDB(t)
	ctx := context.Background()
	authSvc, err := access.New(f.s)
	if err != nil {
		t.Fatal(err)
	}
	accounts := access.NewAccounts(f.s)

	initialHash, err := access.PasswordHash("owner-password-123")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.Pool.Exec(ctx, `UPDATE accounts SET password_hash=$2 WHERE id=$1`, f.owner, initialHash); err != nil {
		t.Fatal(err)
	}

	// Log in twice to create two distinct active sessions.
	s1, sec1, err := authSvc.Login(ctx, "account0", "owner-password-123", "127.0.0.1", nil)
	if err != nil {
		t.Fatal(err)
	}
	_, sec2, err := authSvc.Login(ctx, "account0", "owner-password-123", "127.0.0.1", nil)
	if err != nil {
		t.Fatal(err)
	}

	profile, err := accounts.Profile(ctx, s1.UserID)
	if err != nil || profile.Username != "account0" {
		t.Fatalf("profile mismatch: %+v err=%v", profile, err)
	}
	updated, err := accounts.UpdateProfile(ctx, s1.UserID, "项目负责人")
	if err != nil || updated.DisplayName != "项目负责人" {
		t.Fatalf("update profile failed: %+v err=%v", updated, err)
	}

	// Wrong current password fails closed.
	if _, _, err := accounts.ChangePassword(ctx, s1, "wrong-password-123", "new-password-456789"); err == nil {
		t.Fatal("wrong current password accepted")
	} else {
		faultCode(t, err, "INVALID_CREDENTIALS")
	}

	// Valid password change rotates current session and revokes all other sessions.
	rotated, secNew, err := accounts.ChangePassword(ctx, s1, "owner-password-123", "new-password-456789")
	if err != nil || secNew == "" || rotated.DisplayName != "项目负责人" {
		t.Fatalf("password change failed: %+v err=%v", rotated, err)
	}
	if _, err := authSvc.Authenticate(ctx, secNew); err != nil {
		t.Fatalf("rotated session must remain valid: %v", err)
	}
	if _, err := authSvc.Authenticate(ctx, sec1); err == nil {
		t.Fatal("old current session secret must be revoked")
	}
	if _, err := authSvc.Authenticate(ctx, sec2); err == nil {
		t.Fatal("other session must be revoked on password change")
	}
	// New password logs in; old password fails.
	if _, _, err := authSvc.Login(ctx, "account0", "owner-password-123", "127.0.0.1", nil); err == nil {
		t.Fatal("old password should fail login")
	}
	if _, _, err := authSvc.Login(ctx, "account0", "new-password-456789", "127.0.0.1", nil); err != nil {
		t.Fatalf("new password login failed: %v", err)
	}

	// Verify expired invitation transitions cleanly to expired status.
	invites := access.NewInvitations(f.s)
	issued, err := invites.Create(ctx, f.owner, f.project, access.InvitationCreateInput{Role: access.RoleViewer})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.Pool.Exec(ctx, `UPDATE project_invitations SET expires_at=$2 WHERE id=$1`, issued.Invitation.ID, time.Now().UTC().Add(-time.Minute)); err != nil {
		t.Fatal(err)
	}
	if _, err := invites.Preview(ctx, issued.InviteToken, "127.0.0.1"); err == nil {
		t.Fatal("expired invitation should fail preview")
	}
	list, err := invites.List(ctx, f.owner, f.project, nil, 10)
	if err != nil || len(list) == 0 || list[0].Status != access.InvitationStatusExpired {
		t.Fatalf("expected expired status in list, got %+v err=%v", list, err)
	}
}
