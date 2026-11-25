package postgres

import (
	"context"
	"errors"
	"time"

	"github.com/howtomakeaname/Evia/backend/internal/access"
	"github.com/howtomakeaname/Evia/backend/internal/platform/fault"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

var _ access.AccountSelfStore = (*Store)(nil)
var _ access.InvitationStore = (*Store)(nil)

func (s *Store) GetAccountProfile(ctx context.Context, userID string) (access.AccountProfile, error) {
	var p access.AccountProfile
	err := s.Pool.QueryRow(ctx, `SELECT id::text,username,display_name,created_at FROM accounts WHERE id=$1 AND NOT disabled`, userID).
		Scan(&p.UserID, &p.Username, &p.DisplayName, &p.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return p, fault.New(fault.Unauthenticated, "SESSION_EXPIRED", "登录已过期，请重新登录")
	}
	return p, err
}

func (s *Store) UpdateAccountProfile(ctx context.Context, userID, displayName string) (access.AccountProfile, error) {
	var p access.AccountProfile
	err := s.Pool.QueryRow(ctx, `UPDATE accounts SET display_name=$2 WHERE id=$1 AND NOT disabled RETURNING id::text,username,display_name,created_at`, userID, displayName).
		Scan(&p.UserID, &p.Username, &p.DisplayName, &p.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return p, fault.New(fault.Unauthenticated, "SESSION_EXPIRED", "登录已过期，请重新登录")
	}
	return p, err
}

func (s *Store) GetAccountPasswordHash(ctx context.Context, userID string) (string, error) {
	var hash string
	err := s.Pool.QueryRow(ctx, `SELECT password_hash FROM accounts WHERE id=$1 AND NOT disabled`, userID).Scan(&hash)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", access.ErrMissing
	}
	return hash, err
}

func (s *Store) ChangeAccountPassword(ctx context.Context, userID, newHash, currentSecretHash string, replacement access.Session) (access.Session, error) {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return access.Session{}, err
	}
	defer tx.Rollback(context.Background())
	var displayName string
	err = tx.QueryRow(ctx, `UPDATE accounts SET password_hash=$2 WHERE id=$1 AND NOT disabled RETURNING display_name`, userID, newHash).Scan(&displayName)
	if errors.Is(err, pgx.ErrNoRows) {
		return access.Session{}, access.ErrMissing
	}
	if err != nil {
		return access.Session{}, err
	}
	var sessionExists bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM sessions WHERE secret_hash=$1 AND user_id=$2 AND expires_at>now())`, currentSecretHash, userID).Scan(&sessionExists); err != nil {
		return access.Session{}, err
	}
	if !sessionExists {
		return access.Session{}, access.ErrMissing
	}
	if _, err = tx.Exec(ctx, `DELETE FROM sessions WHERE user_id=$1`, userID); err != nil {
		return access.Session{}, err
	}
	replacement.DisplayName = displayName
	if _, err = tx.Exec(ctx, `INSERT INTO sessions(secret_hash,user_id,csrf_token,expires_at) VALUES($1,$2,$3,$4)`, replacement.SecretHash, replacement.UserID, replacement.CSRF, replacement.ExpiresAt); err != nil {
		return access.Session{}, err
	}
	return replacement, tx.Commit(ctx)
}

func invitationStatus(acceptedAt, revokedAt *time.Time, expiresAt, now time.Time) string {
	switch {
	case acceptedAt != nil:
		return access.InvitationStatusAccepted
	case revokedAt != nil:
		return access.InvitationStatusRevoked
	case !expiresAt.After(now):
		return access.InvitationStatusExpired
	default:
		return access.InvitationStatusPending
	}
}

func scanInvitation(row queryScanner, now time.Time) (access.ProjectInvitation, error) {
	var inv access.ProjectInvitation
	err := row.Scan(
		&inv.ID,
		&inv.ProjectID,
		&inv.Role,
		&inv.Note,
		&inv.CreatedBy,
		&inv.CreatedByDisplayName,
		&inv.CreatedAt,
		&inv.ExpiresAt,
		&inv.AcceptedBy,
		&inv.AcceptedUsername,
		&inv.AcceptedAt,
		&inv.RevokedAt,
	)
	if err != nil {
		return inv, err
	}
	inv.Status = invitationStatus(inv.AcceptedAt, inv.RevokedAt, inv.ExpiresAt, now)
	return inv, nil
}

const invitationSelectColumns = `
	i.id::text,
	i.project_id::text,
	i.role,
	i.note,
	i.created_by::text,
	c.display_name,
	i.created_at,
	i.expires_at,
	i.accepted_by::text,
	a.username,
	i.accepted_at,
	i.revoked_at
