package access

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"net/url"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/howtomakeaname/Evia/backend/internal/platform/fault"
	"github.com/howtomakeaname/Evia/backend/internal/platform/ids"
)

const (
	// OAuthPasswordSentinel marks accounts created via external OAuth2 without a
	// local password. bcrypt always rejects this non-hash value.
	OAuthPasswordSentinel = "!oauth2"

	oauthAuthRequestTTL = 10 * time.Minute
	oauthSessionTTL     = 8 * time.Hour
)

var oauthProviderIDPattern = regexp.MustCompile(`^[a-z0-9_-]{1,64}$`)

type OAuthProviderConfig struct {
	ID            string
	DisplayName   string
	ClientID      string
	ClientSecret  string
	RedirectURI   string
	AuthorizeURL  string
	TokenURL      string
	UserInfoURL   string
	IntrospectURL string
	Scopes        []string
}

type OAuthProviderSummary struct {
	ID          string
	DisplayName string
}

type OAuthAuthRequest struct {
	StateHash    string
	BindingHash  string
	Provider     string
	CodeVerifier string
	RedirectURI  string
	NextPath     string
	ExpiresAt    time.Time
}

type OAuthTokenResult struct {
	AccessToken string
	TokenType   string
	Scope       string
	Subject     string
}

type OAuthUserInfo struct {
	Subject           string
	PreferredUsername string
	Name              string
	Email             string
	Picture           string
}

type OAuthAuthorizeStart struct {
	AuthorizationURL string
	Binding          string
	MaxAgeSeconds    int
}

type OAuthCallbackResult struct {
	Session       Session
	SessionSecret string
	NextPath      string
}

type OAuthClient interface {
	ExchangeCode(ctx context.Context, provider OAuthProviderConfig, code, codeVerifier, redirectURI string) (OAuthTokenResult, error)
	FetchUserInfo(ctx context.Context, provider OAuthProviderConfig, accessToken string) (OAuthUserInfo, error)
	IntrospectEmpty(ctx context.Context, provider OAuthProviderConfig) error
}

type OAuthStore interface {
	InsertOAuthAuthRequest(ctx context.Context, req OAuthAuthRequest) error
	ConsumeOAuthAuthRequest(ctx context.Context, stateHash, bindingHash string) (OAuthAuthRequest, error)
	ResolveOAuthSession(ctx context.Context, provider, subject, externalUsername, email, displayName string, usernameCandidates []string, priorSessionHash string, replacement Session) (Session, error)
}

type OAuthService struct {
	store     OAuthStore
	client    OAuthClient
	providers []OAuthProviderConfig
	byID      map[string]OAuthProviderConfig
	now       func() time.Time
}

func NewOAuth(store OAuthStore, client OAuthClient, providers []OAuthProviderConfig) *OAuthService {
	copied := make([]OAuthProviderConfig, 0, len(providers))
	byID := make(map[string]OAuthProviderConfig, len(providers))
	for _, p := range providers {
		p.ID = strings.ToLower(strings.TrimSpace(p.ID))
		if p.ID == "" || !oauthProviderIDPattern.MatchString(p.ID) {
			continue
		}
		scopes := normalizeOAuthScopes(p.Scopes)
		p.Scopes = scopes
		copied = append(copied, p)
		byID[p.ID] = p
	}
	return &OAuthService{
		store:     store,
		client:    client,
		providers: copied,
		byID:      byID,
		now:       func() time.Time { return time.Now().UTC() },
	}
}

func normalizeOAuthScopes(raw []string) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, len(raw)+1)
	for _, s := range raw {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		if _, ok := seen[s]; ok {
			continue
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}
	if _, ok := seen["openid"]; !ok {
		out = append([]string{"openid"}, out...)
	}
	return out
}

func (s *OAuthService) ListProviders() []OAuthProviderSummary {
	out := make([]OAuthProviderSummary, 0, len(s.providers))
	for _, p := range s.providers {
		out = append(out, OAuthProviderSummary{ID: p.ID, DisplayName: p.DisplayName})
	}
	return out
}

