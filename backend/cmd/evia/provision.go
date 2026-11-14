package main

import (
	"context"
	"errors"
	"log/slog"

	"github.com/howtomakeaname/Evia/backend/internal/access"
	"github.com/howtomakeaname/Evia/backend/internal/platform/fault"
)

func accountCreate(ctx context.Context, store access.ProvisionStore, get func(string) string, logger *slog.Logger) error {
	user, err := access.ProvisionAccount(ctx, store, get("EVIA_ACCOUNT_USERNAME"), get("EVIA_ACCOUNT_DISPLAY_NAME"), get("EVIA_ACCOUNT_PASSWORD"))
	if err != nil {
		var known *fault.Error
		if errors.As(err, &known) && (known.Kind == fault.Invalid || known.Kind == fault.Conflict) {
			// Only provisioning's fixed domain messages are safe for CLI output.
			switch known.Code {
			case "INVALID_USERNAME", "INVALID_DISPLAY_NAME", "INVALID_PASSWORD", "ACCOUNT_PROVISION_REQUIRES_BOOTSTRAP", "ACCOUNT_USERNAME_EXISTS":
				return fault.New(known.Kind, known.Code, known.Message)
			}
		}
		return stageError("account provisioning", err)
	}
	logger.Info("account created", "account_id", user.ID)
	return nil
}

// accountSetDisabled maps the bounded operator fault codes to safe CLI output,
// mirroring accountCreate so driver details and credentials never leak.
func accountSetDisabled(ctx context.Context, store access.ProvisionStore, get func(string) string, disabled bool, logger *slog.Logger) error {
	user, err := access.SetAccountDisabled(ctx, store, get("EVIA_ACCOUNT_USERNAME"), disabled)
	if err != nil {
		var known *fault.Error
		if errors.As(err, &known) && (known.Kind == fault.Invalid || known.Kind == fault.NotFound || known.Kind == fault.Conflict) {
			switch known.Code {
			case "INVALID_USERNAME", "ACCOUNT_NOT_FOUND", "LAST_ENABLED_ACCOUNT":
				return fault.New(known.Kind, known.Code, known.Message)
			}
		}
		return stageError("account administration", err)
	}
	logger.Info("account disabled state changed", "account_id", user.ID, "disabled", user.Disabled)
	return nil
}

func accountDisable(ctx context.Context, store access.ProvisionStore, get func(string) string, logger *slog.Logger) error {
	return accountSetDisabled(ctx, store, get, true, logger)
}

func accountEnable(ctx context.Context, store access.ProvisionStore, get func(string) string, logger *slog.Logger) error {
	return accountSetDisabled(ctx, store, get, false, logger)
}
