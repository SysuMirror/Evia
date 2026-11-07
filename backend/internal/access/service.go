// Package access owns account, session and project authorization policy.
package access

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/howtomakeaname/Evia/backend/internal/platform/fault"
	"github.com/howtomakeaname/Evia/backend/internal/platform/ids"
	"golang.org/x/crypto/bcrypt"
)

var ErrMissing = errors.New("access record not found")
var usernamePattern = regexp.MustCompile(`^[a-zA-Z0-9_.@-]{1,100}$`)

type User struct {
	ID, Username, DisplayName, PasswordHash string
	Disabled                                bool
}
type Session struct {
	UserID, DisplayName, SecretHash, CSRF string
	ExpiresAt                             time.Time
}
type Project struct{ ID, Name, Timezone, Role string }
type Store interface {
	ReserveLogin(context.Context, string, string) (bool, error)
	FindUser(context.Context, string) (User, error)
	InsertSession(context.Context, Session) error
	RotateSession(context.Context, string, Session) error
	FindSession(context.Context, string) (Session, error)
	DeleteSession(context.Context, string) error
	CreateProject(context.Context, string, Project) error
	ListProjects(context.Context, string, string, int) ([]Project, error)
	ProjectRole(context.Context, string, string) (string, error)
}
type Service struct {
	store Store
	dummy []byte
}

func New(store Store) (*Service, error) {
	dummy, e := bcrypt.GenerateFromPassword([]byte(ids.Secret()), 12)
	if e != nil {
		return nil, e
	}
	return &Service{store, dummy}, nil
}
func Digest(secret string) string {
	sum := sha256.Sum256([]byte(secret))
	return hex.EncodeToString(sum[:])
}
func PasswordHash(password string) (string, error) {
	if len(password) < 12 || len(password) > 72 {
		return "", fault.New(fault.Invalid, "INVALID_PASSWORD", "密码必须为 12 到 72 字节")
	}
	hash, e := bcrypt.GenerateFromPassword([]byte(password), 12)
	return string(hash), e
}
func (s *Service) Login(ctx context.Context, username, password, remoteIP string, prior *Session) (Session, string, error) {
	username = strings.ToLower(strings.TrimSpace(username))
	denied := fault.New(fault.Unauthenticated, "INVALID_CREDENTIALS", "账号或密码不正确")
	if !usernamePattern.MatchString(username) || len(password) == 0 || len(password) > 72 {
		return Session{}, "", denied
	}
	allowed, e := s.store.ReserveLogin(ctx, Digest(username), Digest(remoteIP))
	if e != nil {
		return Session{}, "", e
	}
	if !allowed {
		return Session{}, "", fault.New(fault.Limited, "LOGIN_LIMITED", "登录尝试过于频繁，请稍后重试")
	}
	user, e := s.store.FindUser(ctx, username)
	if e != nil && !errors.Is(e, ErrMissing) {
		return Session{}, "", e
	}
	hash := []byte(user.PasswordHash)
	if errors.Is(e, ErrMissing) {
		hash = s.dummy
	}
	matches := bcrypt.CompareHashAndPassword(hash, []byte(password)) == nil
	if !matches || e != nil || user.Disabled {
		return Session{}, "", denied
	}
	secret := ids.Secret()
	session := Session{UserID: user.ID, DisplayName: user.DisplayName, SecretHash: Digest(secret), CSRF: ids.Secret(), ExpiresAt: time.Now().UTC().Add(8 * time.Hour)}
	if prior == nil {
		e = s.store.InsertSession(ctx, session)
	} else {
		e = s.store.RotateSession(ctx, prior.SecretHash, session)
	}
	if errors.Is(e, ErrMissing) {
		return Session{}, "", fault.New(fault.Unauthenticated, "SESSION_EXPIRED", "登录已过期，请重新登录")
	}
	if e != nil {
		return Session{}, "", e
	}
	return session, secret, nil
}
func (s *Service) Authenticate(ctx context.Context, secret string) (Session, error) {
	if len(secret) != 43 {
		return Session{}, fault.New(fault.Unauthenticated, "SESSION_REQUIRED", "请先登录")
	}
	session, e := s.store.FindSession(ctx, Digest(secret))
	if errors.Is(e, ErrMissing) || e == nil && !session.ExpiresAt.After(time.Now()) {
		return Session{}, fault.New(fault.Unauthenticated, "SESSION_EXPIRED", "登录已过期，请重新登录")
	}
	return session, e
}
func (s *Service) Logout(ctx context.Context, session Session) error {
	return s.store.DeleteSession(ctx, session.SecretHash)
}
func (s *Service) CreateProject(ctx context.Context, userID, name, timezone string) (Project, error) {
	name = strings.TrimSpace(name)
	if name == "" || strings.ContainsRune(name, 0) || !utf8.ValidString(name) || utf8.RuneCountInString(name) > 100 {
		return Project{}, fault.New(fault.Invalid, "INVALID_PROJECT_NAME", "项目名称必须为 1 到 100 个字符")
	}
	if timezone == "" || len(timezone) > 100 || timezone == "Local" {
		return Project{}, fault.New(fault.Invalid, "INVALID_TIMEZONE", "请选择有效的 IANA 时区")
	}
	if _, e := time.LoadLocation(timezone); e != nil {
		return Project{}, fault.New(fault.Invalid, "INVALID_TIMEZONE", "请选择有效的 IANA 时区")
	}
	project := Project{ID: ids.New(), Name: name, Timezone: timezone, Role: "owner"}
	if e := s.store.CreateProject(ctx, userID, project); e != nil {
		return Project{}, e
	}
	return project, nil
}
func (s *Service) ListProjects(ctx context.Context, userID, after string, limit int) ([]Project, error) {
	if limit < 1 || limit > 200 {
		return nil, fault.New(fault.Invalid, "INVALID_LIMIT", "分页大小超出范围")
	}
	return s.store.ListProjects(ctx, userID, after, limit+1)
}
func (s *Service) Authorize(ctx context.Context, userID, projectID string, write bool) error {
	role, e := s.store.ProjectRole(ctx, userID, projectID)
	if errors.Is(e, ErrMissing) {
		return fault.New(fault.NotFound, "PROJECT_NOT_FOUND", "项目不存在或不可见")
	}
	if e != nil {
		return e
	}
	if write && role != "owner" && role != "editor" {
		return fault.New(fault.Forbidden, "PROJECT_FORBIDDEN", "当前角色不能修改此项目")
	}
	return nil
}
