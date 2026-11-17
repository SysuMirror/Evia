//go:build integration

package postgres

import (
	"context"
	"testing"

	"github.com/howtomakeaname/Evia/backend/internal/platform/ids"
	"github.com/howtomakeaname/Evia/backend/internal/reporting"
)

func TestDashboardCopyCreatesIndependentDraft(t *testing.T) {
	f, d, _ := dashboardFixture(t, 2)
	dashboardPublish(t, f, d)
	ctx := context.Background()

	copy1, e := f.s.CopyDashboard(ctx, f.owner, f.project, d.ID, "copy-key", reporting.DashboardCopy{Title: "Copied dashboard"})
	if e != nil {
		t.Fatal(e)
	}
	if copy1.ID == d.ID {
		t.Fatal("copy must receive a fresh dashboard ID")
	}
	if copy1.DraftRevision != 1 || copy1.PublishedVersion != 0 {
		t.Fatalf("copy must start as an unpublished draft: %+v", copy1)
	}
	if copy1.Title != "Copied dashboard" || copy1.Environment != d.Environment {
		t.Fatalf("copy title/environment mismatch: %+v", copy1)
	}
	if len(copy1.Definition.Widgets) != 2 {
		t.Fatalf("expected two widgets, got %d", len(copy1.Definition.Widgets))
	}
	for i, widget := range copy1.Definition.Widgets {
		if widget.ID == d.Definition.Widgets[i].ID {
			t.Fatal("copy must regenerate widget IDs")
		}
		if widget.AnalysisID != d.Definition.Widgets[i].AnalysisID ||
			widget.AnalysisVersion != d.Definition.Widgets[i].AnalysisVersion {
			t.Fatal("pinned immutable references must be retained")
		}
	}

	// The copy has no publication witnesses, runs or idempotency rows of its own.
	var versionRows, runRows int
	if e = f.s.Pool.QueryRow(ctx, `SELECT count(*) FROM dashboard_versions WHERE dashboard_id=$1`, copy1.ID).Scan(&versionRows); e != nil || versionRows != 0 {
		t.Fatalf("copy must be unpublished: rows=%d err=%v", versionRows, e)
	}
	if e = f.s.Pool.QueryRow(ctx, `SELECT count(*) FROM dashboard_runs WHERE dashboard_id=$1`, copy1.ID).Scan(&runRows); e != nil || runRows != 0 {
		t.Fatalf("copy must not carry runs: rows=%d err=%v", runRows, e)
	}
	// The source publication remains intact.
	if source, e := f.s.GetDashboard(ctx, f.viewer, f.project, d.ID); e != nil || source.PublishedVersion != 1 {
		t.Fatalf("source publication changed: %+v %v", source, e)
	}
	var auditCount int
	if e = f.s.Pool.QueryRow(ctx, `SELECT count(*) FROM audit_entries WHERE action='dashboard.copied' AND resource_key=$1`, copy1.ID).Scan(&auditCount); e != nil || auditCount != 1 {
		t.Fatalf("expected one copy audit entry, got %d err=%v", auditCount, e)
	}

	// Same-key replay returns the identical copy.
	replay, e := f.s.CopyDashboard(ctx, f.owner, f.project, d.ID, "copy-key", reporting.DashboardCopy{Title: "Copied dashboard"})
	if e != nil || replay.ID != copy1.ID {
		t.Fatalf("idempotent replay must return the same copy: %+v %v", replay, e)
	}
	// A changed request under the same key conflicts rather than creating a twin.
	_, e = f.s.CopyDashboard(ctx, f.owner, f.project, d.ID, "copy-key", reporting.DashboardCopy{Title: "Different title"})
	faultCode(t, e, "IDEMPOTENCY_CONFLICT")
}

func TestDashboardCopyRetainsParametersAndTargetsFolder(t *testing.T) {
	f, d, _, _ := parameterRunFixture(t, true)
	dashboardPublish(t, f, d)
	ctx := context.Background()
	folder, e := f.s.CreateDashboardFolder(ctx, f.owner, f.project, ids.New(), reporting.DashboardFolderCreate{Environment: "development", Title: "Copy target"})
	if e != nil {
		t.Fatal(e)
	}

	copy1, e := f.s.CopyDashboard(ctx, f.owner, f.project, d.ID, ids.New(), reporting.DashboardCopy{
		Title:    "Parameterized copy",
		FolderID: folder.ID,
	})
	if e != nil {
		t.Fatal(e)
	}
	if copy1.FolderID != folder.ID {
		t.Fatalf("copy must land in the target folder, got %q", copy1.FolderID)
	}
	if len(copy1.Definition.Parameters) != len(d.Definition.Parameters) {
		t.Fatal("parameter declarations must be retained")
	}
	sourceWidget := d.Definition.Widgets[0]
	copyWidget := copy1.Definition.Widgets[0]
	if copyWidget.ID == sourceWidget.ID {
		t.Fatal("widget ID must be regenerated")
	}
	if len(copyWidget.ParameterBindings) != len(sourceWidget.ParameterBindings) {
		t.Fatal("parameter bindings must be retained")
	}
	if copyWidget.ParameterBindings[0].ParameterID != sourceWidget.ParameterBindings[0].ParameterID {
		t.Fatal("bound parameter identity must be retained")
	}

	// An unknown folder is rejected before any row is written.
	_, e = f.s.CopyDashboard(ctx, f.owner, f.project, d.ID, ids.New(), reporting.DashboardCopy{
		Title:    "Bad folder",
		FolderID: ids.New(),
	})
	faultCode(t, e, "DASHBOARD_FOLDER_NOT_FOUND")
}

func TestDashboardCopyMissingSourceAndArchivedSource(t *testing.T) {
	f, d, _ := dashboardFixture(t, 1)
	ctx := context.Background()

	// Unknown source fails closed.
	_, e := f.s.CopyDashboard(ctx, f.owner, f.project, ids.New(), ids.New(), reporting.DashboardCopy{Title: "No source"})
	faultCode(t, e, "DASHBOARD_NOT_FOUND")

	// Copying an archived dashboard yields a fresh active draft without
	// changing the source archive state.
	if _, e = f.s.ArchiveDashboard(ctx, f.owner, f.project, d.ID); e != nil {
		t.Fatal(e)
	}
	copy1, e := f.s.CopyDashboard(ctx, f.owner, f.project, d.ID, ids.New(), reporting.DashboardCopy{Title: "From archive"})
	if e != nil {
		t.Fatal(e)
	}
	var archivedSource, archivedCopy bool
	if e = f.s.Pool.QueryRow(ctx, `SELECT archived_at IS NOT NULL FROM dashboards WHERE dashboard_id=$1`, d.ID).Scan(&archivedSource); e != nil || !archivedSource {
		t.Fatalf("source archive state changed: %v err=%v", archivedSource, e)
	}
	if e = f.s.Pool.QueryRow(ctx, `SELECT archived_at IS NOT NULL FROM dashboards WHERE dashboard_id=$1`, copy1.ID).Scan(&archivedCopy); e != nil || archivedCopy {
		t.Fatalf("copy must start active: archived=%v err=%v", archivedCopy, e)
	}
}
