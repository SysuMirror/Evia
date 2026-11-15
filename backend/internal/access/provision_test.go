package access

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/howtomakeaname/Evia/backend/internal/platform/fault"
	"golang.org/x/crypto/bcrypt"
)

type provisionCapture struct {
	user          User
	calls         int
	err           error
	disabledName  string
	disabledFlag  bool
	disabledCalls int
	auditName     string
	auditEvents   []AuditEvent
	auditCalls    int
}

func (s *provisionCapture) ProvisionAccount(_ context.Context, user User) error {
	s.user = user
	s.calls++
	return s.err
}

func (s *provisionCapture) SetAccountDisabled(_ context.Context, username string, disabled bool) (User, error) {
	s.disabledName = username
	s.disabledFlag = disabled
	s.disabledCalls++
	if s.err != nil {
		return User{}, s.err
	}
	return User{ID: "00000000-0000-4000-8000-000000000009", Username: username, Disabled: disabled, PasswordHash: "secret-hash"}, nil
}

func (s *provisionCapture) ResetAccountPassword(_ context.Context, username, passwordHash string) (User, error) {
	s.disabledName = username
	if s.err != nil {
		return User{}, s.err
	}
	return User{ID: "00000000-0000-4000-8000-000000000009", Username: username, PasswordHash: passwordHash}, nil
}

func (s *provisionCapture) AccountAuditEvents(_ context.Context, username string) ([]AuditEvent, error) {
	s.auditName = username
	s.auditCalls++
	if s.err != nil {
		return nil, s.err
	}
	return s.auditEvents, nil
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

func TestSetAccountDisabledValidatesAndStripsHash(t *testing.T) {
	store := &provisionCapture{}
	user, err := SetAccountDisabled(context.Background(), store, "  Operator@Example ", true)
	if err != nil {
		t.Fatal(err)
	}
	if store.disabledCalls != 1 || store.disabledName != "operator@example" || !store.disabledFlag {
		t.Fatalf("persistence did not receive normalized toggle: %+v", store)
	}
	if !user.Disabled || user.PasswordHash != "" {
		t.Fatal("returned user must reflect state and never expose the password hash")
	}

	enabled, err := SetAccountDisabled(context.Background(), store, "operator@example", false)
	if err != nil || enabled.Disabled {
		t.Fatalf("enable toggle failed: %+v %v", enabled, err)
	}
}

func TestSetAccountDisabledRejectsInvalidUsernameBeforePersistence(t *testing.T) {
	for _, username := range []string{" ", "bad:name", strings.Repeat("a", 101), "名字"} {
		store := &provisionCapture{}
		if _, err := SetAccountDisabled(context.Background(), store, username, true); err == nil ||
			fault.Public(err).Code != "INVALID_USERNAME" || store.disabledCalls != 0 {
			t.Fatalf("username %q must fail validation before persistence: %v", username, err)
		}
	}
}

func TestSetAccountDisabledPropagatesPersistenceFault(t *testing.T) {
	store := &provisionCapture{err: fault.New(fault.Conflict, "LAST_ENABLED_ACCOUNT", "cannot disable the last enabled account")}
	_, err := SetAccountDisabled(context.Background(), store, "operator", true)
	if err == nil || fault.Public(err).Code != "LAST_ENABLED_ACCOUNT" {
		t.Fatalf("persistence fault must surface unchanged: %v", err)
	}
}

func TestResetAccountPasswordValidatesAndStripsHash(t *testing.T) {
	store := &provisionCapture{}
	user, err := ResetAccountPassword(context.Background(), store, "  Recovery@Example ", "synthetic-new-password-42")
	if err != nil {
		t.Fatal(err)
	}
	if store.disabledName != "recovery@example" {
		t.Fatalf("persistence did not receive normalized username: %q", store.disabledName)
	}
	if user.PasswordHash != "" {
		t.Fatal("returned user must never expose the password hash")
	}
	store.err = errors.New("synthetic driver failure")
	if _, err := ResetAccountPassword(context.Background(), store, "recovery@example", "synthetic-new-password-42"); err == nil {
		t.Fatal("persistence failure must surface")
	}
}

func TestResetAccountPasswordRejectsBadInputBeforePersistence(t *testing.T) {
	store := &provisionCapture{}
	if _, err := ResetAccountPassword(context.Background(), store, "bad:name", "synthetic-new-password-42"); err == nil ||
		fault.Public(err).Code != "INVALID_USERNAME" || store.disabledCalls != 0 {
		t.Fatal("invalid username must fail before persistence")
	}
	for _, password := range []string{"short", strings.Repeat("x", 73)} {
		if _, err := ResetAccountPassword(context.Background(), store, "recovery", password); err == nil ||
			fault.Public(err).Code != "INVALID_PASSWORD" {
			t.Fatalf("password %q must be rejected: %v", password, err)
		}
	}
}

func TestAccountAuditEventsNormalizesAndDelegates(t *testing.T) {
	stamp := time.Date(2026, 11, 15, 8, 0, 0, 0, time.UTC)
	store := &provisionCapture{auditEvents: []AuditEvent{
		{EventType: "account_created", OccurredAt: stamp},
		{EventType: "account_disabled", OccurredAt: stamp.Add(time.Minute)},
	}}
	events, err := AccountAuditEvents(context.Background(), store, "  Audit.User@Example ")
	if err != nil {
		t.Fatal(err)
	}
	if store.auditName != "audit.user@example" || store.auditCalls != 1 {
		t.Fatalf("normalized username not delegated: %q calls=%d", store.auditName, store.auditCalls)
	}
	if len(events) != 2 || events[0].EventType != "account_created" || events[1].EventType != "account_disabled" {
		t.Fatalf("audit trail altered: %+v", events)
	}
}

func TestAccountAuditEventsRejectsInvalidUsernameBeforePersistence(t *testing.T) {
	store := &provisionCapture{}
	if _, err := AccountAuditEvents(context.Background(), store, "bad name"); err == nil ||
		fault.Public(err).Code != "INVALID_USERNAME" {
		t.Fatalf("invalid username must fail closed: %v", err)
	}
	if store.auditCalls != 0 {
		t.Fatal("invalid username reached persistence")
	}
}

func TestAccountAuditEventsPropagatesPersistenceFault(t *testing.T) {
	store := &provisionCapture{err: fault.New(fault.NotFound, "ACCOUNT_NOT_FOUND", "account does not exist")}
	_, err := AccountAuditEvents(context.Background(), store, "ghost")
	if err == nil || fault.Public(err).Code != "ACCOUNT_NOT_FOUND" {
		t.Fatalf("not-found fault must propagate: %v", err)
	}
}
