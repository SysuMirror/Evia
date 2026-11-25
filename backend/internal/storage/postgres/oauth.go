package postgres

import (
	"context"
	"errors"

	"github.com/howtomakeaname/Evia/backend/internal/access"
	"github.com/howtomakeaname/Evia/backend/internal/platform/fault"
	"github.com/howtomakeaname/Evia/backend/internal/platform/ids"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

var _ access.OAuthStore = (*Store)(nil)

func (s *Store) InsertOAuthAuthRequest(ctx context.Context, req access.OAuthAuthRequest) error {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(context.Background())

	if _, err = tx.Exec(ctx, `DELETE FROM oauth_auth_requests WHERE expires_at <= now()`); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `
		INSERT INTO oauth_auth_requests (state_hash, binding_hash, provider, code_verifier, redirect_uri, next_path, expires_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
	`, req.StateHash, req.BindingHash, req.Provider, req.CodeVerifier, req.RedirectURI, req.NextPath, req.ExpiresAt); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *Store) ConsumeOAuthAuthRequest(ctx context.Context, stateHash, bindingHash string) (access.OAuthAuthRequest, error) {
	var out access.OAuthAuthRequest
	err := s.Pool.QueryRow(ctx, `
		DELETE FROM oauth_auth_requests
		WHERE state_hash = $1 AND binding_hash = $2 AND expires_at > now()
		RETURNING state_hash, binding_hash, provider, code_verifier, redirect_uri, next_path, expires_at
	`, stateHash, bindingHash).Scan(
		&out.StateHash,
		&out.BindingHash,
		&out.Provider,
		&out.CodeVerifier,
		&out.RedirectURI,
		&out.NextPath,
		&out.ExpiresAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return out, access.ErrMissing
	}
	return out, err
}

func (s *Store) ResolveOAuthSession(
	ctx context.Context,
	provider, subject, externalUsername, email, displayName string,
	usernameCandidates []string,
	priorSessionHash string,
	replacement access.Session,
) (access.Session, error) {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return access.Session{}, err
	}
	defer tx.Rollback(context.Background())

	var userID, accountDisplayName string
	var disabled bool

	err = tx.QueryRow(ctx, `
		SELECT a.id::text, a.display_name, a.disabled
		FROM oauth_identities oi
		JOIN accounts a ON a.id = oi.user_id
		WHERE oi.provider = $1 AND oi.subject = $2
	`, provider, subject).Scan(&userID, &accountDisplayName, &disabled)

	switch {
	case err == nil:
		if disabled {
			return access.Session{}, fault.New(fault.Forbidden, "ACCOUNT_DISABLED", "该账号已被停用，请联系平台管理员")
		}
		if _, err = tx.Exec(ctx, `
			UPDATE oauth_identities
			SET external_username = $3, email = $4, display_name = $5, last_login_at = now()
			WHERE provider = $1 AND subject = $2
		`, provider, subject, externalUsername, email, displayName); err != nil {
			return access.Session{}, err
		}
	case errors.Is(err, pgx.ErrNoRows):
		// Serialize concurrent first-time OAuth provisioning for the same external identity.
		if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1 || ':' || $2, 0))`, provider, subject); err != nil {
			return access.Session{}, err
		}
		err = tx.QueryRow(ctx, `
			SELECT a.id::text, a.display_name, a.disabled
			FROM oauth_identities oi
			JOIN accounts a ON a.id = oi.user_id
			WHERE oi.provider = $1 AND oi.subject = $2
		`, provider, subject).Scan(&userID, &accountDisplayName, &disabled)
		if err == nil {
			if disabled {
				return access.Session{}, fault.New(fault.Forbidden, "ACCOUNT_DISABLED", "该账号已被停用，请联系平台管理员")
			}
		} else if errors.Is(err, pgx.ErrNoRows) {
			createdID, createErr := insertOAuthAccount(ctx, tx, usernameCandidates, displayName)
			if createErr != nil {
				return access.Session{}, createErr
			}
			userID = createdID
			accountDisplayName = displayName
			if _, err = tx.Exec(ctx, `
				INSERT INTO oauth_identities (provider, subject, user_id, external_username, email, display_name)
				VALUES ($1, $2, $3, $4, $5, $6)
			`, provider, subject, userID, externalUsername, email, displayName); err != nil {
				return access.Session{}, err
			}
		} else {
			return access.Session{}, err
		}
	default:
		return access.Session{}, err
	}

	if priorSessionHash != "" {
		if _, err = tx.Exec(ctx, `DELETE FROM sessions WHERE secret_hash = $1`, priorSessionHash); err != nil {
			return access.Session{}, err
		}
	}
	replacement.UserID = userID
	replacement.DisplayName = accountDisplayName
	if _, err = tx.Exec(ctx, `
		INSERT INTO sessions (secret_hash, user_id, csrf_token, expires_at)
		VALUES ($1, $2, $3, $4)
	`, replacement.SecretHash, replacement.UserID, replacement.CSRF, replacement.ExpiresAt); err != nil {
		return access.Session{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return access.Session{}, err
	}
	return replacement, nil
}

func insertOAuthAccount(ctx context.Context, tx pgx.Tx, candidates []string, displayName string) (string, error) {
	for _, username := range candidates {
		if username == "" {
			continue
		}
		id := ids.New()
		if _, err := tx.Exec(ctx, `SAVEPOINT oauth_account_insert`); err != nil {
			return "", err
		}
		_, err := tx.Exec(ctx, `
			INSERT INTO accounts (id, username, display_name, password_hash)
			VALUES ($1, $2, $3, $4)
		`, id, username, displayName, access.OAuthPasswordSentinel)
		if err == nil {
			if _, relErr := tx.Exec(ctx, `RELEASE SAVEPOINT oauth_account_insert`); relErr != nil {
				return "", relErr
			}
			return id, nil
		}
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			if _, rbErr := tx.Exec(ctx, `ROLLBACK TO SAVEPOINT oauth_account_insert`); rbErr != nil {
				return "", rbErr
			}
			continue
		}
		return "", err
	}
	return "", fault.New(fault.Conflict, "ACCOUNT_PROVISION_CONFLICT", "无法为单点登录账号分配唯一用户名，请联系管理员")
}
