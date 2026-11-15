package access

import (
	"context"
	"strings"
	"time"

	"github.com/howtomakeaname/Evia/backend/internal/platform/fault"
	"github.com/howtomakeaname/Evia/backend/internal/platform/ids"
)

// AuditEvent is one typed operator action against an account. It carries only
// safe metadata: no actor identity beyond the fixed operator kind, no
// credential or request content. The closed EventType set mirrors the
// account_provision_events CHECK constraint.
type AuditEvent struct {
	EventType  string    `json:"event_type"`
	OccurredAt time.Time `json:"occurred_at"`
}

// ProvisionStore persists an operator-created account and its operator audit
// event atomically. It must reject an uninitialized installation and duplicates.
type ProvisionStore interface {
	ProvisionAccount(context.Context, User) error
	SetAccountDisabled(ctx context.Context, username string, disabled bool) (User, error)
	ResetAccountPassword(ctx context.Context, username, passwordHash string) (User, error)
	// AccountAuditEvents returns the account's typed operator history oldest
	// first. An unknown username fails with ACCOUNT_NOT_FOUND.
	AccountAuditEvents(ctx context.Context, username string) ([]AuditEvent, error)
}

// ProvisionAccount is an operator action, separate from bootstrap and project
// membership. No session or project authority is created by provisioning.
func ProvisionAccount(ctx context.Context, store ProvisionStore, username, displayName, password string) (User, error) {
	username = strings.ToLower(strings.TrimSpace(username))
	if !usernamePattern.MatchString(username) {
		return User{}, fault.New(fault.Invalid, "INVALID_USERNAME", "username must contain 1 to 100 ASCII letters, digits or ._@-")
	}
	if strings.TrimSpace(displayName) == "" || strings.ContainsRune(displayName, 0) || len(displayName) > 200 {
		return User{}, fault.New(fault.Invalid, "INVALID_DISPLAY_NAME", "display name must contain 1 to 200 bytes")
	}
	hash, err := PasswordHash(password)
	if err != nil {
		return User{}, err
	}
	user := User{ID: ids.New(), Username: username, DisplayName: displayName, PasswordHash: hash}
	if err = store.ProvisionAccount(ctx, user); err != nil {
		return User{}, err
	}
	// Password hashes belong only at the persistence boundary.
	user.PasswordHash = ""
	return user, nil
}

// SetAccountDisabled is an operator action that toggles an account's disabled
// flag. The persistence layer enforces the last-enabled-account guard and
// returns a not-found fault for an unknown username. Password hashes never
// cross back to the caller.
func SetAccountDisabled(ctx context.Context, store ProvisionStore, username string, disabled bool) (User, error) {
	username = strings.ToLower(strings.TrimSpace(username))
	if !usernamePattern.MatchString(username) {
		return User{}, fault.New(fault.Invalid, "INVALID_USERNAME", "username must contain 1 to 100 ASCII letters, digits or ._@-")
	}
	user, err := store.SetAccountDisabled(ctx, username, disabled)
	if err != nil {
		return User{}, err
	}
	user.PasswordHash = ""
	return user, nil
}

// ResetAccountPassword is an operator account-recovery action. It reuses the
// same bcrypt policy as provisioning and bootstrap. The persistence layer
// replaces the hash and revokes the account's existing sessions in one
// transaction, so a reset for a compromised account cannot leave an old session
// alive. Password hashes never cross back to the caller.
func ResetAccountPassword(ctx context.Context, store ProvisionStore, username, password string) (User, error) {
	username = strings.ToLower(strings.TrimSpace(username))
	if !usernamePattern.MatchString(username) {
		return User{}, fault.New(fault.Invalid, "INVALID_USERNAME", "username must contain 1 to 100 ASCII letters, digits or ._@-")
	}
	hash, err := PasswordHash(password)
	if err != nil {
		return User{}, err
	}
	user, err := store.ResetAccountPassword(ctx, username, hash)
	if err != nil {
		return User{}, err
	}
	user.PasswordHash = ""
	return user, nil
}

// AccountAuditEvents is the read side of the operator audit trail. It applies
// the same username normalization as the mutating commands and returns the
// account's typed events oldest first. It never returns credential material.
func AccountAuditEvents(ctx context.Context, store ProvisionStore, username string) ([]AuditEvent, error) {
	username = strings.ToLower(strings.TrimSpace(username))
	if !usernamePattern.MatchString(username) {
		return nil, fault.New(fault.Invalid, "INVALID_USERNAME", "username must contain 1 to 100 ASCII letters, digits or ._@-")
	}
	return store.AccountAuditEvents(ctx, username)
}