func (s *OAuthService) StartAuthorize(ctx context.Context, providerID, rawNext string) (OAuthAuthorizeStart, error) {
	providerID = strings.ToLower(strings.TrimSpace(providerID))
	provider, ok := s.byID[providerID]
	if !ok {
		return OAuthAuthorizeStart{}, fault.New(fault.NotFound, "OAUTH_PROVIDER_NOT_FOUND", "未启用指定的单点登录提供方")
	}
	state, err := randomURLSafe(32)
	if err != nil {
		return OAuthAuthorizeStart{}, err
	}
	binding, err := randomURLSafe(32)
	if err != nil {
		return OAuthAuthorizeStart{}, err
	}
	verifier, err := randomURLSafe(48)
	if err != nil {
		return OAuthAuthorizeStart{}, err
	}
	challenge := pkceChallengeS256(verifier)
	nextPath := sanitizeOAuthNextPath(rawNext)

	req := OAuthAuthRequest{
		StateHash:    Digest(state),
		BindingHash:  Digest(binding),
		Provider:     provider.ID,
		CodeVerifier: verifier,
		RedirectURI:  provider.RedirectURI,
		NextPath:     nextPath,
		ExpiresAt:    s.now().Add(oauthAuthRequestTTL),
	}
	if err := s.store.InsertOAuthAuthRequest(ctx, req); err != nil {
		return OAuthAuthorizeStart{}, err
	}

	u, err := url.Parse(provider.AuthorizeURL)
	if err != nil {
		return OAuthAuthorizeStart{}, fault.New(fault.Unavailable, "OAUTH_PROVIDER_ERROR", "单点登录地址配置无效")
	}
	q := u.Query()
	q.Set("client_id", provider.ClientID)
	q.Set("redirect_uri", provider.RedirectURI)
	q.Set("response_type", "code")
	q.Set("scope", strings.Join(provider.Scopes, " "))
	q.Set("state", state)
	q.Set("code_challenge", challenge)
	q.Set("code_challenge_method", "S256")
	u.RawQuery = q.Encode()

	return OAuthAuthorizeStart{
		AuthorizationURL: u.String(),
		Binding:          binding,
		MaxAgeSeconds:    int(oauthAuthRequestTTL.Seconds()),
	}, nil
}

func (s *OAuthService) CompleteCallback(ctx context.Context, code, state, binding string, prior *Session) (OAuthCallbackResult, error) {
	code = strings.TrimSpace(code)
	state = strings.TrimSpace(state)
	binding = strings.TrimSpace(binding)
	if code == "" || state == "" || binding == "" {
		return OAuthCallbackResult{}, fault.New(fault.Invalid, "OAUTH_STATE_INVALID", "登录状态已失效或参数缺失，请重新发起登录")
	}

	authReq, err := s.store.ConsumeOAuthAuthRequest(ctx, Digest(state), Digest(binding))
	if errors.Is(err, ErrMissing) {
		return OAuthCallbackResult{}, fault.New(fault.Invalid, "OAUTH_STATE_INVALID", "登录状态校验失败或已过期，请重新登录")
	}
	if err != nil {
		return OAuthCallbackResult{}, err
	}

	provider, ok := s.byID[authReq.Provider]
	if !ok {
		return OAuthCallbackResult{}, fault.New(fault.Unavailable, "OAUTH_PROVIDER_NOT_FOUND", "单点登录提供方当前不可用")
	}

	tokenRes, err := s.client.ExchangeCode(ctx, provider, code, authReq.CodeVerifier, authReq.RedirectURI)
	if err != nil {
		return OAuthCallbackResult{}, err
	}
	if strings.TrimSpace(tokenRes.AccessToken) == "" {
		return OAuthCallbackResult{}, fault.New(fault.Unavailable, "OAUTH_PROVIDER_ERROR", "单点登录未返回有效访问令牌")
	}

	info, err := s.client.FetchUserInfo(ctx, provider, tokenRes.AccessToken)
	if err != nil {
		return OAuthCallbackResult{}, err
	}

	subject := strings.TrimSpace(info.Subject)
	if subject == "" {
		subject = strings.TrimSpace(tokenRes.Subject)
	}
	if subject == "" || len(subject) > 255 {
		return OAuthCallbackResult{}, fault.New(fault.Unavailable, "OAUTH_PROVIDER_ERROR", "单点登录未返回有效用户标识")
	}

	displayName := deriveOAuthDisplayName(info, provider.DisplayName)
	candidates := deriveOAuthUsernameCandidates(provider.ID, subject, info)

	secret := ids.Secret()
	replacement := Session{
		SecretHash: Digest(secret),
		CSRF:       ids.Secret(),
		ExpiresAt:  s.now().Add(oauthSessionTTL),
	}
	priorHash := ""
	if prior != nil {
		priorHash = prior.SecretHash
	}

	session, err := s.store.ResolveOAuthSession(
		ctx,
		provider.ID,
		subject,
		truncateUTF8(strings.TrimSpace(info.PreferredUsername), 200),
		truncateUTF8(strings.TrimSpace(info.Email), 255),
		displayName,
		candidates,
		priorHash,
		replacement,
	)
	if err != nil {
		return OAuthCallbackResult{}, err
	}

	return OAuthCallbackResult{
		Session:       session,
		SessionSecret: secret,
		NextPath:      sanitizeOAuthNextPath(authReq.NextPath),
	}, nil
}

