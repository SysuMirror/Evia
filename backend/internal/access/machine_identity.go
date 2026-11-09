package access

import (
	"context"
	"time"
)

// Identity identifies the exact authenticated machine credential. The metadata
// supports discovery only: persistence rechecks current authority in its own transaction.
type Identity struct {
	PrincipalID, CredentialID, ProjectID, Environment, Name string
	Permissions                                             []string
	ExpiresAt                                               time.Time
}
type identityKey struct{}

func WithIdentity(ctx context.Context, identity Identity) context.Context {
	identity.Permissions = append([]string(nil), identity.Permissions...)
	return context.WithValue(ctx, identityKey{}, identity)
}
func IdentityFromContext(ctx context.Context) (Identity, bool) {
	identity, ok := ctx.Value(identityKey{}).(Identity)
	identity.Permissions = append([]string(nil), identity.Permissions...)
	return identity, ok
}
