package oauth2client

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/howtomakeaname/Evia/backend/internal/access"
	"github.com/howtomakeaname/Evia/backend/internal/platform/fault"
)

func TestClientTokenUserInfoAndIntrospect(t *testing.T) {
	usedCodes := map[string]bool{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/oauth/token":
			if err := r.ParseForm(); err != nil {
				http.Error(w, "bad form", http.StatusBadRequest)
				return
			}
			if r.FormValue("client_id") != "cid" || r.FormValue("client_secret") != "csec" {
				w.WriteHeader(http.StatusUnauthorized)
				_ = json.NewEncoder(w).Encode(map[string]string{"error": "invalid_client"})
				return
			}
			code := r.FormValue("code")
			if code == "" || usedCodes[code] || r.FormValue("code_verifier") != "verifier_43_chars_abcdefghijklmnopqrstuvwxyz" {
				w.WriteHeader(http.StatusBadRequest)
				_ = json.NewEncoder(w).Encode(map[string]string{"error": "invalid_grant"})
				return
			}
			usedCodes[code] = true
			_ = json.NewEncoder(w).Encode(map[string]any{
				"access_token": "acc_123",
				"token_type":   "Bearer",
				"expires_in":   3600,
				"sso_sub":      "sub_from_token",
			})
		case "/api/oauth/userinfo":
			if r.Header.Get("Authorization") != "Bearer acc_123" {
				w.WriteHeader(http.StatusUnauthorized)
				_ = json.NewEncoder(w).Encode(map[string]string{"error": "invalid_token"})
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"sub":                "user_sub_99",
				"preferred_username": "bob",
				"name":               "Bob Li",
				"email":              "bob@example.com",
			})
		case "/api/oauth/introspect":
			_ = r.ParseForm()
			if r.FormValue("client_id") != "cid" || r.FormValue("client_secret") != "csec" {
				w.WriteHeader(http.StatusUnauthorized)
				_ = json.NewEncoder(w).Encode(map[string]string{"error": "invalid_client"})
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"active": false})
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	provider := access.OAuthProviderConfig{
		ID:            "unisso",
		ClientID:      "cid",
		ClientSecret:  "csec",
		RedirectURI:   "https://evia.example.com/user/login/oauth2/callback",
		TokenURL:      srv.URL + "/api/oauth/token",
		UserInfoURL:   srv.URL + "/api/oauth/userinfo",
		IntrospectURL: srv.URL + "/api/oauth/introspect",
	}
	client := New(srv.Client())

	if err := client.IntrospectEmpty(context.Background(), provider); err != nil {
		t.Fatalf("IntrospectEmpty failed: %v", err)
	}

	tok, err := client.ExchangeCode(context.Background(), provider, "code_once", "verifier_43_chars_abcdefghijklmnopqrstuvwxyz", provider.RedirectURI)
	if err != nil || tok.AccessToken != "acc_123" {
		t.Fatalf("ExchangeCode failed: tok=%+v err=%v", tok, err)
	}

	// Reusing a single-use code returns OAUTH_CODE_INVALID.
	if _, err := client.ExchangeCode(context.Background(), provider, "code_once", "verifier_43_chars_abcdefghijklmnopqrstuvwxyz", provider.RedirectURI); fault.Public(err).Code != "OAUTH_CODE_INVALID" {
		t.Fatalf("expected OAUTH_CODE_INVALID on code replay, got %v", err)
	}

	// Bad client_secret returns OAUTH_CLIENT_MISCONFIGURED on token and error on introspect.
	badProvider := provider
	badProvider.ClientSecret = "wrong"
	if _, err := client.ExchangeCode(context.Background(), badProvider, "code_2", "verifier_43_chars_abcdefghijklmnopqrstuvwxyz", provider.RedirectURI); fault.Public(err).Code != "OAUTH_CLIENT_MISCONFIGURED" {
		t.Fatalf("expected OAUTH_CLIENT_MISCONFIGURED, got %v", err)
	}
	if err := client.IntrospectEmpty(context.Background(), badProvider); err == nil {
		t.Fatal("expected IntrospectEmpty to fail with bad client_secret")
	}

	info, err := client.FetchUserInfo(context.Background(), provider, tok.AccessToken)
	if err != nil || info.Subject != "user_sub_99" || info.Name != "Bob Li" {
		t.Fatalf("FetchUserInfo failed: info=%+v err=%v", info, err)
	}
}
