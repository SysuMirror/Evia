package access

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/howtomakeaname/Evia/backend/internal/platform/fault"
)

type fakeOAuthStore struct {
	requests map[string]OAuthAuthRequest
	resolved Session
	lastSub  string
	lastCand []string
	err      error
}

func newFakeOAuthStore() *fakeOAuthStore {
	return &fakeOAuthStore{requests: map[string]OAuthAuthRequest{}}
}

func (f *fakeOAuthStore) InsertOAuthAuthRequest(_ context.Context, req OAuthAuthRequest) error {
	f.requests[req.StateHash+":"+req.BindingHash] = req
	return nil
}

func (f *fakeOAuthStore) ConsumeOAuthAuthRequest(_ context.Context, stateHash, bindingHash string) (OAuthAuthRequest, error) {
	key := stateHash + ":" + bindingHash
	req, ok := f.requests[key]
	if !ok {
		return OAuthAuthRequest{}, ErrMissing
	}
	delete(f.requests, key)
	return req, nil
}

func (f *fakeOAuthStore) ResolveOAuthSession(_ context.Context, _, subject, _, _, displayName string, candidates []string, _ string, replacement Session) (Session, error) {
	if f.err != nil {
		return Session{}, f.err
	}
	f.lastSub = subject
	f.lastCand = append([]string(nil), candidates...)
	replacement.UserID = "11111111-1111-4111-8111-111111111111"
	replacement.DisplayName = displayName
	f.resolved = replacement
	return replacement, nil
}

type fakeOAuthClient struct {
	tokenRes     OAuthTokenResult
	tokenErr     error
	userInfo     OAuthUserInfo
	userErr      error
	introspectEr error
	lastCode     string
	lastVerifier string
	lastRedirect string
}

func (f *fakeOAuthClient) ExchangeCode(_ context.Context, _ OAuthProviderConfig, code, verifier, redirect string) (OAuthTokenResult, error) {
	f.lastCode = code
	f.lastVerifier = verifier
	f.lastRedirect = redirect
	return f.tokenRes, f.tokenErr
}

func (f *fakeOAuthClient) FetchUserInfo(_ context.Context, _ OAuthProviderConfig, _ string) (OAuthUserInfo, error) {
	return f.userInfo, f.userErr
}

func (f *fakeOAuthClient) IntrospectEmpty(_ context.Context, _ OAuthProviderConfig) error {
	return f.introspectEr
}

