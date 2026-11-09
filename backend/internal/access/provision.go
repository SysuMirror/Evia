package access

import (
	"context"
	"strings"

	"github.com/howtomakeaname/Evia/backend/internal/platform/fault"
	"github.com/howtomakeaname/Evia/backend/internal/platform/ids"
)

// ProvisionStore persists an operator-created account and its operator audit
// event atomically. It must reject an uninitialized installation and duplicates.
type ProvisionStore interface {
	ProvisionAccount(context.Context, User) error
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
