package postgres

import (
	"context"
	"errors"
	"github.com/howtomakeaname/Evia/backend/internal/access"
	"github.com/howtomakeaname/Evia/backend/internal/platform/fault"
	"github.com/howtomakeaname/Evia/backend/internal/platform/ids"
	"github.com/jackc/pgx/v5"
	"strings"
)

var _ access.Store = (*Store)(nil)

func (s *Store) Bootstrap(ctx context.Context, username, displayName, password string) error {
	username = strings.ToLower(strings.TrimSpace(username))
	// CLI input has the same constraints as API credentials.
	if len(username) < 1 || len(username) > 100 {
		return errors.New("username must contain 1 to 100 ASCII letters, digits or ._@-")
	}
	for _, c := range username {
		if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || strings.ContainsRune("._@-", c)) {
			return errors.New("invalid username")
		}
	}
	if strings.TrimSpace(displayName) == "" || strings.ContainsRune(displayName, 0) || len(displayName) > 200 {
		return errors.New("display name must contain 1 to 200 bytes")
	}
	hash, e := access.PasswordHash(password)
	if e != nil {
		return e
	}
	tx, e := s.Pool.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(context.Background())
	if _, e = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(7314202)`); e != nil {
		return e
	}
	var exists bool
	if e = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM accounts)`).Scan(&exists); e != nil {
		return e
	}
	if exists {
		return fault.New(fault.Conflict, "BOOTSTRAP_COMPLETE", "初始化账号已存在；该命令不会覆盖账号或密码")
	}
	if _, e = tx.Exec(ctx, `INSERT INTO accounts(id,username,display_name,password_hash) VALUES($1,$2,$3,$4)`, ids.New(), username, displayName, hash); e != nil {
		return e
	}
	return tx.Commit(ctx)
}
func (s *Store) ReserveLogin(ctx context.Context, account, ip string) (bool, error) {
	tx, e := s.Pool.Begin(ctx)
	if e != nil {
		return false, e
	}
	defer tx.Rollback(context.Background())
	allowed := true
	for _, b := range []struct {
		key   string
		limit int
	}{{"ip:" + ip, 60}, {"account:" + account, 10}} {
		var hits int
		e = tx.QueryRow(ctx, `INSERT INTO login_buckets(bucket_key,window_start,attempts) VALUES($1,now(),1)
   ON CONFLICT(bucket_key) DO UPDATE SET
   window_start=CASE WHEN login_buckets.window_start<now()-interval '5 minutes' THEN now() ELSE login_buckets.window_start END,
   attempts=CASE WHEN login_buckets.window_start<now()-interval '5 minutes' THEN 1 ELSE least(login_buckets.attempts+1,1000000) END RETURNING attempts`, b.key).Scan(&hits)
		if e != nil {
			return false, e
		}
		if hits > b.limit {
			allowed = false
			// Commit the IP reservation without allocating a new account bucket
			// after this source has exhausted its budget.
			break
		}
	}
	if e = tx.Commit(ctx); e != nil {
		return false, e
	}
	return allowed, nil
}
func (s *Store) FindUser(ctx context.Context, username string) (access.User, error) {
	var u access.User
	e := s.Pool.QueryRow(ctx, `SELECT id::text,username,display_name,password_hash,disabled FROM accounts WHERE username=$1`, username).Scan(&u.ID, &u.Username, &u.DisplayName, &u.PasswordHash, &u.Disabled)
	if errors.Is(e, pgx.ErrNoRows) {
		e = access.ErrMissing
	}
	return u, e
}
func (s *Store) InsertSession(ctx context.Context, v access.Session) error {
	_, e := s.Pool.Exec(ctx, `INSERT INTO sessions(secret_hash,user_id,csrf_token,expires_at) VALUES($1,$2,$3,$4)`, v.SecretHash, v.UserID, v.CSRF, v.ExpiresAt)
	return e
}

// RotateSession consumes the prior credential and installs its replacement in
// one transaction. A concurrent logout or rotation must not revive a session.
func (s *Store) RotateSession(ctx context.Context, priorHash string, replacement access.Session) error {
	tx, e := s.Pool.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(context.Background())
	result, e := tx.Exec(ctx, `DELETE FROM sessions WHERE secret_hash=$1 AND expires_at>now()`, priorHash)
	if e != nil {
		return e
	}
	if result.RowsAffected() != 1 {
		return access.ErrMissing
	}
	if _, e = tx.Exec(ctx, `INSERT INTO sessions(secret_hash,user_id,csrf_token,expires_at) VALUES($1,$2,$3,$4)`, replacement.SecretHash, replacement.UserID, replacement.CSRF, replacement.ExpiresAt); e != nil {
		return e
	}
	return tx.Commit(ctx)
}
func (s *Store) FindSession(ctx context.Context, hash string) (access.Session, error) {
	var v access.Session
	e := s.Pool.QueryRow(ctx, `SELECT s.user_id::text,a.display_name,s.secret_hash,s.csrf_token,s.expires_at FROM sessions s JOIN accounts a ON a.id=s.user_id WHERE s.secret_hash=$1 AND s.expires_at>now() AND NOT a.disabled`, hash).Scan(&v.UserID, &v.DisplayName, &v.SecretHash, &v.CSRF, &v.ExpiresAt)
	if errors.Is(e, pgx.ErrNoRows) {
		e = access.ErrMissing
	}
	return v, e
}
func (s *Store) DeleteSession(ctx context.Context, hash string) error {
	_, e := s.Pool.Exec(ctx, `DELETE FROM sessions WHERE secret_hash=$1`, hash)
	return e
}
func (s *Store) CreateProject(ctx context.Context, userID string, p access.Project) error {
	tx, e := s.Pool.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(context.Background())
	if _, e = tx.Exec(ctx, `INSERT INTO projects(id,name,timezone) VALUES($1,$2,$3)`, p.ID, p.Name, p.Timezone); e != nil {
		return e
	}
	if _, e = tx.Exec(ctx, `INSERT INTO project_members(project_id,user_id,role) VALUES($1,$2,'owner')`, p.ID, userID); e != nil {
		return e
	}
	if _, e = tx.Exec(ctx, `INSERT INTO audit_entries(project_id,actor_id,action,resource_key) VALUES($1,$2,'project.created',$3)`, p.ID, userID, p.ID); e != nil {
		return e
	}
	return tx.Commit(ctx)
}
func (s *Store) ListProjects(ctx context.Context, userID, after string, limit int) ([]access.Project, error) {
	rows, e := s.Pool.Query(ctx, `SELECT p.id::text,p.name,p.timezone,m.role FROM projects p JOIN project_members m ON m.project_id=p.id WHERE m.user_id=$1 AND ($2='' OR p.id>nullif($2,'')::uuid) ORDER BY p.id LIMIT $3`, userID, after, limit)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := make([]access.Project, 0)
	for rows.Next() {
		var p access.Project
		if e = rows.Scan(&p.ID, &p.Name, &p.Timezone, &p.Role); e != nil {
			return nil, e
		}
		out = append(out, p)
	}
	return out, rows.Err()
}
func (s *Store) ProjectRole(ctx context.Context, userID, projectID string) (string, error) {
	var role string
	e := s.Pool.QueryRow(ctx, `SELECT role FROM project_members WHERE user_id=$1 AND project_id=$2`, userID, projectID).Scan(&role)
	if errors.Is(e, pgx.ErrNoRows) {
		e = access.ErrMissing
	}
	return role, e
}