`

func (s *Store) ListProjectInvitations(ctx context.Context, actor, project string, after *access.InvitationPosition, limit int) ([]access.ProjectInvitation, error) {
	if err := memberHuman(ctx); err != nil {
		return nil, err
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(context.Background())
	if err = memberOwner(ctx, tx, actor, project); err != nil {
		return nil, err
	}
	var now time.Time
	if err = tx.QueryRow(ctx, `SELECT now()`).Scan(&now); err != nil {
		return nil, err
	}
	var rows pgx.Rows
	if after == nil {
		rows, err = tx.Query(ctx, `SELECT `+invitationSelectColumns+`
			FROM project_invitations i
			JOIN accounts c ON c.id=i.created_by
			LEFT JOIN accounts a ON a.id=i.accepted_by
			WHERE i.project_id=$1
			ORDER BY i.created_at DESC, i.id DESC
			LIMIT $2`, project, limit)
	} else {
		rows, err = tx.Query(ctx, `SELECT `+invitationSelectColumns+`
			FROM project_invitations i
			JOIN accounts c ON c.id=i.created_by
			LEFT JOIN accounts a ON a.id=i.accepted_by
			WHERE i.project_id=$1 AND (i.created_at, i.id) < ($2, $3::uuid)
			ORDER BY i.created_at DESC, i.id DESC
			LIMIT $4`, project, after.CreatedAt.UTC(), after.ID, limit)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]access.ProjectInvitation, 0)
	for rows.Next() {
		inv, err := scanInvitation(rows, now)
		if err != nil {
			return nil, err
		}
		out = append(out, inv)
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	return out, tx.Commit(ctx)
}

func (s *Store) CreateProjectInvitation(ctx context.Context, actor, project, id, role, tokenHash, note string, expiresAt time.Time) (access.ProjectInvitation, error) {
	if err := memberHuman(ctx); err != nil {
		return access.ProjectInvitation{}, err
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return access.ProjectInvitation{}, err
	}
	defer tx.Rollback(context.Background())
	if err = memberOwner(ctx, tx, actor, project); err != nil {
		return access.ProjectInvitation{}, err
	}
	if _, err = tx.Exec(ctx, `SELECT id FROM projects WHERE id=$1 FOR UPDATE`, project); err != nil {
		return access.ProjectInvitation{}, err
	}
	if err = setAuditCredential(ctx, tx, ""); err != nil {
		return access.ProjectInvitation{}, err
	}
	var pending int
	if err = tx.QueryRow(ctx, `SELECT count(*) FROM project_invitations WHERE project_id=$1 AND accepted_at IS NULL AND revoked_at IS NULL AND expires_at>now()`, project).Scan(&pending); err != nil {
		return access.ProjectInvitation{}, err
	}
	if pending >= access.MaxPendingInvitations {
		return access.ProjectInvitation{}, fault.New(fault.Conflict, "INVITATION_LIMIT_EXCEEDED", "项目待接受邀请数量已达上限，请先撤销旧邀请")
	}
	if _, err = tx.Exec(ctx, `INSERT INTO project_invitations(id,project_id,role,token_hash,note,created_by,expires_at) VALUES($1,$2,$3,$4,$5,$6,$7)`,
		id, project, role, tokenHash, note, actor, expiresAt.UTC()); err != nil {
		return access.ProjectInvitation{}, err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO audit_entries(project_id,actor_id,action,resource_key) VALUES($1,$2,'project.invitation_created',$3)`, project, actor, id); err != nil {
		return access.ProjectInvitation{}, err
	}
	var now time.Time
	if err = tx.QueryRow(ctx, `SELECT now()`).Scan(&now); err != nil {
		return access.ProjectInvitation{}, err
	}
	inv, err := scanInvitation(tx.QueryRow(ctx, `SELECT `+invitationSelectColumns+`
		FROM project_invitations i
		JOIN accounts c ON c.id=i.created_by
		LEFT JOIN accounts a ON a.id=i.accepted_by
		WHERE i.id=$1`, id), now)
	if err != nil {
		return access.ProjectInvitation{}, err
	}
	return inv, tx.Commit(ctx)
}

