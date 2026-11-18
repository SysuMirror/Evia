package access

import (
	"context"
	"strings"

	"github.com/howtomakeaname/Evia/backend/internal/platform/canonical"
	"github.com/howtomakeaname/Evia/backend/internal/platform/fault"
)

// Membership revisions cover the complete project collection, including removals.
// A cursor carrying a prior revision cannot silently mix membership snapshots.
const MaxMembershipRevision int64 = 9007199254740991

// Project role closed set, mirrored by the ProjectMember* OpenAPI enums and
// guarded by TestProjectRoleContractStaysAligned.
const (
	RoleOwner  = "owner"
	RoleEditor = "editor"
	RoleViewer = "viewer"
)

// ProjectRoles is the single Go-side source of truth for the role vocabulary.
var ProjectRoles = []string{RoleOwner, RoleEditor, RoleViewer}

type Member struct {
	UserID, Username, DisplayName, Role string
	Disabled                            bool
}
type MemberListFilter struct {
	After    string
	Revision int64
	Limit    int
}
type MemberPage struct {
	Items    []Member
	Revision int64
}
type MemberInput struct {
	Username, Role, IdempotencyKey string
	Revision                       int64
}
type MemberResult struct {
	Member   Member
	Revision int64
}
type MembershipStore interface {
	ListProjectMembers(context.Context, string, string, MemberListFilter) (MemberPage, error)
	MutateProjectMember(context.Context, string, string, string, string, MemberInput, string) (MemberResult, error)
}
type Memberships struct{ store MembershipStore }

func NewMemberships(store MembershipStore) *Memberships { return &Memberships{store: store} }

func membershipHuman(ctx context.Context) error {
	if _, ok := IdentityFromContext(ctx); ok {
		return fault.New(fault.Forbidden, "MACHINE_FORBIDDEN", "机器主体不能管理项目成员")
	}
	return nil
}
func membershipScope(ctx context.Context, project string) error {
	if e := membershipHuman(ctx); e != nil {
		return e
	}
	if !projectIDPattern.MatchString(project) {
		return fault.New(fault.Invalid, "INVALID_PROJECT_ID", "项目标识格式不正确")
	}
	return nil
}
func (s *Memberships) List(ctx context.Context, actor, project string, in MemberListFilter) (MemberPage, error) {
	if e := membershipScope(ctx, project); e != nil {
		return MemberPage{}, e
	}
	if in.Limit < 1 || in.Limit > 100 {
		return MemberPage{}, fault.New(fault.Invalid, "INVALID_LIMIT", "分页大小超出范围")
	}
	if in.After != "" && (!projectIDPattern.MatchString(in.After) || in.Revision < 1) || in.Revision < 0 || in.Revision > MaxMembershipRevision {
		return MemberPage{}, fault.New(fault.Invalid, "INVALID_CURSOR", "成员分页游标无效")
	}
	in.Limit++
	return s.store.ListProjectMembers(ctx, actor, project, in)
}
func (s *Memberships) Add(ctx context.Context, actor, project string, in MemberInput) (MemberResult, error) {
	in.Username = strings.ToLower(strings.TrimSpace(in.Username))
	return s.mutate(ctx, actor, project, "", "add", in)
}
func (s *Memberships) Update(ctx context.Context, actor, project, user string, in MemberInput) (MemberResult, error) {
	return s.mutate(ctx, actor, project, user, "update", in)
}
func (s *Memberships) Remove(ctx context.Context, actor, project, user string, in MemberInput) (MemberResult, error) {
	return s.mutate(ctx, actor, project, user, "remove", in)
}
func (s *Memberships) mutate(ctx context.Context, actor, project, user, operation string, in MemberInput) (MemberResult, error) {
	if e := membershipScope(ctx, project); e != nil {
		return MemberResult{}, e
	}
	if operation != "add" && !projectIDPattern.MatchString(user) {
		return MemberResult{}, fault.New(fault.Invalid, "INVALID_MEMBER_ID", "成员标识格式不正确")
	}
	if in.Revision < 1 || in.Revision > MaxMembershipRevision {
		return MemberResult{}, fault.New(fault.Invalid, "REVISION_REQUIRED", "必须提供有效成员修订")
	}
	if e := machineKey(in.IdempotencyKey); e != nil {
		return MemberResult{}, e
	}
	if operation == "add" && !usernamePattern.MatchString(in.Username) {
		return MemberResult{}, fault.New(fault.Invalid, "INVALID_USERNAME", "账号名称无效")
	}
	if operation != "add" && in.Username != "" || operation == "remove" && in.Role != "" {
		return MemberResult{}, fault.New(fault.Invalid, "INVALID_MEMBER_INPUT", "成员操作参数无效")
	}
	if operation != "remove" && in.Role != RoleOwner && in.Role != RoleEditor && in.Role != RoleViewer {
		return MemberResult{}, fault.New(fault.Invalid, "INVALID_MEMBER_ROLE", "成员角色无效")
	}
	hash, e := canonical.Hash(struct {
		Operation, UserID, Username, Role string
		Revision                          int64
	}{operation, user, in.Username, in.Role, in.Revision})
	if e != nil {
		return MemberResult{}, e
	}
	return s.store.MutateProjectMember(ctx, actor, project, user, operation, in, hash)
}
