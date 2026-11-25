package control

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/howtomakeaname/Evia/backend/internal/access"
	"github.com/howtomakeaname/Evia/backend/internal/platform/contract"
	"github.com/howtomakeaname/Evia/backend/internal/platform/httpapi"
)

type stubOAuthService struct {
	providers    []access.OAuthProviderSummary
	startResult  access.OAuthAuthorizeStart
	startErr     error
	callbackRes  access.OAuthCallbackResult
	callbackErr  error
	lastProvider string
	lastNext     string
	lastCode     string
	lastState    string
	lastBinding  string
}

func (s *stubOAuthService) ListProviders() []access.OAuthProviderSummary { return s.providers }
func (s *stubOAuthService) StartAuthorize(_ context.Context, providerID, rawNext string) (access.OAuthAuthorizeStart, error) {
	s.lastProvider = providerID
	s.lastNext = rawNext
	return s.startResult, s.startErr
}
func (s *stubOAuthService) CompleteCallback(_ context.Context, code, state, binding string, _ *access.Session) (access.OAuthCallbackResult, error) {
	s.lastCode = code
	s.lastState = state
	s.lastBinding = binding
	return s.callbackRes, s.callbackErr
}

func TestOAuthControlEndpoints(t *testing.T) {
	validator, err := contract.New()
	if err != nil {
		t.Fatalf("contract.New: %v", err)
	}
	oauthSvc := &stubOAuthService{
		providers: []access.OAuthProviderSummary{{ID: "unisso", DisplayName: "UniSSO"}},
		startResult: access.OAuthAuthorizeStart{
			AuthorizationURL: "https://sso.example.com/authorize?client_id=cid&state=st1",
			Binding:          "bind-token-123",
			MaxAgeSeconds:    600,
		},
		callbackRes: access.OAuthCallbackResult{
			Session: access.Session{
				UserID:      "11111111-1111-4111-8111-111111111111",
				DisplayName: "Alice",
				CSRF:        "csrf-abc",
				ExpiresAt:   time.Now().Add(8 * time.Hour),
			},
			SessionSecret: strings.Repeat("a", 43),
			NextPath:      "/projects",
		},
	}
	api := &API{
		Boundary:      &httpapi.Boundary{Validator: validator},
		OAuth:         oauthSvc,
		Origin:        "https://evia.example.com",
		SecureCookies: true,
	}
	h := api.Handler()

	// 1. List providers
	req := httptest.NewRequest(http.MethodGet, "/api/v1/auth/oauth2/providers", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("providers status=%d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"id":"unisso"`) {
		t.Fatalf("unexpected providers body: %s", rec.Body.String())
	}

	// 2. Start authorize sets binding cookie and returns authorization_url
	req = httptest.NewRequest(http.MethodPost, "/api/v1/auth/oauth2/authorize", bytes.NewBufferString(`{"provider":"unisso","next":"/projects"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", "https://evia.example.com")
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("authorize status=%d body=%s", rec.Code, rec.Body.String())
	}
	var bindingCookie *http.Cookie
	for _, c := range rec.Result().Cookies() {
		if c.Name == oauthBindingCookieName {
			bindingCookie = c
		}
	}
	if bindingCookie == nil || bindingCookie.Value != "bind-token-123" || !bindingCookie.HttpOnly || bindingCookie.SameSite != http.SameSiteLaxMode {
		t.Fatalf("invalid oauth binding cookie: %+v", bindingCookie)
	}

	// 3. Callback without binding cookie is rejected
	req = httptest.NewRequest(http.MethodPost, "/api/v1/auth/oauth2/callback", bytes.NewBufferString(`{"code":"c1","state":"st1"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", "https://evia.example.com")
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 without binding cookie, got %d body=%s", rec.Code, rec.Body.String())
	}

	// 4. Callback with binding cookie establishes evia_session and clears binding cookie
	req = httptest.NewRequest(http.MethodPost, "/api/v1/auth/oauth2/callback", bytes.NewBufferString(`{"code":"c1","state":"st1"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", "https://evia.example.com")
	req.AddCookie(bindingCookie)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("callback status=%d body=%s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Session struct {
			UserID      string `json:"user_id"`
			DisplayName string `json:"display_name"`
			CSRFToken   string `json:"csrf_token"`
		} `json:"session"`
		NextPath string `json:"next_path"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode callback response: %v", err)
	}
	if resp.Session.DisplayName != "Alice" || resp.NextPath != "/projects" {
		t.Fatalf("unexpected callback payload: %+v", resp)
	}
	var gotSession, clearedBinding bool
	for _, c := range rec.Result().Cookies() {
		if c.Name == "evia_session" && c.Value == strings.Repeat("a", 43) && c.HttpOnly {
			gotSession = true
		}
		if c.Name == oauthBindingCookieName && c.MaxAge < 0 {
			clearedBinding = true
		}
	}
	if !gotSession || !clearedBinding {
		t.Fatalf("expected evia_session cookie set and binding cookie cleared; cookies=%+v", rec.Result().Cookies())
	}
}
