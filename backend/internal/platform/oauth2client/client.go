// Package oauth2client performs bounded backchannel OAuth2 token, userinfo, and introspect calls.
package oauth2client

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/howtomakeaname/Evia/backend/internal/access"
	"github.com/howtomakeaname/Evia/backend/internal/platform/fault"
)

const maxResponseBytes = 64 * 1024

type Client struct {
	http *http.Client
}

func New(httpClient *http.Client) *Client {
	if httpClient == nil {
		httpClient = &http.Client{
			Timeout: 15 * time.Second,
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		}
	}
	return &Client{http: httpClient}
}

var _ access.OAuthClient = (*Client)(nil)

type oauthErrorBody struct {
	Error            string `json:"error"`
	ErrorDescription string `json:"error_description"`
}

func (c *Client) ExchangeCode(ctx context.Context, provider access.OAuthProviderConfig, code, codeVerifier, redirectURI string) (access.OAuthTokenResult, error) {
	form := url.Values{}
	form.Set("grant_type", "authorization_code")
	form.Set("code", code)
	form.Set("redirect_uri", redirectURI)
	form.Set("client_id", provider.ClientID)
	form.Set("client_secret", provider.ClientSecret)
	form.Set("code_verifier", codeVerifier)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, provider.TokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return access.OAuthTokenResult{}, fault.New(fault.Unavailable, "OAUTH_PROVIDER_ERROR", "无法构造单点登录令牌请求")
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return access.OAuthTokenResult{}, fault.New(fault.Unavailable, "OAUTH_PROVIDER_UNAVAILABLE", "无法连接单点登录服务，请稍后重试")
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if err != nil || len(body) > maxResponseBytes {
		return access.OAuthTokenResult{}, fault.New(fault.Unavailable, "OAUTH_PROVIDER_ERROR", "单点登录令牌响应异常")
	}

	if resp.StatusCode != http.StatusOK {
		var errBody oauthErrorBody
		_ = json.Unmarshal(body, &errBody)
		switch strings.TrimSpace(errBody.Error) {
		case "invalid_grant":
			return access.OAuthTokenResult{}, fault.New(fault.Invalid, "OAUTH_CODE_INVALID", "授权码无效、已过期或已被使用，请重新登录")
		case "invalid_client":
			return access.OAuthTokenResult{}, fault.New(fault.Unavailable, "OAUTH_CLIENT_MISCONFIGURED", "单点登录客户端凭据校验失败，请联系管理员")
		default:
			return access.OAuthTokenResult{}, fault.New(fault.Unavailable, "OAUTH_PROVIDER_ERROR", "单点登录换取令牌失败，请重试")
		}
	}

	var payload struct {
		AccessToken string `json:"access_token"`
		TokenType   string `json:"token_type"`
		Scope       string `json:"scope"`
		SSOSub      string `json:"sso_sub"`
		Sub         string `json:"sub"`
	}
	if err := json.Unmarshal(body, &payload); err != nil || strings.TrimSpace(payload.AccessToken) == "" {
		return access.OAuthTokenResult{}, fault.New(fault.Unavailable, "OAUTH_PROVIDER_ERROR", "单点登录令牌格式无效")
	}
	sub := strings.TrimSpace(payload.Sub)
	if sub == "" {
		sub = strings.TrimSpace(payload.SSOSub)
	}
	return access.OAuthTokenResult{
		AccessToken: payload.AccessToken,
		TokenType:   payload.TokenType,
		Scope:       payload.Scope,
		Subject:     sub,
	}, nil
}

func (c *Client) FetchUserInfo(ctx context.Context, provider access.OAuthProviderConfig, accessToken string) (access.OAuthUserInfo, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, provider.UserInfoURL, nil)
	if err != nil {
		return access.OAuthUserInfo{}, fault.New(fault.Unavailable, "OAUTH_PROVIDER_ERROR", "无法构造用户信息请求")
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Accept", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return access.OAuthUserInfo{}, fault.New(fault.Unavailable, "OAUTH_PROVIDER_UNAVAILABLE", "无法连接单点登录用户信息接口")
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if err != nil || len(body) > maxResponseBytes {
		return access.OAuthUserInfo{}, fault.New(fault.Unavailable, "OAUTH_PROVIDER_ERROR", "单点登录用户信息响应异常")
	}

	if resp.StatusCode != http.StatusOK {
		return access.OAuthUserInfo{}, fault.New(fault.Unavailable, "OAUTH_PROVIDER_ERROR", "获取单点登录用户信息失败，请重试")
	}

	var payload struct {
		Sub               string `json:"sub"`
		SSOSub            string `json:"sso_sub"`
		PreferredUsername string `json:"preferred_username"`
		Name              string `json:"name"`
		Email             string `json:"email"`
		Picture           string `json:"picture"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return access.OAuthUserInfo{}, fault.New(fault.Unavailable, "OAUTH_PROVIDER_ERROR", "单点登录用户信息格式无效")
	}
	subject := strings.TrimSpace(payload.Sub)
	if subject == "" {
		subject = strings.TrimSpace(payload.SSOSub)
	}
	return access.OAuthUserInfo{
		Subject:           subject,
		PreferredUsername: payload.PreferredUsername,
		Name:              payload.Name,
		Email:             payload.Email,
		Picture:           payload.Picture,
	}, nil
}

// IntrospectEmpty runs the provider deployment self-check: calling introspect
// with client credentials and an empty token must return HTTP 200 {"active":false}.
func (c *Client) IntrospectEmpty(ctx context.Context, provider access.OAuthProviderConfig) error {
	if strings.TrimSpace(provider.IntrospectURL) == "" {
		return errors.New("oauth provider introspect url is not configured")
	}
	form := url.Values{}
	form.Set("client_id", provider.ClientID)
	form.Set("client_secret", provider.ClientSecret)
	form.Set("token", "")

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, provider.IntrospectURL, strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if err != nil || len(body) > maxResponseBytes {
		return errors.New("oauth introspect response unreadable")
	}
	if resp.StatusCode != http.StatusOK {
		return errors.New("oauth introspect rejected client credentials")
	}
	var payload struct {
		Active *bool `json:"active"`
	}
	if err := json.Unmarshal(body, &payload); err != nil || payload.Active == nil || *payload.Active {
		return errors.New("oauth introspect returned unexpected payload")
	}
	return nil
}
