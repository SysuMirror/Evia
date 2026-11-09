package access

import (
	"context"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/howtomakeaname/Evia/backend/internal/platform/canonical"
	"github.com/howtomakeaname/Evia/backend/internal/platform/fault"
)

// ProjectSettings contains the default timezone for future queries and views.
// Changing it never rewrites facts, published versions, or explicit query timezones.
type ProjectSettings struct {
	ProjectID, Name, Timezone string
	Revision                  int64
}

// SettingsUpdate replaces both mutable settings. Each newly accepted write,
// including an unchanged replacement, advances the settings revision once.
type SettingsUpdate struct{ Name, Timezone string }

// SettingsStore reauthorizes active human membership inside each transaction.
// Updates require a current owner before checking durable idempotency results.
type SettingsStore interface {
	GetProjectSettings(context.Context, string, string) (ProjectSettings, error)
	UpdateProjectSettings(context.Context, string, string, int64, string, string, SettingsUpdate) (ProjectSettings, error)
}

type Settings struct{ store SettingsStore }

func NewSettings(store SettingsStore) *Settings { return &Settings{store: store} }

func settingsCaller(ctx context.Context, actor, project string) error {
	if _, machine := IdentityFromContext(ctx); machine {
		return fault.New(fault.Forbidden, "MACHINE_FORBIDDEN", "机器主体无权执行此操作")
	}
	if !projectIDPattern.MatchString(actor) {
		return fault.New(fault.Unauthenticated, "SESSION_REQUIRED", "请先登录")
	}
	if !projectIDPattern.MatchString(project) {
		return fault.New(fault.Invalid, "INVALID_PROJECT_ID", "项目标识格式不正确")
	}
	return nil
}

func (s *Settings) Get(ctx context.Context, actor, project string) (ProjectSettings, error) {
	if e := settingsCaller(ctx, actor, project); e != nil {
		return ProjectSettings{}, e
	}
	return s.store.GetProjectSettings(ctx, actor, project)
}

func (s *Settings) Update(ctx context.Context, actor, project string, revision int64, key string, in SettingsUpdate) (ProjectSettings, error) {
	if e := settingsCaller(ctx, actor, project); e != nil {
		return ProjectSettings{}, e
	}
	in.Name = strings.TrimSpace(in.Name)
	if in.Name == "" || strings.ContainsRune(in.Name, 0) || !utf8.ValidString(in.Name) || utf8.RuneCountInString(in.Name) > 100 {
		return ProjectSettings{}, fault.New(fault.Invalid, "INVALID_PROJECT_NAME", "项目名称必须为 1 到 100 个字符")
	}
	if in.Timezone == "" || len(in.Timezone) > 100 || in.Timezone == "Local" {
		return ProjectSettings{}, fault.New(fault.Invalid, "INVALID_TIMEZONE", "请选择有效的 IANA 时区")
	}
	if _, e := time.LoadLocation(in.Timezone); e != nil {
		return ProjectSettings{}, fault.New(fault.Invalid, "INVALID_TIMEZONE", "请选择有效的 IANA 时区")
	}
	if revision < 1 || revision > 9007199254740991 {
		return ProjectSettings{}, fault.New(fault.Invalid, "REVISION_REQUIRED", "必须提供有效修订")
	}
	if e := machineKey(key); e != nil {
		return ProjectSettings{}, e
	}
	hash, e := canonical.Hash(struct {
		Revision int64
		Input    SettingsUpdate
	}{revision, in})
	if e != nil {
		return ProjectSettings{}, e
	}
	return s.store.UpdateProjectSettings(ctx, actor, project, revision, key, hash, in)
}
