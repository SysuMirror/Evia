package access

import (
	"context"
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/howtomakeaname/Evia/backend/internal/platform/fault"
	"github.com/howtomakeaname/Evia/backend/internal/platform/ids"
	"golang.org/x/crypto/bcrypt"
)

const (
	InvitationStatusPending  = "pending"
	InvitationStatusAccepted = "accepted"
	InvitationStatusRevoked  = "revoked"
	InvitationStatusExpired  = "expired"

	DefaultInvitationTTLHours = 168
	MaxInvitationTTLHours     = 720
	MaxPendingInvitations     = 200
)

type AccountProfile struct {
	UserID      string
	Username    string
	DisplayName string
	CreatedAt   time.Time
}

type ProjectInvitation struct {
	ID                   string
	ProjectID            string
	Role                 string
	Note                 string
	Status               string
	CreatedBy            string
	CreatedByDisplayName string
	CreatedAt            time.Time
	ExpiresAt            time.Time
	AcceptedBy           *string
	AcceptedUsername     *string
	AcceptedAt           *time.Time
	RevokedAt            *time.Time
}

type InvitationPosition struct {
	CreatedAt time.Time
	ID        string
}

type InvitationCreateInput struct {
	Role           string
	Note           string
	ExpiresInHours int
}

type InvitationIssued struct {
	Invitation  ProjectInvitation
	InviteToken string
}

type InvitationPreview struct {
	ProjectID   string
	ProjectName string
	Role        string
	Note        string
	ExpiresAt   time.Time
}

type InvitationAcceptInput struct {
	Token       string
	Username    string
	DisplayName string
	Password    string
	RemoteIP    string
	Session     *Session
}

type InvitationAcceptResult struct {
	Session       Session
	SessionSecret string
	ProjectID     string
	ProjectName   string
	Role          string
}

type AccountSelfStore interface {
	GetAccountProfile(context.Context, string) (AccountProfile, error)
	UpdateAccountProfile(context.Context, string, string) (AccountProfile, error)
	GetAccountPasswordHash(context.Context, string) (string, error)
	ChangeAccountPassword(context.Context, string, string, string, Session) (Session, error)
}

type InvitationStore interface {
	ReserveLogin(context.Context, string, string) (bool, error)
	ListProjectInvitations(context.Context, string, string, *InvitationPosition, int) ([]ProjectInvitation, error)
	CreateProjectInvitation(context.Context, string, string, string, string, string, string, time.Time) (ProjectInvitation, error)
	RevokeProjectInvitation(context.Context, string, string, string) (ProjectInvitation, error)
	PreviewInvitation(context.Context, string) (InvitationPreview, error)
	AcceptInvitationNewAccount(context.Context, string, string, string, string, string, Session) (InvitationAcceptResult, error)
	AcceptInvitationExistingAccount(context.Context, string, Session) (InvitationAcceptResult, error)
}

type Accounts struct {
	store AccountSelfStore
}

func NewAccounts(store AccountSelfStore) *Accounts { return &Accounts{store: store} }

func (a *Accounts) Profile(ctx context.Context, userID string) (AccountProfile, error) {
	if _, machine := IdentityFromContext(ctx); machine {
		return AccountProfile{}, fault.New(fault.Forbidden, "MACHINE_FORBIDDEN", "机器主体无权执行此操作")
	}
	if !projectIDPattern.MatchString(userID) {
		return AccountProfile{}, fault.New(fault.Invalid, "INVALID_USER_ID", "账号标识无效")
	}
	return a.store.GetAccountProfile(ctx, userID)
}

func (a *Accounts) UpdateProfile(ctx context.Context, userID, displayName string) (AccountProfile, error) {
	if _, machine := IdentityFromContext(ctx); machine {
		return AccountProfile{}, fault.New(fault.Forbidden, "MACHINE_FORBIDDEN", "机器主体无权执行此操作")
	}
	if !projectIDPattern.MatchString(userID) {
		return AccountProfile{}, fault.New(fault.Invalid, "INVALID_USER_ID", "账号标识无效")
	}
	cleaned, err := normalizeDisplayName(displayName)
	if err != nil {
		return AccountProfile{}, err
	}
	return a.store.UpdateAccountProfile(ctx, userID, cleaned)
}