func (s *OAuthService) IntrospectCheck(ctx context.Context, providerID string) error {
	providerID = strings.ToLower(strings.TrimSpace(providerID))
	provider, ok := s.byID[providerID]
	if !ok {
		return fault.New(fault.NotFound, "OAUTH_PROVIDER_NOT_FOUND", "未配置指定的单点登录提供方")
	}
	return s.client.IntrospectEmpty(ctx, provider)
}

func pkceChallengeS256(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

func randomURLSafe(numBytes int) (string, error) {
	b := make([]byte, numBytes)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

func sanitizeOAuthNextPath(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" || !strings.HasPrefix(raw, "/") || strings.HasPrefix(raw, "//") || strings.ContainsAny(raw, "\r\n\x00") || len(raw) > 512 {
		return "/projects"
	}
	return raw
}

func deriveOAuthDisplayName(info OAuthUserInfo, providerName string) string {
	for _, candidate := range []string{info.Name, info.PreferredUsername, info.Email} {
		clean := strings.TrimSpace(strings.ReplaceAll(candidate, "\x00", ""))
		if clean != "" {
			return truncateUTF8(clean, 200)
		}
	}
	if strings.TrimSpace(providerName) != "" {
		return truncateUTF8(strings.TrimSpace(providerName)+" 用户", 200)
	}
	return "SSO 用户"
}

func deriveOAuthUsernameCandidates(providerID, subject string, info OAuthUserInfo) []string {
	subHash := Digest(providerID + ":" + subject)
	fallback := providerID + "_" + subHash[:12]
	out := make([]string, 0, 5)
	seen := map[string]struct{}{}
	add := func(raw string) {
		clean := sanitizeUsernameToken(raw)
		if clean == "" {
			return
		}
		if _, ok := seen[clean]; ok {
			return
		}
		seen[clean] = struct{}{}
		out = append(out, clean)
	}
	add(info.PreferredUsername)
	add(info.Email)
	if base := sanitizeUsernameToken(info.PreferredUsername); base != "" {
		maxBase := 100 - 1 - 6
		if len(base) > maxBase {
			base = base[:maxBase]
		}
		add(base + "_" + subHash[:6])
	}
	add(fallback)
	add(providerID + "_" + subHash[:20])
	return out
}

func sanitizeUsernameToken(raw string) string {
	raw = strings.ToLower(strings.TrimSpace(raw))
	if raw == "" {
		return ""
	}
	var b strings.Builder
	b.Grow(len(raw))
	for i := 0; i < len(raw); i++ {
		c := raw[i]
		if (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '.' || c == '_' || c == '@' || c == '-' {
			b.WriteByte(c)
		}
	}
	s := b.String()
	if len(s) > 100 {
		s = s[:100]
	}
	if !usernamePattern.MatchString(s) {
		return ""
	}
	return s
}

func truncateUTF8(s string, maxBytes int) string {
	if len(s) <= maxBytes {
		return s
	}
	for len(s) > maxBytes {
		_, size := utf8.DecodeLastRuneInString(s)
		if size <= 0 {
			return s[:maxBytes]
		}
		s = s[:len(s)-size]
	}
	return s
}
