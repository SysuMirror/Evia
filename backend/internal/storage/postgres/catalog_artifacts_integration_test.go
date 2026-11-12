//go:build integration

package postgres

import (
	"context"
	"strings"
	"testing"

	"github.com/howtomakeaname/Evia/backend/internal/artifacts"
)

func TestGetReleaseManifestFeedsTypeScriptGenerator(t *testing.T) {
	f := catalogDB(t)
	ctx := context.Background()
	draft := f.event(t, "lesson_opened")
	release, e := f.svc.CreateRelease(ctx, f.owner, f.project, "artifact-key-1", releaseInput("lesson_opened", draft.Revision))
	if e != nil {
		t.Fatal(e)
	}

	plan, e := f.s.GetReleaseManifest(ctx, f.owner, f.project, release.ID)
	if e != nil {
		t.Fatal(e)
	}
	if plan.ReleaseID != release.ID || plan.Revision != release.Revision || plan.Environment != "development" || plan.ManifestHash != release.ManifestHash {
		t.Fatalf("plan metadata wrong: %+v vs %+v", plan, release)
	}
	if len(plan.Events) != 1 {
		t.Fatalf("expected one manifest event, got %d", len(plan.Events))
	}
	event := plan.Events[0]
	if event.Key != "lesson_opened" || event.SchemaVersion != 1 || event.Schema["type"] != "object" {
		t.Fatalf("manifest event wrong: %+v", event)
	}

	artifact, e := artifacts.Generate(plan)
	if e != nil {
		t.Fatal(e)
	}
	if artifact.EventCount != 1 || artifact.ByteSize != len(artifact.Source) || len(artifact.Checksum) != 64 {
		t.Fatalf("artifact metadata wrong: %+v", artifact)
	}
	if !strings.Contains(artifact.Source, "export interface LessonOpenedProperties {") ||
		!strings.Contains(artifact.Source, "  lesson_id: number;") {
		t.Fatalf("generated source missing expected declarations:\n%s", artifact.Source)
	}
}

func TestGetReleaseManifestAuthorityAndMissingRelease(t *testing.T) {
	f := catalogDB(t)
	ctx := context.Background()
	draft := f.event(t, "lesson_opened")
	release, e := f.svc.CreateRelease(ctx, f.owner, f.project, "artifact-key-2", releaseInput("lesson_opened", draft.Revision))
	if e != nil {
		t.Fatal(e)
	}

	// Release read is a member-level permission, so a viewer can read the artifact.
	if plan, e := f.s.GetReleaseManifest(ctx, f.viewer, f.project, release.ID); e != nil || len(plan.Events) != 1 {
		t.Fatalf("viewer must be able to read the release manifest: %+v err=%v", plan, e)
	}
	if _, e = f.s.GetReleaseManifest(ctx, f.outsider, f.project, release.ID); e == nil || faultPublicCode(e) != "PROJECT_NOT_FOUND" {
		t.Fatalf("outsider must see an invisible project, got %v", e)
	}
	if _, e = f.s.GetReleaseManifest(ctx, f.owner, f.project, "99999999-9999-4999-8999-999999999999"); e == nil || faultPublicCode(e) != "RELEASE_NOT_FOUND" {
		t.Fatalf("unknown release must be missing, got %v", e)
	}
	if _, e = f.s.GetReleaseManifest(ctx, f.owner, f.project, "not-a-uuid"); e == nil {
		t.Fatal("malformed release id must be rejected")
	}
}