func (a *Accounts) ChangePassword(ctx context.Context, current Session, currentPassword, newPassword string) (Session, string, error) {
	if _, machine := IdentityFromContext(ctx); machine {
		return Session{}, "", fault.New(fault.Forbidden, "MACHINE_FORBIDDEN", "机器主体无权执行此操作")
	}
	if len(currentPassword) == 0 || len(currentPassword) > 72 {
		return Session{}, "", fault.New(fault.Unauthenticated, "INVALID_CREDENTIALS", "当前密码不正确")
	}
	existingHash, err := a.store.GetAccountPasswordHash(ctx, current.UserID)
	if err != nil {
		if errors.Is(err, ErrMissing) {
			return Session{}, "", fault.New(fault.Unauthenticated, "SESSION_EXPIRED", "登录已过期，请重新登录")
		}
		return Session{}, "", err
	}
	if bcrypt.CompareHashAndPassword([]byte(existingHash), []byte(currentPassword)) != nil {
		return Session{}, "", fault.New(fault.Unauthenticated, "INVALID_CREDENTIALS", "当前密码不正确")
	}
	nextHash, err := PasswordHash(newPassword)
	if err != nil {
		return Session{}, "", err
	}
	secret := ids.Secret()
	replacement := Session{
		UserID:      current.UserID,
		DisplayName: current.DisplayName,
		SecretHash:  Digest(secret),
		CSRF:        ids.Secret(),
		ExpiresAt:   time.Now().UTC().Add(8 * time.Hour),
	}
	rotated, err := a.store.ChangeAccountPassword(ctx, current.UserID, nextHash, current.SecretHash, replacement)
	if err != nil {
		if errors.Is(err, ErrMissing) {
			return Session{}, "", fault.New(fault.Unauthenticated, "SESSION_EXPIRED", "登录已过期，请重新登录")
		}
		return Session{}, "", err
	}
	return rotated, secret, nil
}

type Invitations struct {
	store InvitationStore
}

func NewInvitations(store InvitationStore) *Invitations { return &Invitations{store: store} }

func (i *Invitations) List(ctx context.Context, actor, project string, after *InvitationPosition, limit int) ([]ProjectInvitation, error) {
	if _, machine := IdentityFromContext(ctx); machine {
		return nil, fault.New(fault.Forbidden, "MACHINE_FORBIDDEN", "机器主体无权执行此操作")
	}
	if !projectIDPattern.MatchString(project) {
		return nil, fault.New(fault.Invalid, "INVALID_PROJECT_ID", "项目标识格式不正确")
	}
	if limit < 1 || limit > 100 {
		return nil, fault.New(fault.Invalid, "INVALID_LIMIT", "分页大小超出范围")
	}
	if after != nil {
		if !projectIDPattern.MatchString(after.ID) || after.CreatedAt.IsZero() {
			return nil, fault.New(fault.Invalid, "INVALID_CURSOR", "分页游标无效")
		}
	}
	return i.store.ListProjectInvitations(ctx, actor, project, after, limit+1)
}

func (i *Invitations) Create(ctx context.Context, actor, project string, in InvitationCreateInput) (InvitationIssued, error) {
	if _, machine := IdentityFromContext(ctx); machine {
		return InvitationIssued{}, fault.New(fault.Forbidden, "MACHINE_FORBIDDEN", "机器主体无权执行此操作")
	}
	if !projectIDPattern.MatchString(project) {
		return InvitationIssued{}, fault.New(fault.Invalid, "INVALID_PROJECT_ID", "项目标识格式不正确")
	}
	if !validRole(in.Role) {
		return InvitationIssued{}, fault.New(fault.Invalid, "INVALID_ROLE", "成员角色无效")
	}
	note := strings.TrimSpace(in.Note)
	if strings.ContainsRune(note, 0) || !utf8.ValidString(note) || utf8.RuneCountInString(note) > 200 {
		return InvitationIssued{}, fault.New(fault.Invalid, "INVALID_INVITATION_NOTE", "邀请备注不能超过 200 字且不能包含空字符")
	}
	hours := in.ExpiresInHours
	if hours == 0 {
		hours = DefaultInvitationTTLHours
	}
	if hours < 1 || hours > MaxInvitationTTLHours {
		return InvitationIssued{}, fault.New(fault.Invalid, "INVALID_INVITATION_TTL", "邀请有效期必须在 1 到 720 小时之间")
	}
	token := ids.Secret()
	expiresAt := time.Now().UTC().Add(time.Duration(hours) * time.Hour)
	inv, err := i.store.CreateProjectInvitation(ctx, actor, project, ids.New(), in.Role, Digest(token), note, expiresAt)
	if err != nil {
		return InvitationIssued{}, err
	}
	return InvitationIssued{Invitation: inv, InviteToken: token}, nil
}

