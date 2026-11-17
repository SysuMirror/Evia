//go:build integration

package postgres

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/howtomakeaname/Evia/backend/internal/platform/fault"
	"github.com/howtomakeaname/Evia/backend/internal/reporting"
)

func TestDashboardFolderLifecycle(t *testing.T) {
	f := catalogDB(t)
	ctx := context.Background()

	// Root -> child -> grandchild reaches the depth-3 bound.
	root, e := f.s.CreateDashboardFolder(ctx, f.owner, f.project, "folder-root", reporting.DashboardFolderCreate{Environment: "development", Title: "Root"})
	if e != nil {
		t.Fatal(e)
	}
	if root.Depth != 1 || root.ParentID != "" || root.Revision != 1 {
		t.Fatalf("unexpected root folder: %+v", root)
	}
	child, e := f.s.CreateDashboardFolder(ctx, f.owner, f.project, "folder-child", reporting.DashboardFolderCreate{Environment: "development", ParentID: root.ID, Title: "Child"})
	if e != nil {
		t.Fatal(e)
	}
	if child.Depth != 2 || child.ParentID != root.ID {
		t.Fatalf("unexpected child folder: %+v", child)
	}
	grandchild, e := f.s.CreateDashboardFolder(ctx, f.owner, f.project, "folder-grandchild", reporting.DashboardFolderCreate{Environment: "development", ParentID: child.ID, Title: "Grandchild"})
	if e != nil {
		t.Fatal(e)
	}
	if grandchild.Depth != 3 {
		t.Fatalf("grandchild must be depth 3: %+v", grandchild)
	}

	// A fourth level is rejected before any row is written.
	_, e = f.s.CreateDashboardFolder(ctx, f.owner, f.project, "folder-too-deep", reporting.DashboardFolderCreate{Environment: "development", ParentID: grandchild.ID, Title: "Too deep"})
	faultCode(t, e, "DASHBOARD_FOLDER_TOO_DEEP")

	// Missing and cross-environment parents share the same not-found answer.
	_, e = f.s.CreateDashboardFolder(ctx, f.owner, f.project, "folder-missing-parent", reporting.DashboardFolderCreate{Environment: "development", ParentID: "33333333-3333-4333-8333-333333333333", Title: "Orphan"})
	faultCode(t, e, "DASHBOARD_FOLDER_PARENT_NOT_FOUND")
	staging, e := f.s.CreateDashboardFolder(ctx, f.owner, f.project, "folder-staging", reporting.DashboardFolderCreate{Environment: "staging", Title: "Staging root"})
	if e != nil {
		t.Fatal(e)
	}
	_, e = f.s.CreateDashboardFolder(ctx, f.owner, f.project, "folder-cross-env", reporting.DashboardFolderCreate{Environment: "staging", ParentID: root.ID, Title: "Cross"})
	faultCode(t, e, "DASHBOARD_FOLDER_PARENT_NOT_FOUND")

	// Idempotency: same key/body replays, same key/different body conflicts.
	replay, e := f.s.CreateDashboardFolder(ctx, f.owner, f.project, "folder-root", reporting.DashboardFolderCreate{Environment: "development", Title: "Root"})
	if e != nil || replay.ID != root.ID {
		t.Fatalf("idempotent replay should return the original folder: %+v %v", replay, e)
	}
	_, e = f.s.CreateDashboardFolder(ctx, f.owner, f.project, "folder-root", reporting.DashboardFolderCreate{Environment: "development", Title: "Renamed attempt"})
	faultCode(t, e, "IDEMPOTENCY_CONFLICT")

	// Listing is environment-scoped and ordered by folder UUID.
	devFolders, e := f.s.ListDashboardFolders(ctx, f.owner, f.project, "development", "", 50)
	if e != nil {
		t.Fatal(e)
	}
	if len(devFolders) != 3 {
		t.Fatalf("development should list 3 folders, got %d", len(devFolders))
	}
	stagingFolders, e := f.s.ListDashboardFolders(ctx, f.owner, f.project, "staging", "", 50)
	if e != nil {
		t.Fatal(e)
	}
	if len(stagingFolders) != 1 || stagingFolders[0].ID != staging.ID {
		t.Fatalf("staging scope leaked folders: %+v", stagingFolders)
	}
	got, e := f.s.GetDashboardFolder(ctx, f.owner, f.project, child.ID)
	if e != nil || got.Title != "Child" {
		t.Fatalf("get folder failed: %+v %v", got, e)
	}

	// Rename requires the current revision and bumps it; stale revisions conflict.
	renamed, e := f.s.UpdateDashboardFolder(ctx, f.owner, f.project, child.ID, child.Revision, reporting.DashboardFolderUpdate{Title: "Child renamed"})
	if e != nil {
		t.Fatal(e)
	}
	if renamed.Revision != child.Revision+1 || renamed.Title != "Child renamed" {
		t.Fatalf("rename did not produce the expected revision/title: %+v", renamed)
	}
	_, e = f.s.UpdateDashboardFolder(ctx, f.owner, f.project, child.ID, child.Revision, reporting.DashboardFolderUpdate{Title: "Stale"})
	faultCode(t, e, "DASHBOARD_FOLDER_REVISION_CONFLICT")

	// The hierarchy position is immutable at the database level.
	_, e = f.s.Pool.Exec(ctx, `UPDATE dashboard_folders SET parent_id=$1 WHERE folder_id=$2`, grandchild.ID, root.ID)
	var pgErr *pgconn.PgError
	if !errors.As(e, &pgErr) || pgErr.Code != "23514" {
		t.Fatalf("folder reparent should be rejected by the scope trigger, got %v", e)
	}

	// Non-empty folders cannot be deleted; an empty one can.
	if e := f.s.DeleteDashboardFolder(ctx, f.owner, f.project, root.ID); e == nil {
		t.Fatal("non-empty root deletion should be rejected")
	} else {
		faultCode(t, e, "DASHBOARD_FOLDER_NOT_EMPTY")
	}
	empty, e := f.s.CreateDashboardFolder(ctx, f.owner, f.project, "folder-empty", reporting.DashboardFolderCreate{Environment: "development", Title: "Empty"})
	if e != nil {
		t.Fatal(e)
	}
	if e := f.s.DeleteDashboardFolder(ctx, f.owner, f.project, empty.ID); e != nil {
		t.Fatalf("empty folder should delete: %v", e)
	}
	if _, e = f.s.GetDashboardFolder(ctx, f.owner, f.project, empty.ID); e == nil {
		t.Fatal("deleted folder should be unreadable")
	} else {
		var missing *fault.Error
		if !errors.As(e, &missing) || missing.Code != "DASHBOARD_FOLDER_NOT_FOUND" {
			t.Fatalf("expected not found after delete, got %v", e)
		}
	}
}