func (s *Store) RevokeProjectInvitation(ctx context.Context, actor, project, invitationID string) (access.ProjectInvitation, error) {
	if err := memberHuman(ctx); err != nil {
		return access.ProjectInvitation{}, err
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return access.ProjectInvitation{}, err
	}
	defer tx.Rollback(context.Background())
	if err = memberOwner(ctx, tx, actor, project); err != nil {
		return access.ProjectInvitation{}, err
	}
	if err = setAuditCredential(ctx, tx, ""); err != nil {
		return access.ProjectInvitation{}, err
	}
	var acceptedAt, revokedAt *time.Time
	err = tx.QueryRow(ctx, `SELECT accepted_at,revoked_at FROM project_invitations WHERE id=$1 AND project_id=$2 FOR UPDATE`, invitationID, project).Scan(&acceptedAt, &revokedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return access.ProjectInvitation{}, fault.New(fault.NotFound, "INVITATION_NOT_FOUND", "邀请不存在或不可见")
	}
	if err != nil {
		return access.ProjectInvitation{}, err
	}
	if acceptedAt != nil {
		return access.ProjectInvitation{}, fault.New(fault.Conflict, "INVITATION_ALREADY_ACCEPTED", "该邀请已被接受，无法撤销")
	}
	if revokedAt == nil {
		if _, err = tx.Exec(ctx, `UPDATE project_invitations SET revoked_by=$3,revoked_at=now() WHERE id=$1 AND project_id=$2`, invitationID, project, actor); err != nil {
			return access.ProjectInvitation{}, err
		}
		if _, err = tx.Exec(ctx, `INSERT INTO audit_entries(project_id,actor_id,action,resource_key) VALUES($1,$2,'project.invitation_revoked',$3)`, project, actor, invitationID); err != nil {
			return access.ProjectInvitation{}, err
		}
	}
	var now time.Time
	if err = tx.QueryRow(ctx, `SELECT now()`).Scan(&now); err != nil {
		return access.ProjectInvitation{}, err
	}
	inv, err := scanInvitation(tx.QueryRow(ctx, `SELECT `+invitationSelectColumns+`
		FROM project_invitations i
		JOIN accounts c ON c.id=i.created_by
		LEFT JOIN accounts a ON a.id=i.accepted_by
		WHERE i.id=$1`, invitationID), now)
	if err != nil {
		return access.ProjectInvitation{}, err
	}
	return inv, tx.Commit(ctx)
}

func (s *Store) PreviewInvitation(ctx context.Context, tokenHash string) (access.InvitationPreview, error) {
	var p access.InvitationPreview
	err := s.Pool.QueryRow(ctx, `SELECT i.project_id::text,pr.name,i.role,i.note,i.expires_at
		FROM project_invitations i
		JOIN projects pr ON pr.id=i.project_id
		WHERE i.token_hash=$1 AND i.accepted_at IS NULL AND i.revoked_at IS NULL AND i.expires_at>now()`, tokenHash).
		Scan(&p.ProjectID, &p.ProjectName, &p.Role, &p.Note, &p.ExpiresAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return p, access.ErrMissing
	}
	return p, err
}