func (i *Invitations) Revoke(ctx context.Context, actor, project, invitationID string) (ProjectInvitation, error) {
	if _, machine := IdentityFromContext(ctx); machine {
		return ProjectInvitation{}, fault.New(fault.Forbidden, "MACHINE_FORBIDDEN", "机器主体无权执行此操作")
	}
	if !projectIDPattern.MatchString(project) {
		return ProjectInvitation{}, fault.New(fault.Invalid, "INVALID_PROJECT_ID", "项目标识格式不正确")
	}
	if !projectIDPattern.MatchString(invitationID) {
		return ProjectInvitation{}, fault.New(fault.Invalid, "INVALID_INVITATION_ID", "邀请标识格式不正确")
	}
	return i.store.RevokeProjectInvitation(ctx, actor, project, invitationID)
}

func (i *Invitations) Preview(ctx context.Context, token, remoteIP string) (InvitationPreview, error) {
	token = strings.TrimSpace(token)
	allowed, err := i.store.ReserveLogin(ctx, Digest("invite:"+token), Digest(remoteIP))
	if err != nil {
		return InvitationPreview{}, err
	}
	if !allowed {
		return InvitationPreview{}, fault.New(fault.Limited, "INVITATION_LIMITED", "请求过于频繁，请稍后重试")
	}
	if len(token) < 16 || len(token) > 128 {
		return InvitationPreview{}, invitationInvalid()
	}
	preview, err := i.store.PreviewInvitation(ctx, Digest(token))
	if errors.Is(err, ErrMissing) {
		return InvitationPreview{}, invitationInvalid()
	}
	return preview, err
}

func (i *Invitations) Accept(ctx context.Context, in InvitationAcceptInput) (InvitationAcceptResult, error) {
	token := strings.TrimSpace(in.Token)
	allowed, err := i.store.ReserveLogin(ctx, Digest("invite-accept:"+token), Digest(in.RemoteIP))
	if err != nil {
		return InvitationAcceptResult{}, err
	}
	if !allowed {
		return InvitationAcceptResult{}, fault.New(fault.Limited, "INVITATION_LIMITED", "请求过于频繁，请稍后重试")
	}
	if len(token) < 16 || len(token) > 128 {
		return InvitationAcceptResult{}, invitationInvalid()
	}
	if in.Session != nil {
		if in.Username != "" || in.DisplayName != "" || in.Password != "" {
			return InvitationAcceptResult{}, fault.New(fault.Invalid, "INVALID_INVITATION_ACCEPT", "已登录状态下接受邀请无需填写新账号信息")
		}
		res, err := i.store.AcceptInvitationExistingAccount(ctx, Digest(token), *in.Session)
		if errors.Is(err, ErrMissing) {
			return InvitationAcceptResult{}, invitationInvalid()
		}
		return res, err
	}
	username := strings.ToLower(strings.TrimSpace(in.Username))
	if !usernamePattern.MatchString(username) {
		return InvitationAcceptResult{}, fault.New(fault.Invalid, "INVALID_USERNAME", "用户名必须为 1 到 100 位 ASCII 字母、数字或 ._@-")
	}
	displayName, err := normalizeDisplayName(in.DisplayName)
	if err != nil {
		return InvitationAcceptResult{}, err
	}
	hash, err := PasswordHash(in.Password)
	if err != nil {
		return InvitationAcceptResult{}, err
	}
	userID := ids.New()
	secret := ids.Secret()
	session := Session{
		UserID:      userID,
		DisplayName: displayName,
		SecretHash:  Digest(secret),
		CSRF:        ids.Secret(),
		ExpiresAt:   time.Now().UTC().Add(8 * time.Hour),
	}
	res, err := i.store.AcceptInvitationNewAccount(ctx, Digest(token), userID, username, displayName, hash, session)
	if errors.Is(err, ErrMissing) {
		return InvitationAcceptResult{}, invitationInvalid()
	}
	if err != nil {
		return InvitationAcceptResult{}, err
	}
	res.SessionSecret = secret
	return res, nil
}

func normalizeDisplayName(raw string) (string, error) {
	cleaned := strings.TrimSpace(raw)
	if cleaned == "" || strings.ContainsRune(cleaned, 0) || !utf8.ValidString(cleaned) || len(cleaned) > 200 {
		return "", fault.New(fault.Invalid, "INVALID_DISPLAY_NAME", "显示名称必须为 1 到 200 字节的有效文本")
	}
	return cleaned, nil
}

func validRole(role string) bool {
	return role == RoleOwner || role == RoleEditor || role == RoleViewer
}

func invitationInvalid() error {
	return fault.New(fault.NotFound, "INVITATION_INVALID", "邀请链接无效、已使用、已撤销或已过期")
}
