//go:build integration

package postgres

import (
	"context"
	"errors"
	"fmt"
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

func TestExhaustedIPCannotAllocateAccountBuckets(t *testing.T) {
	s := database(t)
	ctx := context.Background()
	for i := 0; i < 60; i++ {
		allowed, e := s.ReserveLogin(ctx, fmt.Sprintf("initial-%d", i), "same-ip")
		if e != nil || !allowed {
			t.Fatal("initial IP reservation failed", i, allowed, e)
		}
	}
	var before int
	if e := s.Pool.QueryRow(ctx, `SELECT count(*) FROM login_buckets`).Scan(&before); e != nil {
		t.Fatal(e)
	}
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Go(func() {
			allowed, e := s.ReserveLogin(ctx, fmt.Sprintf("denied-%d", i), "same-ip")
			if e != nil || allowed {
				t.Error("exhausted IP admitted login", allowed, e)
			}
		})
	}
	wg.Wait()
	var after, ipAttempts int
	if e := s.Pool.QueryRow(ctx, `SELECT count(*) FROM login_buckets`).Scan(&after); e != nil {
		t.Fatal(e)
	}
	if e := s.Pool.QueryRow(ctx, `SELECT attempts FROM login_buckets WHERE bucket_key='ip:same-ip'`).Scan(&ipAttempts); e != nil {
		t.Fatal(e)
	}
	if after != before || ipAttempts != 160 {
		t.Fatalf("denied reservations must commit only to existing IP bucket: rows %d -> %d, attempts %d", before, after, ipAttempts)
	}
	allowed, e := s.ReserveLogin(ctx, "different-account", "different-ip")
	if e != nil || !allowed {
		t.Fatal("unrelated IP budget was affected", allowed, e)
	}
}

func TestSessionRotationAtomicityAndConcurrentConsumption(t *testing.T) {
	s := database(t)
	ctx := context.Background()
	if e := s.Bootstrap(ctx, "review", "Reviewer", "synthetic-password-42"); e != nil {
		t.Fatal(e)
	}
	u, e := s.FindUser(ctx, "review")
	if e != nil {
		t.Fatal(e)
	}
	session := func() access.Session {
		return access.Session{UserID: u.ID, SecretHash: access.Digest(ids.Secret()), CSRF: ids.Secret(), ExpiresAt: time.Now().Add(time.Hour)}
	}
	prior, replacement := session(), session()
	if e = s.InsertSession(ctx, prior); e != nil {
		t.Fatal(e)
	}
	// A failure during revocation cannot persist any replacement.
	if _, e = s.Pool.Exec(ctx, `CREATE FUNCTION deny_session_delete() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'synthetic revocation failure'; END $$;
	CREATE TRIGGER deny_session_delete BEFORE DELETE ON sessions FOR EACH ROW EXECUTE FUNCTION deny_session_delete()`); e != nil {
		t.Fatal(e)
	}
	if e = s.RotateSession(ctx, prior.SecretHash, replacement); e == nil {
		t.Fatal("revocation failure accepted")
	}
	if _, e = s.FindSession(ctx, prior.SecretHash); e != nil {
		t.Fatal("failed revocation lost prior session", e)
	}
	if _, e = s.FindSession(ctx, replacement.SecretHash); !errors.Is(e, access.ErrMissing) {
		t.Fatal("failed revocation persisted replacement", e)
	}
	if _, e = s.Pool.Exec(ctx, `DROP TRIGGER deny_session_delete ON sessions; DROP FUNCTION deny_session_delete()`); e != nil {
		t.Fatal(e)
	}
	// A failure after revocation must roll back the deleted prior credential.
	invalid := replacement
	invalid.UserID = ids.New()
	if e = s.RotateSession(ctx, prior.SecretHash, invalid); e == nil {
		t.Fatal("invalid replacement accepted")
	}
	if _, e = s.FindSession(ctx, prior.SecretHash); e != nil {
		t.Fatal("replacement failure lost prior session", e)
	}
	if _, e = s.FindSession(ctx, replacement.SecretHash); !errors.Is(e, access.ErrMissing) {
		t.Fatal("failed replacement left an orphan", e)
	}
	if e = s.RotateSession(ctx, prior.SecretHash, replacement); e != nil {
		t.Fatal(e)
	}
	if _, e = s.FindSession(ctx, prior.SecretHash); !errors.Is(e, access.ErrMissing) {
		t.Fatal("rotation retained old credential", e)
	}
	if _, e = s.FindSession(ctx, replacement.SecretHash); e != nil {
		t.Fatal("replacement missing", e)
	}
	replayed := session()
	if e = s.RotateSession(ctx, prior.SecretHash, replayed); !errors.Is(e, access.ErrMissing) {
		t.Fatal("consumed prior revived", e)
	}
	if _, e = s.FindSession(ctx, replayed.SecretHash); !errors.Is(e, access.ErrMissing) {
		t.Fatal("replayed rotation persisted credential", e)
	}
	expired := session()
	expired.ExpiresAt = time.Now().Add(-time.Minute)
	if e = s.InsertSession(ctx, expired); e != nil {
		t.Fatal(e)
	}
	if e = s.RotateSession(ctx, expired.SecretHash, replayed); !errors.Is(e, access.ErrMissing) {
		t.Fatal("expired prior revived", e)
	}

	prior = session()
	if e = s.InsertSession(ctx, prior); e != nil {
		t.Fatal(e)
	}
	var wg sync.WaitGroup
	var successes atomic.Int32
	hashes := make([]string, 8)
	for i := range hashes {
		next := session()
		hashes[i] = next.SecretHash
		wg.Go(func() {
			e := s.RotateSession(ctx, prior.SecretHash, next)
			if e == nil {
				successes.Add(1)
			} else if !errors.Is(e, access.ErrMissing) {
				t.Error(e)
			}
		})
	}
	wg.Wait()
	var persisted int
	if e = s.Pool.QueryRow(ctx, `SELECT count(*) FROM sessions WHERE secret_hash=ANY($1::text[])`, hashes).Scan(&persisted); e != nil {
		t.Fatal(e)
	}
	if successes.Load() != 1 || persisted != 1 {
		t.Fatal("concurrent rotation must consume prior once", successes.Load(), persisted)
	}
}

func TestCleanupDrainsExpiredBurstsWithoutRemovingLiveRows(t *testing.T) {
	s := database(t)
	ctx := context.Background()
	if _, e := s.Pool.Exec(ctx, `INSERT INTO login_buckets(bucket_key,window_start,attempts)
	SELECT 'expired-'||i,now()-interval '2 hours',1 FROM generate_series(1,2001) i;
	INSERT INTO login_buckets(bucket_key,window_start,attempts) VALUES('live',now(),1)`); e != nil {
		t.Fatal(e)
	}
	if e := s.Cleanup(ctx); e != nil {
		t.Fatal(e)
	}
	var count int
	if e := s.Pool.QueryRow(ctx, `SELECT count(*) FROM login_buckets`).Scan(&count); e != nil || count != 1 {
		t.Fatal("expired burst not drained or live row deleted", count, e)
	}
	if e := s.Pool.QueryRow(ctx, `SELECT count(*) FROM login_buckets WHERE bucket_key='live'`).Scan(&count); e != nil || count != 1 {
		t.Fatal("live budget deleted", count, e)
	}
}
