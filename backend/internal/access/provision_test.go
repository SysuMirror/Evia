package access

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/howtomakeaname/Evia/backend/internal/platform/fault"
	"golang.org/x/crypto/bcrypt"
)

type provisionCapture struct {
	user  User
	calls int
	err   error
}

func (s *provisionCapture) ProvisionAccount(_ context.Context, user User) error {
	s.user = user
	s.calls++
	return s.err
}

func TestAccountProvisionPolicyAndHashBoundary(t *testing.T) {
	store := &provisionCapture{}
	user, err := ProvisionAccount(context.Background(), store, "  Mixed.User@Example  ", "Synthetic account", "synthetic-password-42")
	if err != nil {
		t.Fatal(err)
	}
	if store.calls != 1 || store.user.Username != "mixed.user@example" || !projectIDPattern.MatchString(user.ID) || user.Disabled || user.PasswordHash != "" {
		t.Fatal("account policy or returned credential boundary failed")
	}
	if store.user.PasswordHash == "synthetic-password-42" || bcrypt.CompareHashAndPassword([]byte(store.user.PasswordHash), []byte("synthetic-password-42")) != nil {
		t.Fatal("persistence did not receive a password hash")
	}
	if cost, err := bcrypt.Cost([]byte(store.user.PasswordHash)); err != nil || cost != 12 {
		t.Fatal("password hashing policy changed", cost, err)
	}
	store.err = errors.New("synthetic persistence failure")
	user, err = ProvisionAccount(context.Background(), store, "other", "Other", "synthetic-password-42")
	if !errors.Is(err, store.err) || user != (User{}) {
		t.Fatal("failed persistence returned account data", err)
	}
}

func TestAccountProvisionRejectsInvalidInputBeforePersistence(t *testing.T) {
	for _, tc := range []struct{ name, username, displayName, password, code string }{
		{"empty username", " ", "Name", "synthetic-password-42", "INVALID_USERNAME"},
		{"long username", strings.Repeat("a", 101), "Name", "synthetic-password-42", "INVALID_USERNAME"},
		{"unicode username", "名字", "Name", "synthetic-password-42", "INVALID_USERNAME"},
		{"invalid username", "bad:name", "Name", "synthetic-password-42", "INVALID_USERNAME"},
		{"empty display name", "name", " \t", "synthetic-password-42", "INVALID_DISPLAY_NAME"},
		{"nul display name", "name", "bad\x00name", "synthetic-password-42", "INVALID_DISPLAY_NAME"},
		{"long display name", "name", strings.Repeat("a", 201), "synthetic-password-42", "INVALID_DISPLAY_NAME"},
		{"short password", "name", "Name", "short", "INVALID_PASSWORD"},
		{"long password", "name", "Name", strings.Repeat("x", 73), "INVALID_PASSWORD"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := &provisionCapture{}
			user, err := ProvisionAccount(context.Background(), store, tc.username, tc.displayName, tc.password)
			if err == nil || fault.Public(err).Code != tc.code || store.calls != 0 || user != (User{}) {
				t.Fatal("invalid input reached persistence or wrong error", err)
			}
		})
	}
}