func (s *Store) AcceptInvitationNewAccount(ctx context.Context, tokenHash, userID, username, displayName, passwordHash string, session access.Session) (access.InvitationAcceptResult, error) {
	var out access.InvitationAcceptResult
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return out, err
	}
	defer tx.Rollback(context.Background())

	var invID, projectID, projectName, role string
	err = tx.QueryRow(ctx, `SELECT i.id::text,i.project_id::text,pr.name,i.role
		FROM project_invitations i
		JOIN projects pr ON pr.id=i.project_id
		WHERE i.token_hash=$1 AND i.accepted_at IS NULL AND i.revoked_at IS NULL AND i.expires_at>now()
		FOR UPDATE OF i`, tokenHash).Scan(&invID, &projectID, &projectName, &role)
	if errors.Is(err, pgx.ErrNoRows) {
		return out, access.ErrMissing
	}
	if err != nil {
		return out, err
	}
	var revision int64
	if err = tx.QueryRow(ctx, `SELECT membership_revision FROM projects WHERE id=$1 FOR UPDATE`, projectID).Scan(&revision); err != nil {
		return out, err
	}
	if revision == access.MaxMembershipRevision {
		return out, fault.New(fault.Conflict, "MEMBERSHIP_REVISION_EXHAUSTED", "成员修订已达到上限")
	}
	var count int
	if err = tx.QueryRow(ctx, `SELECT count(*) FROM project_members WHERE project_id=$1`, projectID).Scan(&count); err != nil {
		return out, err
	}
	if count >= 1000 {
		return out, fault.New(fault.Conflict, "MEMBER_LIMIT_REACHED", "项目成员数量已达到上限")
	}
	if _, err = tx.Exec(ctx, `INSERT INTO accounts(id,username,display_name,password_hash) VALUES($1,$2,$3,$4)`, userID, username, displayName, passwordHash); err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return out, fault.New(fault.Conflict, "USERNAME_TAKEN", "用户名已被使用，请更换用户名")
		}
		return out, err
	}
	if err = setAuditCredential(ctx, tx, ""); err != nil {
		return out, err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO project_members(project_id,user_id,role) VALUES($1,$2,$3)`, projectID, userID, role); err != nil {
		return out, err
	}
	if _, err = tx.Exec(ctx, `UPDATE projects SET membership_revision=membership_revision+1 WHERE id=$1`, projectID); err != nil {
		return out, err
	}
	if _, err = tx.Exec(ctx, `UPDATE project_invitations SET accepted_by=$2,accepted_at=now() WHERE id=$1`, invID, userID); err != nil {
		return out, err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO audit_entries(project_id,actor_id,action,resource_key) VALUES($1,$2,'project.invitation_accepted',$3)`, projectID, userID, invID); err != nil {
		return out, err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO sessions(secret_hash,user_id,csrf_token,expires_at) VALUES($1,$2,$3,$4)`, session.SecretHash, session.UserID, session.CSRF, session.ExpiresAt); err != nil {
		return out, err
	}
	if err = tx.Commit(ctx); err != nil {
		return out, err
	}
	return access.InvitationAcceptResult{
		Session:     session,
		ProjectID:   projectID,
		ProjectName: projectName,
		Role:        role,
	}, nil
}

func (s *Store) AcceptInvitationExistingAccount(ctx context.Context, tokenHash string, session access.Session) (access.InvitationAcceptResult, error) {
	var out access.InvitationAcceptResult
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return out, err
	}
	defer tx.Rollback(context.Background())

	var disabled bool
	err = tx.QueryRow(ctx, `SELECT disabled FROM accounts WHERE id=$1 FOR SHARE`, session.UserID).Scan(&disabled)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && disabled) {
		return out, fault.New(fault.Unauthenticated, "SESSION_EXPIRED", "登录已过期，请重新登录")
	}
	if err != nil {
		return out, err
	}
	var invID, projectID, projectName, role string
	err = tx.QueryRow(ctx, `SELECT i.id::text,i.project_id::text,pr.name,i.role
		FROM project_invitations i
		JOIN projects pr ON pr.id=i.project_id
		WHERE i.token_hash=$1 AND i.accepted_at IS NULL AND i.revoked_at IS NULL AND i.expires_at>now()
		FOR UPDATE OF i`, tokenHash).Scan(&invID, &projectID, &projectName, &role)
	if errors.Is(err, pgx.ErrNoRows) {
		return out, access.ErrMissing
	}
	if err != nil {
		return out, err
	}
	var revision int64
	if err = tx.QueryRow(ctx, `SELECT membership_revision FROM projects WHERE id=$1 FOR UPDATE`, projectID).Scan(&revision); err != nil {
		return out, err
	}
	if revision == access.MaxMembershipRevision {
		return out, fault.New(fault.Conflict, "MEMBERSHIP_REVISION_EXHAUSTED", "成员修订已达到上限")
	}
	var exists bool
	var count int
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM project_members WHERE project_id=$1 AND user_id=$2),(SELECT count(*) FROM project_members WHERE project_id=$1)`, projectID, session.UserID).Scan(&exists, &count); err != nil {
		return out, err
	}
	if exists {
		return out, fault.New(fault.Conflict, "MEMBER_EXISTS", "你已是该项目成员，无需重复接受邀请")
	}
	if count >= 1000 {
		return out, fault.New(fault.Conflict, "MEMBER_LIMIT_REACHED", "项目成员数量已达到上限")
	}
	if err = setAuditCredential(ctx, tx, ""); err != nil {
		return out, err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO project_members(project_id,user_id,role) VALUES($1,$2,$3)`, projectID, session.UserID, role); err != nil {
		return out, err
	}
	if _, err = tx.Exec(ctx, `UPDATE projects SET membership_revision=membership_revision+1 WHERE id=$1`, projectID); err != nil {
		return out, err
	}
	if _, err = tx.Exec(ctx, `UPDATE project_invitations SET accepted_by=$2,accepted_at=now() WHERE id=$1`, invID, session.UserID); err != nil {
		return out, err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO audit_entries(project_id,actor_id,action,resource_key) VALUES($1,$2,'project.invitation_accepted',$3)`, projectID, session.UserID, invID); err != nil {
		return out, err
	}
	if err = tx.Commit(ctx); err != nil {
		return out, err
	}
	return access.InvitationAcceptResult{
		Session:     session,
		ProjectID:   projectID,
		ProjectName: projectName,
		Role:        role,
	}, nil
}
