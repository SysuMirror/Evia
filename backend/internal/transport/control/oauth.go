package control

import (
	"context"
	"net/http"
	"time"

	"github.com/howtomakeaname/Evia/backend/internal/access"
	"github.com/howtomakeaname/Evia/backend/internal/gen/wire"
	"github.com/howtomakeaname/Evia/backend/internal/platform/fault"
)

const oauthBindingCookieName = "evia_oauth_txn"

type OAuthService interface {
	ListProviders() []access.OAuthProviderSummary
	StartAuthorize(ctx context.Context, providerID, rawNext string) (access.OAuthAuthorizeStart, error)
	CompleteCallback(ctx context.Context, code, state, binding string, prior *access.Session) (access.OAuthCallbackResult, error)
}

func (a *API) registerOAuth(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/auth/oauth2/providers", a.Boundary.Handler(a.listOAuthProviders))
	mux.HandleFunc("POST /api/v1/auth/oauth2/authorize", a.Boundary.Handler(a.startOAuthAuthorize))
	mux.HandleFunc("POST /api/v1/auth/oauth2/callback", a.Boundary.Handler(a.completeOAuthCallback))
}

func (a *API) oauthBindingCookie(w http.ResponseWriter, value string, maxAge int) {
	c := &http.Cookie{
		Name:     oauthBindingCookieName,
		Value:    value,
		Path:     "/api/v1/auth/oauth2",
		HttpOnly: true,
		Secure:   a.SecureCookies,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   maxAge,
	}
	if maxAge < 0 {
		c.Expires = time.Unix(1, 0)
	}
	http.SetCookie(w, c)
}

func (a *API) listOAuthProviders(w http.ResponseWriter, r *http.Request) error {
	if len(r.URL.RawQuery) != 0 {
		return fault.New(fault.Invalid, "INVALID_QUERY", "查询参数不符合接口定义")
	}
	items := make([]wire.OAuthProvider, 0)
	if a.OAuth != nil {
		for _, p := range a.OAuth.ListProviders() {
			items = append(items, wire.OAuthProvider{
				ID:          p.ID,
				DisplayName: p.DisplayName,
			})
		}
	}
	return a.Boundary.JSON(w, http.StatusOK, "OAuthProviderList", wire.OAuthProviderList{Items: items})
}

func (a *API) startOAuthAuthorize(w http.ResponseWriter, r *http.Request) error {
	if len(r.Header.Values("Authorization")) != 0 {
		return fault.New(fault.Unauthenticated, "AUTHENTICATION_MODE_INVALID", "登录接口不接受 Bearer 凭据")
	}
	if err := a.origin(r); err != nil {
		return err
	}
	if a.OAuth == nil {
		return fault.New(fault.NotFound, "OAUTH_PROVIDER_NOT_FOUND", "当前实例未启用单点登录")
	}
	var in wire.OAuthAuthorizeInput
	if err := a.Boundary.Decode(w, r, "OAuthAuthorizeInput", &in); err != nil {
		return err
	}
	nextPath := ""
	if in.Next != nil {
		nextPath = *in.Next
	}
	started, err := a.OAuth.StartAuthorize(r.Context(), in.Provider, nextPath)
	if err != nil {
		return err
	}
	a.oauthBindingCookie(w, started.Binding, started.MaxAgeSeconds)
	return a.Boundary.JSON(w, http.StatusOK, "OAuthAuthorizeResponse", wire.OAuthAuthorizeResponse{
		AuthorizationURL: started.AuthorizationURL,
	})
}

func (a *API) completeOAuthCallback(w http.ResponseWriter, r *http.Request) error {
	if len(r.Header.Values("Authorization")) != 0 {
		return fault.New(fault.Unauthenticated, "AUTHENTICATION_MODE_INVALID", "登录接口不接受 Bearer 凭据")
	}
	if err := a.origin(r); err != nil {
		return err
	}
	if a.OAuth == nil {
		return fault.New(fault.NotFound, "OAUTH_PROVIDER_NOT_FOUND", "当前实例未启用单点登录")
	}
	var in wire.OAuthCallbackInput
	if err := a.Boundary.Decode(w, r, "OAuthCallbackInput", &in); err != nil {
		return err
	}
	bindingCookies := r.CookiesNamed(oauthBindingCookieName)
	if len(bindingCookies) != 1 || bindingCookies[0].Value == "" {
		return fault.New(fault.Invalid, "OAUTH_STATE_INVALID", "登录会话校验失败，请重新发起单点登录")
	}

	var prior *access.Session
	sessionCookies := r.CookiesNamed("evia_session")
	if len(sessionCookies) == 1 && a.Access != nil {
		session, err := a.Access.Authenticate(r.Context(), sessionCookies[0].Value)
		if err != nil && fault.Public(err).Kind != fault.Unauthenticated {
			return err
		}
		if err == nil {
			prior = &session
		}
	}

	result, err := a.OAuth.CompleteCallback(r.Context(), in.Code, in.State, bindingCookies[0].Value, prior)
	a.oauthBindingCookie(w, "", -1)
	if err != nil {
		return err
	}
	a.cookie(w, result.SessionSecret, 8*60*60)
	return a.Boundary.JSON(w, http.StatusOK, "OAuthCallbackResponse", wire.OAuthCallbackResponse{
		Session:  sessionWire(result.Session),
		NextPath: result.NextPath,
	})
}
