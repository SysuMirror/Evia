package postgres

import (
	"context"
	"errors"

	"github.com/howtomakeaname/Evia/backend/internal/access"
	"github.com/howtomakeaname/Evia/backend/internal/platform/fault"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

var _ access.ProvisionStore = (*Store)(nil)

// Closed set of account provision audit events; it mirrors the CHECK constraint
// added by migration 028. Every operator-side account mutation must record one.
const (
	provisionEventCreated  = "account_created"
	provisionEventDisabled = "account_disabled"
	provisionEventEnabled  = "account_enabled"
	provisionEventPassword = "password_reset"
)

func (s *Store) ProvisionAccount(ctx context.Context, user access.User) error {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(context.Background())
	// Share bootstrap's installation lock so the first account can only be
	// created by bootstrap, including when both commands run concurrently.
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(7314202)`); err != nil {
		return err
	}
	var initialized bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM accounts WHERE NOT disabled)`).Scan(&initialized); err != nil {
		return err
	}
	if !initialized {
		return fault.New(fault.Conflict, "ACCOUNT_PROVISION_REQUIRES_BOOTSTRAP", "an existing enabled account is required; initialize an empty installation with bootstrap")
	}
	if _, err = tx.Exec(ctx, `INSERT INTO accounts(id,username,display_name,password_hash) VALUES($1,$2,$3,$4)`, user.ID, user.Username, user.DisplayName, user.PasswordHash); err != nil {
		var pgerr *pgconn.PgError
		if errors.As(err, &pgerr) && pgerr.Code == "23505" && pgerr.ConstraintName == "accounts_username_key" {
			return fault.New(fault.Conflict, "ACCOUNT_USERNAME_EXISTS", "username already exists; account and password were not changed")
		}
		return err
	}
	// The database operator is deliberately not attributed to an in-app human.
	if _, err = tx.Exec(ctx, `INSERT INTO account_provision_events(actor_kind,target_id,event_type) VALUES('operator',$1,$2)`, user.ID, provisionEventCreated); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// SetAccountDisabled toggles the accounts.disabled flag under the same
// installation advisory lock as provisioning. Disabling the last enabled
// account is rejected because it would lock every operator out and leave no
// account able to provision or bootstrap further access. Existing sessions are
// rejected automatically because every authenticated read filters NOT disabled.
func (s *Store) SetAccountDisabled(ctx context.Context, username string, disabled bool) (access.User, error) {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return access.User{}, err
	}
	defer tx.Rollback(context.Background())
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(7314202)`); err != nil {
		return access.User{}, err
	}
	var user access.User
	err = tx.QueryRow(ctx, `SELECT id::text,username,display_name,password_hash,disabled FROM accounts WHERE username=$1`, username).
		Scan(&user.ID, &user.Username, &user.DisplayName, &user.PasswordHash, &user.Disabled)
	if errors.Is(err, pgx.ErrNoRows) {
		return access.User{}, fault.New(fault.NotFound, "ACCOUNT_NOT_FOUND", "account does not exist")
	}
	if err != nil {
		return access.User{}, err
	}
	// Only guard an actual transition: re-disabling an already disabled account
	// must not trip the last-enabled protection.
	if disabled && !user.Disabled {
		var enabled int
		if err = tx.QueryRow(ctx, `SELECT count(*) FROM accounts WHERE NOT disabled`).Scan(&enabled); err != nil {
			return access.User{}, err
		}
		if enabled <= 1 {
			return access.User{}, fault.New(fault.Conflict, "LAST_ENABLED_ACCOUNT", "cannot disable the last enabled account")
		}
	}
	if _, err = tx.Exec(ctx, `UPDATE accounts SET disabled=$1 WHERE id=$2`, disabled, user.ID); err != nil {
		return access.User{}, err
	}
	// Record only real state transitions so an idempotent re-disable or
	// re-enable that changes nothing does not add misleading audit rows.
	if user.Disabled != disabled {
		eventType := provisionEventEnabled
		if disabled {
			eventType = provisionEventDisabled
		}
		if _, err = tx.Exec(ctx, `INSERT INTO account_provision_events(actor_kind,target_id,event_type) VALUES('operator',$1,$2)`, user.ID, eventType); err != nil {
			return access.User{}, err
		}
	}
	user.Disabled = disabled
	return user, tx.Commit(ctx)
}

// ResetAccountPassword replaces one account's password hash and revokes all of
// its sessions in the same transaction under the installation advisory lock.
// Revoking sessions is required for account recovery: an operator resetting a
// compromised credential must not leave a previously issued session valid.
func (s *Store) ResetAccountPassword(ctx context.Context, username, passwordHash string) (access.User, error) {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return access.User{}, err
	}
	defer tx.Rollback(context.Background())
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(7314202)`); err != nil {
		return access.User{}, err
	}
	var user access.User
	err = tx.QueryRow(ctx, `SELECT id::text,username,display_name,password_hash,disabled FROM accounts WHERE username=$1`, username).
		Scan(&user.ID, &user.Username, &user.DisplayName, &user.PasswordHash, &user.Disabled)
	if errors.Is(err, pgx.ErrNoRows) {
		return access.User{}, fault.New(fault.NotFound, "ACCOUNT_NOT_FOUND", "account does not exist")
	}
	if err != nil {
		return access.User{}, err
	}
	if _, err = tx.Exec(ctx, `UPDATE accounts SET password_hash=$1 WHERE id=$2`, passwordHash, user.ID); err != nil {
		return access.User{}, err
	}
	if _, err = tx.Exec(ctx, `DELETE FROM sessions WHERE user_id=$1`, user.ID); err != nil {
		return access.User{}, err
	}
	// Every reset replaces the credential, so it is always a real mutation and
	// always leaves an audit row in the same transaction as the hash and session
	// revocation.
	if _, err = tx.Exec(ctx, `INSERT INTO account_provision_events(actor_kind,target_id,event_type) VALUES('operator',$1,$2)`, user.ID, provisionEventPassword); err != nil {
		return access.User{}, err
	}
	user.PasswordHash = passwordHash
	return user, tx.Commit(ctx)
}
