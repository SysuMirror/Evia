package postgres

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/howtomakeaname/Evia/backend/internal/access"
	"github.com/howtomakeaname/Evia/backend/internal/platform/fault"
	"github.com/jackc/pgx/v5"
)

var _ access.MembershipStore = (*Store)(nil)

func memberConflict() error {
	return fault.New(fault.Conflict, "MEMBERSHIP_REVISION_CONFLICT", "成员修订已变化，请重新加载")
}
func memberMissing() error {
	return fault.New(fault.NotFound, "MEMBER_NOT_FOUND", "成员不存在或不可见")
}
func memberOwner(ctx context.Context, tx pgx.Tx, actor, project string) error {
	var role string
	e := tx.QueryRow(ctx, `SELECT m.role FROM project_members m JOIN accounts a ON a.id=m.user_id WHERE m.project_id=$1 AND m.user_id=$2 AND NOT a.disabled`, project, actor).Scan(&role)
	if errors.Is(e, pgx.ErrNoRows) {
		return projectInvisible()
	}
	if e != nil {
		return e
	}
	if role != "owner" {
		return fault.New(fault.Forbidden, "MEMBER_MANAGEMENT_FORBIDDEN", "只有项目所有者可以管理成员")
	}
	return nil
}
func memberHuman(ctx context.Context) error {
	_, machine := access.IdentityFromContext(ctx)
	if machine {
		return machineDenied()
	}
	return nil
}
func scanMember(row queryScanner) (access.Member, error) {
	var v access.Member
	e := row.Scan(&v.UserID, &v.Username, &v.DisplayName, &v.Role, &v.Disabled)
	if errors.Is(e, pgx.ErrNoRows) {
		e = memberMissing()
	}
	return v, e
}

func (s *Store) ListProjectMembers(ctx context.Context, actor, project string, in access.MemberListFilter) (access.MemberPage, error) {
	out := access.MemberPage{Items: []access.Member{}}
	if e := memberHuman(ctx); e != nil {
		return out, e
	}
	if in.Limit < 1 || in.Limit > 101 {
		return out, fault.New(fault.Invalid, "INVALID_LIMIT", "分页大小超出范围")
	}
	tx, e := s.Pool.Begin(ctx)
	if e != nil {
		return out, e
	}
	defer tx.Rollback(context.Background())
	// Match the mutation order: membership/account before project. The project
	// share lock provides a stable collection for both revision and member rows.
	var actorID string
	e = tx.QueryRow(ctx, `SELECT m.user_id::text FROM project_members m JOIN accounts a ON a.id=m.user_id WHERE m.project_id=$1 AND m.user_id=$2 AND NOT a.disabled FOR SHARE OF m,a`, project, actor).Scan(&actorID)
	if errors.Is(e, pgx.ErrNoRows) {
		return out, projectInvisible()
	}
	if e != nil {
		return out, e
	}
	if e = memberOwner(ctx, tx, actor, project); e != nil {
		return out, e
	}
	if e = tx.QueryRow(ctx, `SELECT membership_revision FROM projects WHERE id=$1 FOR SHARE`, project).Scan(&out.Revision); e != nil {
		return out, e
	}
	if in.Revision != 0 && in.Revision != out.Revision {
		return out, memberConflict()
	}
	rows, e := tx.Query(ctx, `SELECT a.id::text,a.username,a.display_name,m.role,a.disabled FROM project_members m JOIN accounts a ON a.id=m.user_id WHERE m.project_id=$1 AND ($2='' OR m.user_id>nullif($2,'')::uuid) ORDER BY m.user_id LIMIT $3`, project, in.After, in.Limit)
	if e != nil {
		return out, e
	}
	for rows.Next() {
		v, e := scanMember(rows)
		if e != nil {
			rows.Close()
			return out, e
		}
		out.Items = append(out.Items, v)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return out, e
	}
	return out, tx.Commit(ctx)
}

