package main

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/howtomakeaname/Evia/backend/internal/access"
	"github.com/howtomakeaname/Evia/backend/internal/platform/fault"
)

type accountProvisionStore struct {
	user access.User
	err  error
}

func (s *accountProvisionStore) ProvisionAccount(_ context.Context, user access.User) error {
	s.user = user
	return s.err
}

func (s *accountProvisionStore) SetAccountDisabled(_ context.Context, username string, disabled bool) (access.User, error) {
	if s.err != nil {
		return access.User{}, s.err
	}
	return access.User{ID: "00000000-0000-4000-8000-000000000009", Username: username, Disabled: disabled}, nil
}

func (s *accountProvisionStore) ResetAccountPassword(_ context.Context, username, _ string) (access.User, error) {
	if s.err != nil {
		return access.User{}, s.err
	}
	return access.User{ID: "00000000-0000-4000-8000-000000000009", Username: username}, nil
}

func TestAccountProvisionCLIOutputAndErrorsAreSafe(t *testing.T) {
	values := map[string]string{"EVIA_ACCOUNT_USERNAME": "synthetic-sensitive-username", "EVIA_ACCOUNT_DISPLAY_NAME": "synthetic-sensitive-display", "EVIA_ACCOUNT_PASSWORD": "synthetic-sensitive-password"}
	get := func(name string) string { return values[name] }
	var output bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&output, nil))
	store := &accountProvisionStore{}
	if err := accountCreate(context.Background(), store, get, logger); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), store.user.ID) || !strings.Contains(output.String(), "account_id") {
		t.Fatal("account ID missing from success output")
	}
	for _, value := range []string{values["EVIA_ACCOUNT_USERNAME"], values["EVIA_ACCOUNT_DISPLAY_NAME"], values["EVIA_ACCOUNT_PASSWORD"], store.user.PasswordHash} {
		if strings.Contains(output.String(), value) {
			t.Fatal("success output included private input or hash")
		}
	}
	for _, failure := range []error{
		errors.New("synthetic-sensitive-driver-and-url"),
		fault.New(fault.Conflict, "ACCOUNT_USERNAME_EXISTS", "username already exists; account and password were not changed"),
		fault.New(fault.Conflict, "ACCOUNT_PROVISION_REQUIRES_BOOTSTRAP", "an existing enabled account is required; initialize an empty installation with bootstrap"),
	} {
		store.err = failure
		output.Reset()
		err := accountCreate(context.Background(), store, get, logger)
		if err == nil || strings.Contains(err.Error(), "synthetic-sensitive") || output.Len() != 0 {
			t.Fatal("failure exposed private values or emitted a success", err)
		}
	}
	values["EVIA_ACCOUNT_PASSWORD"] = "short"
	if err := accountCreate(context.Background(), store, get, logger); err == nil || fault.Public(err).Code != "INVALID_PASSWORD" {
		t.Fatal("invalid credentials were not rejected", err)
	}
}

func TestAccountProvisionCLIRequiresOnlyDatabaseConfiguration(t *testing.T) {
	var output bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&output, nil))
	err := execute(context.Background(), []string{"account-create"}, func(string) string { return "" }, logger)
	if err == nil || err.Error() != "EVIA_DATABASE_URL is required" {
		t.Fatal("missing database config did not fail safely", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err = execute(ctx, []string{"account-create"}, func(key string) string {
		if key == "EVIA_DATABASE_URL" {
			return "postgres://synthetic-sensitive-url@127.0.0.1:1/synthetic"
		}
		if key == "EVIA_ACCOUNT_PASSWORD" {
			return "synthetic-sensitive-password"
		}
		return ""
	}, logger)
	if err == nil || !strings.Contains(err.Error(), "database connection") || strings.Contains(err.Error(), "synthetic-sensitive") || output.Len() != 0 {
		t.Fatal("administrative command required worker config or leaked connection data", err)
	}
	if err = execute(context.Background(), []string{"account-create", "synthetic-sensitive-password"}, func(string) string { return "" }, logger); err == nil || strings.Contains(err.Error(), "synthetic-sensitive") {
		t.Fatal("account command accepted positional credentials", err)
	}
}

func TestAccountDisableEnableCLISafeOutput(t *testing.T) {
	values := map[string]string{"EVIA_ACCOUNT_USERNAME": "synthetic-sensitive-username"}
	get := func(name string) string { return values[name] }
	store := &accountProvisionStore{}

	for _, tc := range []struct {
		name     string
		disabled bool
		run      func(context.Context, access.ProvisionStore, func(string) string, *slog.Logger) error
	}{
		{"disable", true, accountDisable},
		{"enable", false, accountEnable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var output bytes.Buffer
			logger := slog.New(slog.NewJSONHandler(&output, nil))
			if err := tc.run(context.Background(), store, get, logger); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(output.String(), "account_id") || !strings.Contains(output.String(), "00000000-0000-4000-8000-000000000009") {
				t.Fatal("success output must reference the account id")
			}
			if strings.Contains(output.String(), values["EVIA_ACCOUNT_USERNAME"]) {
				t.Fatal("operator output must not echo the username")
			}
		})
	}

	for _, tc := range []struct {
		name string
		err  error
		code string
	}{
		{"not found", fault.New(fault.NotFound, "ACCOUNT_NOT_FOUND", "account does not exist"), "ACCOUNT_NOT_FOUND"},
		{"last enabled", fault.New(fault.Conflict, "LAST_ENABLED_ACCOUNT", "cannot disable the last enabled account"), "LAST_ENABLED_ACCOUNT"},
		{"invalid", fault.New(fault.Invalid, "INVALID_USERNAME", "bad username"), "INVALID_USERNAME"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store.err = tc.err
			var output bytes.Buffer
			logger := slog.New(slog.NewJSONHandler(&output, nil))
			err := accountDisable(context.Background(), store, get, logger)
			if err == nil || !strings.Contains(err.Error(), tc.code) || output.Len() != 0 {
				t.Fatalf("expected safe %s error, got %v", tc.code, err)
			}
		})
	}
}

func TestAccountResetPasswordCLISafeOutput(t *testing.T) {
	values := map[string]string{
		"EVIA_ACCOUNT_USERNAME": "synthetic-sensitive-username",
		"EVIA_ACCOUNT_PASSWORD": "synthetic-sensitive-new-password",
	}
	get := func(name string) string { return values[name] }
	var output bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&output, nil))
	store := &accountProvisionStore{}
	if err := accountResetPassword(context.Background(), store, get, logger); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "account_id") || !strings.Contains(output.String(), "00000000-0000-4000-8000-000000000009") {
		t.Fatal("success output must reference the account id")
	}
	for _, secret := range []string{values["EVIA_ACCOUNT_USERNAME"], values["EVIA_ACCOUNT_PASSWORD"]} {
		if strings.Contains(output.String(), secret) {
			t.Fatal("operator output must not echo the username or new password")
		}
	}

	for _, tc := range []struct {
		name string
		err  error
		code string
	}{
		{"not found", fault.New(fault.NotFound, "ACCOUNT_NOT_FOUND", "account does not exist"), "ACCOUNT_NOT_FOUND"},
		{"invalid password", fault.New(fault.Invalid, "INVALID_PASSWORD", "bad password"), "INVALID_PASSWORD"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store.err = tc.err
			output.Reset()
			err := accountResetPassword(context.Background(), store, get, logger)
			if err == nil || !strings.Contains(err.Error(), tc.code) || output.Len() != 0 {
				t.Fatalf("expected safe %s error, got %v", tc.code, err)
			}
		})
	}
}
