package postgres

import (
	"context"
	"errors"

	"github.com/howtomakeaname/Evia/backend/internal/access"
	"github.com/howtomakeaname/Evia/backend/internal/platform/fault"
	"github.com/jackc/pgx/v5/pgconn"
)

var _ access.ProvisionStore = (*Store)(nil)

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
	if _, err = tx.Exec(ctx, `INSERT INTO account_provision_events(actor_kind,target_id) VALUES('operator',$1)`, user.ID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