func (s *Store) MutateProjectMember(ctx context.Context, actor, project, user, operation string, in access.MemberInput, hash string) (access.MemberResult, error) {
	var out access.MemberResult
	if e := memberHuman(ctx); e != nil {
		return out, e
	}
	tx, e := s.Pool.Begin(ctx)
	if e != nil {
		return out, e
	}
	defer tx.Rollback(context.Background())
	// Check authority before resolving any global account name. This initial
	// check is repeated after locks and cannot authorize a mutation on its own.
	if e = memberOwner(ctx, tx, actor, project); e != nil {
		return out, e
	}
	if operation == "add" {
		e = tx.QueryRow(ctx, `SELECT id::text FROM accounts WHERE username=$1`, in.Username).Scan(&user)
		if e != nil && !errors.Is(e, pgx.ErrNoRows) {
			return out, e
		}
	}
	// Existing business mutations acquire membership/account then project locks.
	// Lock actor and target memberships in UUID order before the project. Taking
	// a target row after a project lock would deadlock an authorized target write.
	rows, e := tx.Query(ctx, `SELECT m.user_id::text FROM project_members m JOIN accounts a ON a.id=m.user_id WHERE m.project_id=$1 AND (m.user_id=$2 OR m.user_id=nullif($3,'')::uuid) ORDER BY m.user_id FOR UPDATE OF m FOR SHARE OF a`, project, actor, user)
	if e != nil {
		return out, e
	}
	for rows.Next() {
		var id string
		if e = rows.Scan(&id); e != nil {
			rows.Close()
			return out, e
		}
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return out, e
	}
	if e = memberOwner(ctx, tx, actor, project); e != nil {
		return out, e
	}
	// A new member has no membership row to lock. Pin its account before the
	// project so concurrent account disable/delete cannot bypass enabled checks.
	var disabled bool
	if user != "" {
		e = tx.QueryRow(ctx, `SELECT disabled FROM accounts WHERE id=$1 FOR SHARE`, user).Scan(&disabled)
		if e != nil && !errors.Is(e, pgx.ErrNoRows) {
			return out, e
		}
	}
	var revision int64
	if e = tx.QueryRow(ctx, `SELECT membership_revision FROM projects WHERE id=$1 FOR UPDATE`, project).Scan(&revision); e != nil {
		return out, e
	}
	if e = setAuditCredential(ctx, tx, ""); e != nil {
		return out, e
	}
	var storedHash string
	var result []byte
	e = tx.QueryRow(ctx, `SELECT request_hash,response FROM project_governance_mutations WHERE project_id=$1 AND actor_id=$2 AND idempotency_key=$3 AND operation=$4`, project, actor, in.IdempotencyKey, "members."+operation).Scan(&storedHash, &result)
	if e == nil {
		if storedHash != hash {
			return out, fault.New(fault.Conflict, "IDEMPOTENCY_CONFLICT", "幂等键已用于其他请求")
		}
		if e = json.Unmarshal(result, &out); e != nil {
			return out, e
		}
		return out, tx.Commit(ctx)
	}
	if !errors.Is(e, pgx.ErrNoRows) {
		return out, e
	}
	if revision != in.Revision {
		return out, memberConflict()
	}
	if revision == access.MaxMembershipRevision {
		return out, fault.New(fault.Conflict, "MEMBERSHIP_REVISION_EXHAUSTED", "成员修订已达到上限")
	}
	if operation == "add" {
		if user == "" {
			return out, fault.New(fault.NotFound, "ACCOUNT_NOT_FOUND", "账号不存在或不可用于加入项目")
		}
		if disabled {
			return out, fault.New(fault.Conflict, "ACCOUNT_DISABLED", "已停用账号不能加入项目")
		}
		var exists bool
		var count int
		if e = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM project_members WHERE project_id=$1 AND user_id=$2),(SELECT count(*) FROM project_members WHERE project_id=$1)`, project, user).Scan(&exists, &count); e != nil {
			return out, e
		}
		if exists {
			return out, fault.New(fault.Conflict, "MEMBER_EXISTS", "账号已是项目成员")
		}
		if count >= 1000 {
			return out, fault.New(fault.Conflict, "MEMBER_LIMIT_REACHED", "项目成员数量已达到上限")
		}
		if _, e = tx.Exec(ctx, `INSERT INTO project_members(project_id,user_id,role) VALUES($1,$2,$3)`, project, user, in.Role); e != nil {
			return out, e
		}
	} else {
		out.Member, e = scanMember(tx.QueryRow(ctx, `SELECT a.id::text,a.username,a.display_name,m.role,a.disabled FROM project_members m JOIN accounts a ON a.id=m.user_id WHERE m.project_id=$1 AND m.user_id=$2`, project, user))
		if e != nil {
			return out, e
		}
		if out.Member.Role == "owner" && (operation == "remove" || in.Role != "owner") {
			var owners int
			if e = tx.QueryRow(ctx, `SELECT count(*) FROM project_members m JOIN accounts a ON a.id=m.user_id WHERE m.project_id=$1 AND m.role='owner' AND NOT a.disabled AND m.user_id<>$2`, project, user).Scan(&owners); e != nil {
				return out, e
			}
			if owners == 0 {
				return out, fault.New(fault.Conflict, "LAST_OWNER_REQUIRED", "项目必须保留至少一个启用的所有者")
			}
		}
		if operation == "remove" {
			_, e = tx.Exec(ctx, `DELETE FROM project_members WHERE project_id=$1 AND user_id=$2`, project, user)
		} else if operation == "update" {
			if in.Role == "owner" && disabled {
				return out, fault.New(fault.Conflict, "ACCOUNT_DISABLED", "已停用账号不能成为所有者")
			}
			_, e = tx.Exec(ctx, `UPDATE project_members SET role=$3 WHERE project_id=$1 AND user_id=$2`, project, user, in.Role)
		} else {
			return out, fault.New(fault.Invalid, "INVALID_MEMBER_INPUT", "成员操作参数无效")
		}
		if e != nil {
			return out, e
		}
	}
	if operation != "remove" {
		out.Member, e = scanMember(tx.QueryRow(ctx, `SELECT a.id::text,a.username,a.display_name,m.role,a.disabled FROM project_members m JOIN accounts a ON a.id=m.user_id WHERE m.project_id=$1 AND m.user_id=$2`, project, user))
		if e != nil {
			return out, e
		}
	}
	if e = tx.QueryRow(ctx, `UPDATE projects SET membership_revision=membership_revision+1 WHERE id=$1 RETURNING membership_revision`, project).Scan(&out.Revision); e != nil {
		return out, e
	}
	result, e = json.Marshal(out)
	if e != nil {
		return out, e
	}
	if _, e = tx.Exec(ctx, `INSERT INTO project_governance_mutations(project_id,actor_id,idempotency_key,operation,request_hash,response) VALUES($1,$2,$3,$4,$5,$6)`, project, actor, in.IdempotencyKey, "members."+operation, hash, result); e != nil {
		return out, e
	}
	action := map[string]string{"add": "project.member_added", "update": "project.member_role_changed", "remove": "project.member_removed"}[operation]
	if _, e = tx.Exec(ctx, `INSERT INTO audit_entries(project_id,actor_id,action,resource_key) VALUES($1,$2,$3,$4)`, project, actor, action, user); e != nil {
		return out, e
	}
	return out, tx.Commit(ctx)
}
