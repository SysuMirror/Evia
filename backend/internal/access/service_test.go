package access

import (
	"context"
	"errors"
	"github.com/howtomakeaname/Evia/backend/internal/platform/fault"
	"testing"
	"time"
)

type fakeStore struct {
	Store
	user                 User
	userErr              error
	allowed              bool
	session              Session
	project              Project
	role                 string
	roleErr              error
	insertErr, rotateErr error
	rotatedFrom          string
}

func (f *fakeStore) ReserveLogin(context.Context, string, string) (bool, error) {
	return f.allowed, nil
}
func (f *fakeStore) FindUser(context.Context, string) (User, error) { return f.user, f.userErr }
func (f *fakeStore) InsertSession(_ context.Context, s Session) error {
	f.session = s
	return f.insertErr
}
func (f *fakeStore) RotateSession(_ context.Context, prior string, s Session) error {
	f.rotatedFrom, f.session = prior, s
	return f.rotateErr
}
func (f *fakeStore) FindSession(context.Context, string) (Session, error) { return f.session, nil }
func (f *fakeStore) CreateProject(_ context.Context, _ string, p Project) error {
	f.project = p
	return nil
}
func (f *fakeStore) ProjectRole(context.Context, string, string) (string, error) {
	return f.role, f.roleErr
}
func code(t *testing.T, e error, want string) {
	t.Helper()
	if e == nil || fault.Public(e).Code != want {
		t.Fatalf("wanted %s, got %v", want, e)
	}
}

func TestLoginAndSessionBoundary(t *testing.T) {
	hash, e := PasswordHash("synthetic-password-42")
	if e != nil {
		t.Fatal(e)
	}
	f := &fakeStore{allowed: true, user: User{ID: "u", DisplayName: "Example", PasswordHash: hash}}
	s, e := New(f)
	if e != nil {
		t.Fatal(e)
	}
	ctx := context.Background()
	session, secret, e := s.Login(ctx, "example", "synthetic-password-42", "127.0.0.1", nil)
	if e != nil || session.UserID != "u" || len(secret) != 43 || f.session.SecretHash == secret || session.CSRF == secret {
		t.Fatal("invalid session", e)
	}
	if f.session.SecretHash != Digest(secret) {
		t.Fatal("session secret not hashed")
	}
	_, _, e = s.Login(ctx, "example", "wrong", "127.0.0.1", nil)
	code(t, e, "INVALID_CREDENTIALS")
	f.userErr = ErrMissing
	_, _, e = s.Login(ctx, "missing", "wrong", "127.0.0.1", nil)
	code(t, e, "INVALID_CREDENTIALS")
	f.userErr = nil
	f.user.Disabled = true
	_, _, e = s.Login(ctx, "example", "synthetic-password-42", "127.0.0.1", nil)
	code(t, e, "INVALID_CREDENTIALS")
	f.allowed = false
	_, _, e = s.Login(ctx, "example", "synthetic-password-42", "127.0.0.1", nil)
	code(t, e, "LOGIN_LIMITED")
	f.session.ExpiresAt = time.Now().Add(-time.Second)
	_, e = s.Authenticate(ctx, secret)
	code(t, e, "SESSION_EXPIRED")
	_, e = s.Authenticate(ctx, "bad")
	code(t, e, "SESSION_REQUIRED")
}

func TestLoginRotationRequiresAtomicStorageSuccess(t *testing.T) {
	hash, e := PasswordHash("synthetic-password-42")
	if e != nil {
		t.Fatal(e)
	}
	f := &fakeStore{allowed: true, user: User{ID: "u", PasswordHash: hash}}
	s, e := New(f)
	if e != nil {
		t.Fatal(e)
	}
	prior := &Session{SecretHash: "prior-hash"}
	for _, tc := range []struct {
		name string
		err  error
		code string
	}{
		{"success", nil, ""},
		{"concurrent revocation", ErrMissing, "SESSION_EXPIRED"},
		{"storage failure", errors.New("replacement transaction failed"), "INTERNAL_ERROR"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f.rotateErr = tc.err
			v, secret, e := s.Login(context.Background(), "example", "synthetic-password-42", "127.0.0.1", prior)
			if f.rotatedFrom != prior.SecretHash {
				t.Fatal("prior credential was not passed to atomic rotation")
			}
			if tc.code == "" {
				if e != nil || secret == "" || v.SecretHash != Digest(secret) {
					t.Fatal("rotation failed", e)
				}
			} else {
				code(t, e, tc.code)
				if secret != "" || v.SecretHash != "" {
					t.Fatal("failed rotation disclosed a credential")
				}
			}
		})
	}
}
func TestProjectPolicyAndAuthorization(t *testing.T) {
	f := &fakeStore{role: "viewer"}
	s := &Service{store: f}
	ctx := context.Background()
	for _, v := range []struct{ name, zone, code string }{{" ", "UTC", "INVALID_PROJECT_NAME"}, {"valid\x00suffix", "UTC", "INVALID_PROJECT_NAME"}, {"论坛", "", "INVALID_TIMEZONE"}, {"论坛", "Local", "INVALID_TIMEZONE"}, {"论坛", "invalid/zone", "INVALID_TIMEZONE"}} {
		_, e := s.CreateProject(ctx, "u", v.name, v.zone)
		code(t, e, v.code)
	}
	p, e := s.CreateProject(ctx, "u", "  论坛  ", "Asia/Shanghai")
	if e != nil || p.Name != "论坛" || p.Role != "owner" {
		t.Fatal(p, e)
	}
	if e = s.Authorize(ctx, "u", p.ID, false); e != nil {
		t.Fatal(e)
	}
	code(t, s.Authorize(ctx, "u", p.ID, true), "PROJECT_FORBIDDEN")
	f.role = "editor"
	if e = s.Authorize(ctx, "u", p.ID, true); e != nil {
		t.Fatal(e)
	}
	f.roleErr = ErrMissing
	code(t, s.Authorize(ctx, "outsider", p.ID, false), "PROJECT_NOT_FOUND")
	f.roleErr = errors.New("database unavailable")
	code(t, s.Authorize(ctx, "u", p.ID, false), "INTERNAL_ERROR")
}