func TestDashboardFolderDashboardAssignment(t *testing.T) {
	f, analysisVersion, _ := savedRunFixture(t)
	ctx := context.Background()
	root, e := f.s.CreateDashboardFolder(ctx, f.owner, f.project, "assign-root", reporting.DashboardFolderCreate{Environment: "development", Title: "Root"})
	if e != nil {
		t.Fatal(e)
	}
	nested, e := f.s.CreateDashboardFolder(ctx, f.owner, f.project, "assign-nested", reporting.DashboardFolderCreate{Environment: "development", ParentID: root.ID, Title: "Nested"})
	if e != nil {
		t.Fatal(e)
	}
	def := reporting.DashboardDefinition{
		Title:       "Folded dashboard",
		Description: "",
		Widgets:     []reporting.DashboardWidget{{ID: "11111111-1111-4111-8111-111111111111", AnalysisID: analysisVersion.AnalysisID, AnalysisVersion: analysisVersion.Version, Title: "Card", Layout: reporting.DashboardLayout{Width: 12, Height: 2}}},
	}
	filed, e := f.s.CreateDashboard(ctx, f.owner, f.project, "filed-dashboard", reporting.DashboardCreate{Environment: "development", FolderID: nested.ID, Definition: def})
	if e != nil {
		t.Fatal(e)
	}
	if filed.FolderID != nested.ID {
		t.Fatalf("dashboard should remember its folder, got %+v", filed)
	}
	rootDashboard, e := f.s.CreateDashboard(ctx, f.owner, f.project, "root-dashboard", reporting.DashboardCreate{Environment: "development", Definition: def})
	if e != nil {
		t.Fatal(e)
	}
	if rootDashboard.FolderID != "" {
		t.Fatalf("dashboard without folder should stay at root: %+v", rootDashboard)
	}

	// A reference to a missing folder fails closed.
	_, e = f.s.CreateDashboard(ctx, f.owner, f.project, "ghost-folder-dashboard", reporting.DashboardCreate{Environment: "development", FolderID: "33333333-3333-4333-8333-333333333333", Definition: def})
	faultCode(t, e, "DASHBOARD_FOLDER_NOT_FOUND")

	// Listing filters: UUID scope, root scope and unfiltered all behave distinctly.
	inFolder, e := f.s.ListDashboards(ctx, f.owner, f.project, "development", "", 50, false, nested.ID, "")
	if e != nil || len(inFolder) != 1 || inFolder[0].ID != filed.ID {
		t.Fatalf("folder filter should return exactly the filed dashboard: %+v %v", inFolder, e)
	}
	atRoot, e := f.s.ListDashboards(ctx, f.owner, f.project, "development", "", 50, false, "root", "")
	if e != nil || len(atRoot) != 1 || atRoot[0].ID != rootDashboard.ID {
		t.Fatalf("root filter should return exactly the unfiled dashboard: %+v %v", atRoot, e)
	}
	all, e := f.s.ListDashboards(ctx, f.owner, f.project, "development", "", 50, false, "", "")
	if e != nil || len(all) != 2 {
		t.Fatalf("unfiltered list should return both dashboards: %+v %v", all, e)
	}
	if _, e = f.s.ListDashboards(ctx, f.owner, f.project, "development", "", 50, false, "bogus", ""); e == nil {
		t.Fatal("invalid folder scope should be rejected")
	}

	// Moving through the draft PATCH preserves the revision contract, including back to root.
	moved, e := f.s.SaveDashboardDraft(ctx, f.owner, f.project, rootDashboard.ID, rootDashboard.DraftRevision, def, nested.ID)
	if e != nil {
		t.Fatal(e)
	}
	if moved.FolderID != nested.ID || moved.DraftRevision != rootDashboard.DraftRevision+1 {
		t.Fatalf("move into folder failed: %+v", moved)
	}
	backToRoot, e := f.s.SaveDashboardDraft(ctx, f.owner, f.project, rootDashboard.ID, moved.DraftRevision, def, "")
	if e != nil {
		t.Fatal(e)
	}
	if backToRoot.FolderID != "" {
		t.Fatalf("empty folder id should move the dashboard back to root: %+v", backToRoot)
	}

	// A folder containing a dashboard cannot be deleted, including via raw SQL (RESTRICT backstop).
	if e := f.s.DeleteDashboardFolder(ctx, f.owner, f.project, nested.ID); e == nil {
		t.Fatal("non-empty folder deletion should be rejected")
	} else {
		faultCode(t, e, "DASHBOARD_FOLDER_NOT_EMPTY")
	}
	_, e = f.s.Pool.Exec(ctx, `DELETE FROM dashboard_folders WHERE folder_id=$1`, nested.ID)
	var restrictErr *pgconn.PgError
	if !errors.As(e, &restrictErr) || restrictErr.Code != "23503" {
		t.Fatalf("foreign key RESTRICT should block raw deletion, got %v", e)
	}
}
