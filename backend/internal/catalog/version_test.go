package catalog

import (
	"context"
	"github.com/howtomakeaname/Evia/backend/internal/platform/fault"
	"testing"
)

type versionStore struct {
	Store
	calls int
}

func (s *versionStore) GetEventVersion(_ context.Context, actor, project, key string, version int64) (EventVersion, error) {
	s.calls++
	return EventVersion{Key: key, SchemaVersion: version}, nil
}
func TestEventVersionValidatesVersionAndKey(t *testing.T) {
	store := &versionStore{}
	svc := New(store)
	for _, tc := range []struct {
		key     string
		version int64
		code    string
	}{{"opened", 0, "INVALID_SCHEMA_VERSION"}, {"opened", MaxInteger + 1, "INVALID_SCHEMA_VERSION"}, {"Invalid", 1, "INVALID_EVENT_KEY"}} {
		_, e := svc.GetEventVersion(context.Background(), "actor", "project", tc.key, tc.version)
		if fault.Public(e).Code != tc.code || store.calls != 0 {
			t.Fatal(e, store.calls)
		}
	}
	out, e := svc.GetEventVersion(context.Background(), "actor", "project", "opened", MaxInteger)
	if e != nil || out.SchemaVersion != MaxInteger || store.calls != 1 {
		t.Fatal(out, e)
	}
}
