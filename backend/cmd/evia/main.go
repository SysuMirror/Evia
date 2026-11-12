// Evia runs explicit control, ingestion and query processes or administrative actions.
package main

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"
	_ "time/tzdata"

	"github.com/howtomakeaname/Evia/backend/internal/platform/config"
	"github.com/howtomakeaname/Evia/backend/internal/storage/postgres"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	if err := execute(ctx, os.Args[1:], os.Getenv, logger); err != nil {
		// Every external failure is stage-wrapped; never print driver errors/URLs.
		logger.Error("process stopped", "reason", err.Error())
		os.Exit(1)
	}
}

func execute(ctx context.Context, args []string, get func(string) string, logger *slog.Logger) error {
	if len(args) != 1 {
		return errors.New("usage: evia serve | ingest-worker | query-worker | erasure-worker | retention-worker | export-worker | backfill-worker | migrate | bootstrap | account-create")
	}
	command := args[0]
	if command != "serve" && command != "ingest-worker" && command != "query-worker" && command != "erasure-worker" && command != "retention-worker" && command != "export-worker" && command != "backfill-worker" && command != "migrate" && command != "bootstrap" && command != "account-create" {
		return errors.New("unknown command")
	}
	var cfg config.Config
	var err error
	if command == "migrate" || command == "bootstrap" || command == "account-create" {
		cfg.DatabaseURL = get("EVIA_DATABASE_URL")
		if cfg.DatabaseURL == "" {
			return errors.New("EVIA_DATABASE_URL is required")
		}
	} else {
		cfg, err = config.ReadFor(command, get)
		if err != nil {
			return err
		}
	}
	startup, cancel := context.WithTimeout(ctx, 30*time.Second)
	store, err := postgres.Open(startup, cfg.DatabaseURL)
	if err != nil {
		cancel()
		return stageError("database connection", err)
	}
	defer store.Pool.Close()
	if command == "migrate" {
		defer cancel()
		if err := store.Migrate(startup); err != nil {
			return stageError("database migration", err)
		}
		logger.Info("database migrations applied")
		return nil
	}
	if err := store.CheckMigrations(startup); err != nil {
		cancel()
		return stageError("database migration check", err)
	}
	if command == "bootstrap" {
		defer cancel()
		if err := store.Bootstrap(startup, get("EVIA_BOOTSTRAP_USERNAME"), get("EVIA_BOOTSTRAP_DISPLAY_NAME"), get("EVIA_BOOTSTRAP_PASSWORD")); err != nil {
			return stageError("account bootstrap", err)
		}
		logger.Info("initial account created")
		return nil
	}
	if command == "account-create" {
		defer cancel()
		return accountCreate(startup, store, get, logger)
	}
	cancel()
	switch command {
	case "serve":
		return serve(ctx, cfg, store, logger)
	case "ingest-worker":
		return ingestWorker(ctx, cfg, store, logger)
	case "query-worker":
		return queryWorkers(ctx, cfg, store, logger)
	case "erasure-worker":
		return erasureWorkerRuntime(ctx, cfg, store, logger)
	case "retention-worker":
		return retentionWorkerRuntime(ctx, cfg, store, logger)
	case "export-worker":
		return exportWorkerRuntime(ctx, cfg, store, logger)
	case "backfill-worker":
		return backfillWorkerRuntime(ctx, cfg, store, logger)
	}
	return errors.New("unknown command")
}
