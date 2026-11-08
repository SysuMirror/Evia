//go:build integration

package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"github.com/howtomakeaname/Evia/backend/internal/access"
	"github.com/howtomakeaname/Evia/backend/internal/platform/ids"
)

func TestConsoleReadsAuthorizationEmptyActivationAndImmutableVersions(t *testing.T) {
	f := catalogDB(t)
	ctx := context.Background()
	for _, actor := range []string{f.owner, f.viewer} {
		p, e := f.s.GetProject(ctx, actor, f.project)
		if e != nil || p.ID != f.project || p.Role == "" {
			t.Fatal(p, e)
		}
		for _, env := range []string{"development", "staging", "production"} {
			active, e := f.s.GetActivation(ctx, actor, f.project, env)
			if e != nil || active.Environment != env || active.ActiveReleaseRevision != 0 {
				t.Fatal(active, e)
			}
		}
	}
	for _, project := range []string{f.project, ids.New()} {
		if _, e := f.s.GetProject(ctx, f.outsider, project); !errors.Is(e, access.ErrMissing) {
			t.Fatal("project leaked", e)
		}
		_, e := f.s.GetActivation(ctx, f.outsider, project, "development")
		faultCode(t, e, "PROJECT_NOT_FOUND")
	}
	d := f.event(t, "lesson_opened")
	r, e := f.svc.CreateRelease(ctx, f.owner, f.project, "console-release", releaseInput("lesson_opened", d.Revision))
	if e != nil {
		t.Fatal(e)
	}
	version, e := f.svc.GetEventVersion(ctx, f.viewer, f.project, "lesson_opened", 1)
	expectedJSON, _ := json.Marshal(d.Definition)
	actualJSON, _ := json.Marshal(version.Definition)
	if e != nil || version.DraftRevision != d.Revision || string(expectedJSON) != string(actualJSON) {
		t.Fatal(version, e)
	}
	_, e = f.svc.GetEventVersion(ctx, f.outsider, f.project, "lesson_opened", 1)
	faultCode(t, e, "PROJECT_NOT_FOUND")
	_, e = f.svc.GetEventVersion(ctx, f.owner, f.otherProject, "lesson_opened", 1)
	faultCode(t, e, "EVENT_VERSION_NOT_FOUND")
	_, e = f.svc.GetEventVersion(ctx, f.owner, f.project, "lesson_opened", 2)
	faultCode(t, e, "EVENT_VERSION_NOT_FOUND")
	updated := d.Definition
	label := "Changed after publication"
	updated.DisplayName = &label
	if _, e = f.svc.SaveDraft(ctx, f.owner, f.project, "lesson_opened", d.Revision, updated); e != nil {
		t.Fatal(e)
	}
	frozen, e := f.svc.GetEventVersion(ctx, f.owner, f.project, "lesson_opened", 1)
	if e != nil || !reflect.DeepEqual(frozen, version) {
		t.Fatal("published version changed with draft", frozen, e)
	}
	svc := deliveryService(t, f, &policyProbe{})
	active, e := svc.GetActivation(ctx, f.viewer, f.project, "development")
	if e != nil || active.ActiveReleaseRevision != 0 {
		t.Fatal(active, e)
	}
	if _, e = svc.Activate(ctx, f.owner, f.project, "console-activate", activationInput(r, 0)); e != nil {
		t.Fatal(e)
	}
	active, e = svc.GetActivation(ctx, f.viewer, f.project, "development")
	if e != nil || active.ActiveReleaseRevision != r.Revision {
		t.Fatal(active, e)
	}
	_, e = svc.Activate(ctx, f.owner, f.project, "console-stale", activationInput(r, 0))
	faultCode(t, e, "ACTIVE_REVISION_CONFLICT")
	if _, e = f.s.Pool.Exec(ctx, `DELETE FROM project_members WHERE project_id=$1 AND user_id=$2`, f.project, f.viewer); e != nil {
		t.Fatal(e)
	}
	if _, e = f.s.GetProject(ctx, f.viewer, f.project); !errors.Is(e, access.ErrMissing) {
		t.Fatal("revoked project visible", e)
	}
	_, e = svc.GetActivation(ctx, f.viewer, f.project, "development")
	faultCode(t, e, "PROJECT_NOT_FOUND")
	_, e = f.svc.GetEventVersion(ctx, f.viewer, f.project, "lesson_opened", 1)
	faultCode(t, e, "PROJECT_NOT_FOUND")
	if _, e = f.s.Pool.Exec(ctx, `UPDATE accounts SET disabled=true WHERE id=$1`, f.owner); e != nil {
		t.Fatal(e)
	}
	if _, e = f.s.GetProject(ctx, f.owner, f.project); !errors.Is(e, access.ErrMissing) {
		t.Fatal("disabled account project visible", e)
	}
	_, e = svc.GetActivation(ctx, f.owner, f.project, "development")
	faultCode(t, e, "PROJECT_NOT_FOUND")
	_, e = f.svc.GetEventVersion(ctx, f.owner, f.project, "lesson_opened", 1)
	faultCode(t, e, "PROJECT_NOT_FOUND")
}
