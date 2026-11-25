//go:build integration

package postgres

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/howtomakeaname/Evia/backend/internal/access"
	"github.com/howtomakeaname/Evia/backend/internal/platform/fault"
	"github.com/howtomakeaname/Evia/backend/internal/platform/ids"
)

func TestOAuthAuthRequestAndProvisioningIntegration(t *testing.T) {
	f := catalogDB(t)
	store := f.s
	ctx := context.Background()

	// 1. Auth request insert & single-use atomic consume
	req := access.OAuthAuthRequest{
		StateHash:    access.Digest("state-int-" + ids.Secret()),
		BindingHash:  access.Digest("bind-int-" + ids.Secret()),
		Provider:     "unisso",
		CodeVerifier: "verifier_1234567890123456789012345678901234567890123",
		RedirectURI:  "https://evia.example.com/user/login/oauth2/callback",
		NextPath:     "/projects",
		ExpiresAt:    time.Now().UTC().Add(10 * time.Minute),
	}
	if err := store.InsertOAuthAuthRequest(ctx, req); err != nil {
		t.Fatalf("InsertOAuthAuthRequest: %v", err)
	}
	consumed, err := store.ConsumeOAuthAuthRequest(ctx, req.StateHash, req.BindingHash)
	if err != nil {
		t.Fatalf("ConsumeOAuthAuthRequest: %v", err)
	}
	if consumed.CodeVerifier != req.CodeVerifier || consumed.RedirectURI != req.RedirectURI {
		t.Fatalf("consumed mismatch: %+v", consumed)
	}
	if _, err := store.ConsumeOAuthAuthRequest(ctx, req.StateHash, req.BindingHash); !errors.Is(err, access.ErrMissing) {
		t.Fatalf("expected ErrMissing on second consume, got %v", err)
	}

	// 2. First OAuth login auto-provisions standard account with zero project memberships
	subject := "unisso-sub-" + ids.New()
	secret1 := ids.Secret()
	sess1, err := store.ResolveOAuthSession(
		ctx,
		"unisso",
		subject,
		"sso_user_a",
		"sso_user_a@example.com",
		"SSO 测试用户",
		[]string{"sso_user_" + subject[:8], "unisso_" + subject[:12]},
		"",
		access.Session{
			SecretHash: access.Digest(secret1),
			CSRF:       ids.Secret(),
			ExpiresAt:  time.Now().UTC().Add(8 * time.Hour),
		},
	)
	if err != nil {
		t.Fatalf("ResolveOAuthSession first login: %v", err)
	}
	projects, err := store.ListProjects(ctx, sess1.UserID, "", 20)
	if err != nil {
		t.Fatalf("ListProjects: %v", err)
	}
	if len(projects) != 0 {
		t.Fatalf("newly provisioned OAuth user must have 0 projects, got %d", len(projects))
	}

	// 3. Second OAuth login reuses the same local account
	secret2 := ids.Secret()
	sess2, err := store.ResolveOAuthSession(
		ctx,
		"unisso",
		subject,
		"sso_user_a",
		"sso_user_a@example.com",
		"SSO 测试用户",
		[]string{"different_candidate"},
		sess1.SecretHash,
		access.Session{
			SecretHash: access.Digest(secret2),
			CSRF:       ids.Secret(),
			ExpiresAt:  time.Now().UTC().Add(8 * time.Hour),
		},
	)
	if err != nil {
		t.Fatalf("ResolveOAuthSession second login: %v", err)
	}
	if sess2.UserID != sess1.UserID {
		t.Fatalf("expected same UserID %s, got %s", sess1.UserID, sess2.UserID)
	}

	// 4. Disabled OAuth account is rejected
	if _, err := store.Pool.Exec(ctx, `UPDATE accounts SET disabled = true WHERE id = $1`, sess1.UserID); err != nil {
		t.Fatalf("disable account: %v", err)
	}
	_, err = store.ResolveOAuthSession(
		ctx,
		"unisso",
		subject,
		"sso_user_a",
		"sso_user_a@example.com",
		"SSO 测试用户",
		[]string{"ignored"},
		"",
		access.Session{
			SecretHash: access.Digest(ids.Secret()),
			CSRF:       ids.Secret(),
			ExpiresAt:  time.Now().UTC().Add(8 * time.Hour),
		},
	)
	if fault.Public(err).Code != "ACCOUNT_DISABLED" {
		t.Fatalf("expected ACCOUNT_DISABLED, got %v", err)
	}
}