func TestOAuthStartAuthorizeAndCompleteCallbackPKCE(t *testing.T) {
	store := newFakeOAuthStore()
	client := &fakeOAuthClient{
		tokenRes: OAuthTokenResult{AccessToken: "tok_abc", TokenType: "Bearer"},
		userInfo: OAuthUserInfo{
			Subject:           "sub-42",
			PreferredUsername: "Alice.Dev",
			Name:              "Alice Zhang",
			Email:             "alice@example.com",
		},
	}
	provider := OAuthProviderConfig{
		ID:            "unisso",
		DisplayName:   "UniSSO",
		ClientID:      "client_123",
		ClientSecret:  "secret_xyz",
		RedirectURI:   "https://evia.example.com/user/login/oauth2/callback",
		AuthorizeURL:  "https://sso.example.com/authorize",
		TokenURL:      "https://sso.example.com/api/oauth/token",
		UserInfoURL:   "https://sso.example.com/api/oauth/userinfo",
		IntrospectURL: "https://sso.example.com/api/oauth/introspect",
		Scopes:        []string{"profile:name", "email"},
	}
	svc := NewOAuth(store, client, []OAuthProviderConfig{provider})

	providers := svc.ListProviders()
	if len(providers) != 1 || providers[0].ID != "unisso" || providers[0].DisplayName != "UniSSO" {
		t.Fatalf("unexpected providers: %+v", providers)
	}

	started, err := svc.StartAuthorize(context.Background(), "unisso", "/invite?token=abc123")
	if err != nil {
		t.Fatalf("StartAuthorize failed: %v", err)
	}
	parsed, err := url.Parse(started.AuthorizationURL)
	if err != nil {
		t.Fatalf("invalid authorization url: %v", err)
	}
	q := parsed.Query()
	if q.Get("client_id") != "client_123" {
		t.Fatalf("unexpected client_id: %q", q.Get("client_id"))
	}
	if q.Get("redirect_uri") != provider.RedirectURI {
		t.Fatalf("redirect_uri mismatch: %q", q.Get("redirect_uri"))
	}
	if q.Get("response_type") != "code" || q.Get("code_challenge_method") != "S256" {
		t.Fatalf("missing required response_type or PKCE S256: %v", q)
	}
	if !strings.HasPrefix(q.Get("scope"), "openid ") {
		t.Fatalf("openid scope must be enforced: %q", q.Get("scope"))
	}
	challenge := q.Get("code_challenge")
	if len(challenge) != 43 || strings.Contains(challenge, "=") {
		t.Fatalf("PKCE S256 code_challenge must be 43 unpadded chars, got %q", challenge)
	}

	state := q.Get("state")
	result, err := svc.CompleteCallback(context.Background(), "auth-code-1", state, started.Binding, nil)
	if err != nil {
		t.Fatalf("CompleteCallback failed: %v", err)
	}
	if len(client.lastVerifier) < 43 || len(client.lastVerifier) > 128 {
		t.Fatalf("code_verifier length out of RFC 7636 range: %d", len(client.lastVerifier))
	}
	sum := sha256.Sum256([]byte(client.lastVerifier))
	if expected := base64.RawURLEncoding.EncodeToString(sum[:]); expected != challenge {
		t.Fatalf("PKCE challenge mismatch: expected %q, got %q", expected, challenge)
	}
	if client.lastRedirect != provider.RedirectURI || client.lastCode != "auth-code-1" {
		t.Fatalf("token exchange received wrong code/redirect: %q %q", client.lastCode, client.lastRedirect)
	}
	if result.NextPath != "/invite?token=abc123" || result.Session.DisplayName != "Alice Zhang" || len(result.SessionSecret) != 43 {
		t.Fatalf("unexpected callback result: %+v", result)
	}
	if store.lastSub != "sub-42" || len(store.lastCand) == 0 || store.lastCand[0] != "alice.dev" {
		t.Fatalf("unexpected derived identity: sub=%q candidates=%v", store.lastSub, store.lastCand)
	}

	// Replaying the same state must fail because auth requests are single-use.
	if _, err := svc.CompleteCallback(context.Background(), "auth-code-1", state, started.Binding, nil); fault.Public(err).Code != "OAUTH_STATE_INVALID" {
		t.Fatalf("expected OAUTH_STATE_INVALID on state replay, got %v", err)
	}
}

func TestOAuthSanitizesUnsafeNextPath(t *testing.T) {
	store := newFakeOAuthStore()
	svc := NewOAuth(store, &fakeOAuthClient{}, []OAuthProviderConfig{{
		ID: "unisso", DisplayName: "UniSSO", ClientID: "c", ClientSecret: "s",
		RedirectURI:  "https://evia.example.com/user/login/oauth2/callback",
		AuthorizeURL: "https://sso.example.com/authorize",
	}})
	svc.now = func() time.Time { return time.Unix(1700000000, 0).UTC() }

	for _, bad := range []string{"https://evil.example/pwn", "//evil.example/pwn", "javascript:alert(1)"} {
		started, err := svc.StartAuthorize(context.Background(), "unisso", bad)
		if err != nil {
			t.Fatalf("StartAuthorize(%q): %v", bad, err)
		}
		u, _ := url.Parse(started.AuthorizationURL)
		req := store.requests[Digest(u.Query().Get("state"))+":"+Digest(started.Binding)]
		if req.NextPath != "/projects" {
			t.Fatalf("unsafe next %q was not sanitized: got %q", bad, req.NextPath)
		}
	}
}
