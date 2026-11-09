package access

import (
	"context"
	"strings"
	"testing"

	"github.com/howtomakeaname/Evia/backend/internal/platform/fault"
)

type settingsMemory struct {
	calls int
	in    SettingsUpdate
	hash  string
}

func (m *settingsMemory) GetProjectSettings(_ context.Context, _, project string) (ProjectSettings, error) {
	m.calls++
	return ProjectSettings{ProjectID: project, Name: "Example", Timezone: "UTC", Revision: 1}, nil
}
func (m *settingsMemory) UpdateProjectSettings(_ context.Context, _, project string, revision int64, _, hash string, in SettingsUpdate) (ProjectSettings, error) {
	m.calls++
	m.in, m.hash = in, hash
	return ProjectSettings{ProjectID: project, Name: in.Name, Timezone: in.Timezone, Revision: revision + 1}, nil
}

func TestProjectSettingsValidation(t *testing.T) {
	actor, project := "00000000-0000-4000-8000-000000000001", "00000000-0000-4000-8000-000000000002"
	tests := []struct {
		name      string
		input     SettingsUpdate
		revision  int64
		key, code string
	}{
		{"empty name", SettingsUpdate{" \n ", "UTC"}, 1, "key", "INVALID_PROJECT_NAME"},
		{"long Unicode name", SettingsUpdate{strings.Repeat("界", 101), "UTC"}, 1, "key", "INVALID_PROJECT_NAME"},
		{"NUL name", SettingsUpdate{"a\x00b", "UTC"}, 1, "key", "INVALID_PROJECT_NAME"},
		{"invalid UTF8", SettingsUpdate{string([]byte{0xff}), "UTC"}, 1, "key", "INVALID_PROJECT_NAME"},
		{"missing timezone", SettingsUpdate{"Valid", ""}, 1, "key", "INVALID_TIMEZONE"},
		{"host timezone", SettingsUpdate{"Valid", "Local"}, 1, "key", "INVALID_TIMEZONE"},
		{"unknown timezone", SettingsUpdate{"Valid", "Unknown/Zone"}, 1, "key", "INVALID_TIMEZONE"},
		{"timezone whitespace", SettingsUpdate{"Valid", " UTC "}, 1, "key", "INVALID_TIMEZONE"},
		{"NUL timezone", SettingsUpdate{"Valid", "UTC\x00"}, 1, "key", "INVALID_TIMEZONE"},
		{"long timezone", SettingsUpdate{"Valid", strings.Repeat("a", 101)}, 1, "key", "INVALID_TIMEZONE"},
		{"missing revision", SettingsUpdate{"Valid", "UTC"}, 0, "key", "REVISION_REQUIRED"},
		{"unsafe revision", SettingsUpdate{"Valid", "UTC"}, 9007199254740992, "key", "REVISION_REQUIRED"},
		{"missing key", SettingsUpdate{"Valid", "UTC"}, 1, "", "INVALID_IDEMPOTENCY_KEY"},
		{"long key", SettingsUpdate{"Valid", "UTC"}, 1, strings.Repeat("x", 129), "INVALID_IDEMPOTENCY_KEY"},
		{"key spaces", SettingsUpdate{"Valid", "UTC"}, 1, "a b", "INVALID_IDEMPOTENCY_KEY"},
		{"key control", SettingsUpdate{"Valid", "UTC"}, 1, "x\x7f", "INVALID_IDEMPOTENCY_KEY"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			store := &settingsMemory{}
			_, e := NewSettings(store).Update(context.Background(), actor, project, tc.revision, tc.key, tc.input)
			if e == nil || fault.Public(e).Code != tc.code || store.calls != 0 {
				t.Fatal("invalid settings reached storage", e, store.calls)
			}
		})
	}
	store := &settingsMemory{}
	service := NewSettings(store)
	name := strings.Repeat("界", 100)
	out, e := service.Update(context.Background(), actor, project, 1, "first", SettingsUpdate{" \t" + name + "\n", "Asia/Shanghai"})
	if e != nil || out.Name != name || store.in.Timezone != "Asia/Shanghai" || len(store.hash) != 64 {
		t.Fatal("valid Unicode settings rejected", out, e)
	}
	hash := store.hash
	if _, e = service.Update(context.Background(), actor, project, 1, "second", SettingsUpdate{name, "Asia/Shanghai"}); e != nil || store.hash != hash {
		t.Fatal("canonical request varied with key or trimmed whitespace", e)
	}
	if _, e = service.Update(context.Background(), actor, project, 2, "second", SettingsUpdate{name, "Asia/Shanghai"}); e != nil || store.hash == hash {
		t.Fatal("revision omitted from request identity", e)
	}
}

func TestProjectSettingsHumanBoundary(t *testing.T) {
	actor, project := "00000000-0000-4000-8000-000000000001", "00000000-0000-4000-8000-000000000002"
	for _, identity := range []Identity{{}, {PrincipalID: actor}, {CredentialID: project}} {
		store := &settingsMemory{}
		service := NewSettings(store)
		ctx := WithIdentity(context.Background(), identity)
		if _, e := service.Get(ctx, actor, project); e == nil || fault.Public(e).Code != "MACHINE_FORBIDDEN" {
			t.Fatal(e)
		}
		if _, e := service.Update(ctx, actor, project, 1, "key", SettingsUpdate{"Name", "UTC"}); e == nil || fault.Public(e).Code != "MACHINE_FORBIDDEN" {
			t.Fatal(e)
		}
		if store.calls != 0 {
			t.Fatal("machine reached settings storage")
		}
	}
	store := &settingsMemory{}
	service := NewSettings(store)
	if _, e := service.Get(context.Background(), "", project); e == nil || fault.Public(e).Code != "SESSION_REQUIRED" {
		t.Fatal(e)
	}
	if _, e := service.Get(context.Background(), actor, "invalid"); e == nil || fault.Public(e).Code != "INVALID_PROJECT_ID" {
		t.Fatal(e)
	}
	if _, e := service.Update(context.Background(), actor, "invalid", 1, "key", SettingsUpdate{"Name", "UTC"}); e == nil || fault.Public(e).Code != "INVALID_PROJECT_ID" {
		t.Fatal(e)
	}
	if store.calls != 0 {
		t.Fatal("invalid scope reached settings storage")
	}
	if _, e := service.Get(context.Background(), actor, project); e != nil || store.calls != 1 {
		t.Fatal(e, store.calls)
	}
}
