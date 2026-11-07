// Package postgres implements durable adapters; migrations run only through the CLI.
package postgres

import (
	"context"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed migrations/*.sql
var migrations embed.FS

type Store struct{ Pool *pgxpool.Pool }

func (s *Store) CheckMigrations(ctx context.Context) error {
	entries, e := migrations.ReadDir("migrations")
	if e != nil {
		return e
	}
	var count int
	if e = s.Pool.QueryRow(ctx, `SELECT count(*) FROM schema_migrations`).Scan(&count); e != nil {
		return errors.New("database schema is unavailable; run evia migrate")
	}
	if count != len(entries) {
		return errors.New("database schema version differs; run matching migrations before serving")
	}
	for _, entry := range entries {
		data, e := migrations.ReadFile("migrations/" + entry.Name())
		if e != nil {
			return e
		}
		sum := sha256.Sum256(data)
		var checksum string
		if e = s.Pool.QueryRow(ctx, `SELECT checksum FROM schema_migrations WHERE name=$1`, entry.Name()).Scan(&checksum); e != nil || checksum != hex.EncodeToString(sum[:]) {
			return errors.New("database migration integrity check failed")
		}
	}
	return nil
}

func Open(ctx context.Context, dsn string) (*Store, error) {
	if dsn == "" {
		return nil, errors.New("EVIA_DATABASE_URL is required")
	}
	cfg, e := pgxpool.ParseConfig(dsn)
	if e != nil {
		return nil, errors.New("invalid database configuration")
	}
	cfg.MaxConns = 20
	cfg.MinConns = 2
	cfg.MaxConnLifetime = 30 * time.Minute
	cfg.MaxConnIdleTime = 5 * time.Minute
	cfg.ConnConfig.ConnectTimeout = 5 * time.Second
	cfg.ConnConfig.RuntimeParams["statement_timeout"] = "10000"
	cfg.ConnConfig.RuntimeParams["idle_in_transaction_session_timeout"] = "15000"
	cfg.ConnConfig.RuntimeParams["timezone"] = "UTC"
	pool, e := pgxpool.NewWithConfig(ctx, cfg)
	if e != nil {
		return nil, errors.New("database pool initialization failed")
	}
	if e = pool.Ping(ctx); e != nil {
		pool.Close()
		return nil, errors.New("database connection failed")
	}
	return &Store{pool}, nil
}
func (s *Store) Migrate(ctx context.Context) error {
	tx, e := s.Pool.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(context.Background())
	if _, e = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(7314201)`); e != nil {
		return e
	}
	if _, e = tx.Exec(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations(name text PRIMARY KEY, checksum text NOT NULL, applied_at timestamptz NOT NULL DEFAULT now())`); e != nil {
		return e
	}
	entries, e := migrations.ReadDir("migrations")
	if e != nil {
		return e
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	for _, entry := range entries {
		data, e := migrations.ReadFile("migrations/" + entry.Name())
		if e != nil {
			return e
		}
		sum := sha256.Sum256(data)
		checksum := hex.EncodeToString(sum[:])
		var existing string
		e = tx.QueryRow(ctx, `SELECT checksum FROM schema_migrations WHERE name=$1`, entry.Name()).Scan(&existing)
		if e == nil {
			if existing != checksum {
				return fmt.Errorf("migration checksum mismatch: %s", entry.Name())
			}
			continue
		}
		if !errors.Is(e, pgx.ErrNoRows) {
			return e
		}
		if _, e = tx.Exec(ctx, string(data)); e != nil {
			return fmt.Errorf("migration %s failed: %w", entry.Name(), e)
		}
		if _, e = tx.Exec(ctx, `INSERT INTO schema_migrations(name,checksum) VALUES($1,$2)`, entry.Name(), checksum); e != nil {
			return e
		}
	}
	return tx.Commit(ctx)
}

// Cleanup expires credentials and rate-limit buckets in bounded batches.
func (s *Store) Cleanup(ctx context.Context) error {
	for _, sql := range []string{
		`DELETE FROM sessions WHERE secret_hash IN (SELECT secret_hash FROM sessions WHERE expires_at<now() LIMIT 1000)`,
		`DELETE FROM login_buckets WHERE bucket_key IN (SELECT bucket_key FROM login_buckets WHERE window_start<now()-interval '1 hour' LIMIT 1000)`,
	} {
		if _, e := s.Pool.Exec(ctx, sql); e != nil {
			return e
		}
	}
	return nil
}
