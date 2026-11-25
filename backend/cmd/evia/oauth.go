package main

import (
	"context"
	"errors"
	"log/slog"

	"github.com/howtomakeaname/Evia/backend/internal/access"
	"github.com/howtomakeaname/Evia/backend/internal/platform/config"
	"github.com/howtomakeaname/Evia/backend/internal/platform/oauth2client"
)

func toAccessOAuthProviders(configured []config.OAuthProvider) []access.OAuthProviderConfig {
	out := make([]access.OAuthProviderConfig, 0, len(configured))
	for _, p := range configured {
		out = append(out, access.OAuthProviderConfig{
			ID:            p.ID,
			DisplayName:   p.DisplayName,
			ClientID:      p.ClientID,
			ClientSecret:  p.ClientSecret,
			RedirectURI:   p.RedirectURI,
			AuthorizeURL:  p.AuthorizeURL,
			TokenURL:      p.TokenURL,
			UserInfoURL:   p.UserInfoURL,
			IntrospectURL: p.IntrospectURL,
			Scopes:        append([]string(nil), p.Scopes...),
		})
	}
	return out
}

func oauthCheck(ctx context.Context, get func(string) string, logger *slog.Logger) error {
	configured, err := config.ReadOAuth(get)
	if err != nil {
		return stageError("oauth configuration", err)
	}
	if len(configured) == 0 {
		return stageError("oauth configuration", errors.New("no oauth2 providers configured"))
	}
	client := oauth2client.New(nil)
	for _, provider := range toAccessOAuthProviders(configured) {
		if err := client.IntrospectEmpty(ctx, provider); err != nil {
			return stageError("oauth introspect check ("+provider.ID+")", err)
		}
		logger.Info("oauth provider introspect check passed", "provider", provider.ID)
	}
	return nil
}
