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
