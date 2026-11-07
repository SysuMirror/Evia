// Evia runs the control API or explicitly applies administrative database actions.
package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
	_ "time/tzdata"

	"github.com/howtomakeaname/Evia/backend/internal/access"
	"github.com/howtomakeaname/Evia/backend/internal/platform/config"
	"github.com/howtomakeaname/Evia/backend/internal/platform/contract"
	"github.com/howtomakeaname/Evia/backend/internal/platform/cursor"
	"github.com/howtomakeaname/Evia/backend/internal/platform/httpapi"
	"github.com/howtomakeaname/Evia/backend/internal/storage/postgres"
	"github.com/howtomakeaname/Evia/backend/internal/transport/control"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	if e := run(logger); e != nil {
		logger.Error("process stopped", "reason", e.Error())
		os.Exit(1)
	}
}
func run(logger *slog.Logger) error {
	if len(os.Args) != 2 {
		return errors.New("usage: evia serve | migrate | bootstrap")
	}
	command := os.Args[1]
	if command != "serve" && command != "migrate" && command != "bootstrap" {
		return errors.New("unknown command")
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	startup, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	store, e := postgres.Open(startup, os.Getenv("EVIA_DATABASE_URL"))
	if e != nil {
		return e
	}
	defer store.Pool.Close()
	if command == "migrate" {
		if e = store.Migrate(startup); e != nil {
			return errors.New("migration failed; inspect database version and privileges")
		}
		logger.Info("database migrations applied")
		return nil
	}
	if e = store.CheckMigrations(startup); e != nil {
		return e
	}
	if command == "bootstrap" {
		e = store.Bootstrap(startup, os.Getenv("EVIA_BOOTSTRAP_USERNAME"), os.Getenv("EVIA_BOOTSTRAP_DISPLAY_NAME"), os.Getenv("EVIA_BOOTSTRAP_PASSWORD"))
		if e != nil {
			return errors.New("bootstrap failed; verify input constraints and that no account exists")
		}
		logger.Info("initial account created")
		return nil
	}
	cfg, e := config.Read(os.Getenv)
	if e != nil {
		return e
	}
	validator, e := contract.New()
	if e != nil {
		return errors.New("contract bundle could not be compiled")
	}
	codec, e := cursor.New(cfg.CursorKey)
	if e != nil {
		return e
	}
	accounts, e := access.New(store)
	if e != nil {
		return e
	}
	boundary := &httpapi.Boundary{Validator: validator, Logger: logger}
	api := (&control.API{Boundary: boundary, Access: accounts, Cursor: codec, Origin: cfg.Origin, SecureCookies: cfg.SecureCookies}).Handler()
	mux := http.NewServeMux()
	mux.Handle("/", api)
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNoContent) })
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, r *http.Request) {
		probe, cancel := context.WithTimeout(r.Context(), time.Second)
		defer cancel()
		if store.Pool.Ping(probe) != nil {
			w.WriteHeader(503)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	server := httpapi.Server(cfg.Listen, mux)
	results := make(chan error, 1)
	go func() { results <- server.ListenAndServe() }()
	ticker := time.NewTicker(10 * time.Minute)
	defer ticker.Stop()
	logger.Info("control API started", "listen", cfg.Listen)
	for {
		select {
		case <-ticker.C:
			clean, cancel := context.WithTimeout(ctx, 10*time.Second)
			e := store.Cleanup(clean)
			cancel()
			if e != nil {
				logger.Warn("expired credential cleanup failed")
			}
		case e := <-results:
			if errors.Is(e, http.ErrServerClosed) {
				return nil
			}
			return errors.New("HTTP listener failed")
		case <-ctx.Done():
			shutdown, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			return server.Shutdown(shutdown)
		}
	}
}
