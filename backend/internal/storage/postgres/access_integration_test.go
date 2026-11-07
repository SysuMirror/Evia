//go:build integration

package postgres

import (
	"context"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/howtomakeaname/Evia/backend/internal/access"
	"github.com/howtomakeaname/Evia/backend/internal/platform/ids"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func database(t *testing.T) *Store {
	t.Helper()
	dsn := os.Getenv("EVIA_TEST_DATABASE_URL")
	if dsn == "" {
		t.Fatal("EVIA_TEST_DATABASE_URL is required for integration tests")
	}
	ctx := context.Background()
	admin, e := Open(ctx, dsn)
	if e != nil {
		t.Fatal(e)
	}
	schema := "evia_test_" + strings.ReplaceAll(ids.New(), "-", "")
	if _, e = admin.Pool.Exec(ctx, "CREATE SCHEMA "+pgx.Identifier{schema}.Sanitize()); e != nil {
		t.Fatal(e)
	}
	cfg, e := pgxpool.ParseConfig(dsn)
	if e != nil {
		t.Fatal(e)
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = schema
	pool, e := pgxpool.NewWithConfig(ctx, cfg)
	if e != nil {
		t.Fatal(e)
	}
	s := &Store{Pool: pool}
	t.Cleanup(func() {
		pool.Close()
		_, e := admin.Pool.Exec(context.Background(), "DROP SCHEMA "+pgx.Identifier{schema}.Sanitize()+" CASCADE")
		admin.Pool.Close()
		if e != nil {
			t.Error(e)
		}
	})
	if e = s.Migrate(ctx); e != nil {
		t.Fatal(e)
	}
	return s
}
func TestAccessTransactionsAndIsolation(t *testing.T) {
	s := database(t)
	ctx := context.Background()
	if e := s.Migrate(ctx); e != nil {
		t.Fatal("migration not idempotent", e)
	}
	if e := s.Bootstrap(ctx, "Admin", "管理员", "synthetic-password-42"); e != nil {
		t.Fatal(e)
	}
	if e := s.Bootstrap(ctx, "other", "Other", "synthetic-password-42"); e == nil {
		t.Fatal("bootstrap overwrote existing setup")
	}
	owner, e := s.FindUser(ctx, "admin")
	if e != nil {
		t.Fatal(e)
	}
	outsider := ids.New()
	if _, e = s.Pool.Exec(ctx, `INSERT INTO accounts(id,username,display_name,password_hash) VALUES($1,'outsider','Other',$2)`, outsider, owner.PasswordHash); e != nil {
		t.Fatal(e)
	}
	p := access.Project{ID: ids.New(), Name: "Example", Timezone: "UTC"}
	if e = s.CreateProject(ctx, ids.New(), p); e == nil {
		t.Fatal("missing actor accepted")
	}
	var count int
	if e = s.Pool.QueryRow(ctx, `SELECT count(*) FROM projects`).Scan(&count); e != nil || count != 0 {
		t.Fatal("failed project was not rolled back", count, e)
	}
	if e = s.CreateProject(ctx, owner.ID, p); e != nil {
		t.Fatal(e)
	}
	if role, e := s.ProjectRole(ctx, owner.ID, p.ID); e != nil || role != "owner" {
		t.Fatal(role, e)
	}
	if _, e = s.ProjectRole(ctx, outsider, p.ID); e != access.ErrMissing {
		t.Fatal("project authority leaked", e)
	}
	if list, e := s.ListProjects(ctx, outsider, "", 10); e != nil || len(list) != 0 {
		t.Fatal("project list leaked", list, e)
	}
	if list, e := s.ListProjects(ctx, owner.ID, "", 10); e != nil || len(list) != 1 {
		t.Fatal(list, e)
	}
	if list, e := s.ListProjects(ctx, owner.ID, p.ID, 10); e != nil || len(list) != 0 {
		t.Fatal("cursor replayed prior row", list, e)
	}
	if e = s.Pool.QueryRow(ctx, `SELECT count(*) FROM audit_entries WHERE project_id=$1`, p.ID).Scan(&count); e != nil || count != 1 {
		t.Fatal("missing audit", count, e)
	}
	secret := ids.Secret()
	session := access.Session{UserID: owner.ID, SecretHash: access.Digest(secret), CSRF: ids.Secret(), ExpiresAt: time.Now().Add(time.Hour)}
	if e = s.InsertSession(ctx, session); e != nil {
		t.Fatal(e)
	}
	if _, e = s.FindSession(ctx, secret); e != access.ErrMissing {
		t.Fatal("stored raw credential")
	}
	if _, e = s.FindSession(ctx, session.SecretHash); e != nil {
		t.Fatal(e)
	}
	if _, e = s.Pool.Exec(ctx, `UPDATE accounts SET disabled=true WHERE id=$1`, owner.ID); e != nil {
		t.Fatal(e)
	}
	if _, e = s.FindSession(ctx, session.SecretHash); e != access.ErrMissing {
		t.Fatal("disabled account retained access")
	}
	if e = s.DeleteSession(ctx, session.SecretHash); e != nil {
		t.Fatal(e)
	}
	if e = s.Cleanup(ctx); e != nil {
		t.Fatal(e)
	}
}
func TestConcurrentLoginBudget(t *testing.T) {
	s := database(t)
	var allowed atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Go(func() {
			ok, e := s.ReserveLogin(context.Background(), "account", "address")
			if e != nil {
				t.Error(e)
			}
			if ok {
				allowed.Add(1)
			}
		})
	}
	wg.Wait()
	if allowed.Load() != 10 {
		t.Fatalf("expected 10 reservations, got %d", allowed.Load())
	}
}
