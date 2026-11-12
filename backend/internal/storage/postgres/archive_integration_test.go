//go:build integration

package postgres

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/howtomakeaname/Evia/backend/internal/archiving"
	"github.com/howtomakeaname/Evia/backend/internal/platform/ids"
)

func archiveManifestFixture(f catalogFixture, status string) archiving.Manifest {
	now := time.Now().UTC()
	m := archiving.Manifest{
		ManifestID:    ids.New(),
		ProjectID:     f.project,
		Environment:   "development",
		RetentionDays: 90,
		Cutoff:        now.AddDate(0, 0, -90),
		RowCount:      12,
		Status:        archiving.StatusPending,
		CreatedAt:     now,
	}
	sweep := ids.New()
	m.SweepID = &sweep
	return m
}

func TestArchiveManifestLifecycleAndIntegrityColumns(t *testing.T) {
	f := catalogDB(t)
	ctx := context.Background()
	m := archiveManifestFixture(f, "")
	if e := f.s.CreateManifest(ctx, m); e != nil {
		t.Fatal(e)
	}
	window := archiving.FactWindow{
		Rows:        12,
		WindowStart: time.Now().Add(-90 * 24 * time.Hour).UTC(),
		WindowEnd:   time.Now().Add(-89 * 24 * time.Hour).UTC(),
	}
	artifact := archiving.Artifact{
		Path:   "/var/lib/evia/archives/" + m.ManifestID + ".jsonl",
		SHA256: strings.Repeat("a", 64),
		Bytes:  2048,
	}
	if e := f.s.MarkSucceeded(ctx, m.ManifestID, artifact, window, 12, time.Now().UTC()); e != nil {
		t.Fatal(e)
	}
	got, e := f.s.GetManifest(ctx, f.owner, f.project, m.ManifestID)
	if e != nil {
		t.Fatal(e)
	}
	if got.Status != archiving.StatusMaterialized || got.ArtifactPath == nil || *got.ArtifactSHA != artifact.SHA256 ||
		got.WindowStart == nil || got.WindowEnd == nil || got.SweepID == nil || got.RowCount != 12 {
		t.Fatalf("materialized manifest wrong: %+v", got)
	}
	// A second terminal transition is rejected by the pending guard.
	if e = f.s.MarkSucceeded(ctx, m.ManifestID, artifact, window, 12, time.Now().UTC()); e == nil {
		t.Fatal("double materialization must be rejected")
	}
}

func TestArchiveManifestFailureIsIdempotent(t *testing.T) {
	f := catalogDB(t)
	ctx := context.Background()
	m := archiveManifestFixture(f, "")
	if e := f.s.CreateManifest(ctx, m); e != nil {
		t.Fatal(e)
	}
	if e := f.s.MarkFailed(ctx, m.ManifestID, "ARCHIVE_ARTIFACT_WRITE_FAILED", time.Now().UTC()); e != nil {
		t.Fatal(e)
	}
	// Retrying the same failure stays idempotent instead of surfacing a conflict.
	if e := f.s.MarkFailed(ctx, m.ManifestID, "ARCHIVE_ARTIFACT_WRITE_FAILED", time.Now().UTC()); e != nil {
		t.Fatalf("idempotent failure retry errored: %v", e)
	}
	got, e := f.s.GetManifest(ctx, f.owner, f.project, m.ManifestID)
	if e != nil || got.Status != archiving.StatusFailed || got.ErrorCode == nil ||
		*got.ErrorCode != "ARCHIVE_ARTIFACT_WRITE_FAILED" || got.ArtifactPath != nil {
		t.Fatalf("failed manifest wrong: %+v err=%v", got, e)
	}
}

func TestArchiveManifestListFilteringAndAuthority(t *testing.T) {
	f := catalogDB(t)
	ctx := context.Background()
	for i := 0; i < 3; i++ {
		m := archiveManifestFixture(f, "")
		m.ManifestID = ids.New()
		if i == 2 {
			m.Environment = "staging"
		}
		if e := f.s.CreateManifest(ctx, m); e != nil {
			t.Fatal(e)
		}
	}
	dev, e := f.s.ListManifests(ctx, f.owner, f.project, archiving.ListFilter{Limit: 10, Environment: "development"})
	if e != nil || len(dev.Items) != 2 {
		t.Fatalf("environment filter wrong: %+v err=%v", dev, e)
	}
	all, e := f.s.ListManifests(ctx, f.owner, f.project, archiving.ListFilter{Limit: 2})
	if e != nil || len(all.Items) != 2 || all.After == "" {
		t.Fatalf("pagination wrong: %+v err=%v", all, e)
	}
	next, e := f.s.ListManifests(ctx, f.owner, f.project, archiving.ListFilter{Limit: 2, After: all.After})
	if e != nil || len(next.Items) != 1 {
		t.Fatalf("cursor page wrong: %+v err=%v", next, e)
	}
	if _, e = f.s.ListManifests(ctx, f.viewer, f.project, archiving.ListFilter{Limit: 10}); e == nil {
		t.Fatal("viewer must not read archive manifests")
	}
	if _, e = f.s.ListManifests(ctx, f.outsider, f.project, archiving.ListFilter{Limit: 10}); e == nil {
		t.Fatal("outsider must not read archive manifests")
	}
}

func TestArchiveManifestRejectsInvalidInputs(t *testing.T) {
	f := catalogDB(t)
	ctx := context.Background()
	if _, e := f.s.GetManifest(ctx, f.owner, f.project, "not-a-uuid"); e == nil {
		t.Fatal("malformed id must be rejected")
	}
	if _, e := f.s.ListManifests(ctx, f.owner, f.project, archiving.ListFilter{Limit: 0}); e == nil {
		t.Fatal("invalid limit must be rejected")
	}
	if _, e := f.s.ListManifests(ctx, f.owner, f.project, archiving.ListFilter{Limit: 10, Environment: "bogus"}); e == nil {
		t.Fatal("invalid environment must be rejected")
	}
	m := archiveManifestFixture(f, "")
	m.Status = archiving.StatusMaterialized
	if e := f.s.CreateManifest(ctx, m); e == nil {
		t.Fatal("non-pending create must be rejected")
	}
}
