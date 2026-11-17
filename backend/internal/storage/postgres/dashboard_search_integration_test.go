//go:build integration

package postgres

import (
	"context"
	"testing"

	"github.com/howtomakeaname/Evia/backend/internal/platform/ids"
	"github.com/howtomakeaname/Evia/backend/internal/reporting"
)

// titledDashboard clones the fixture definition with a replacement title so
// each search target is a distinct, valid dashboard in the same project.
func titledDashboard(base reporting.DashboardDefinition, title string) reporting.DashboardDefinition {
	out := base
	out.Title = title
	return out
}

func idsFrom(list []reporting.Dashboard) map[string]bool {
	out := make(map[string]bool, len(list))
	for _, item := range list {
		out[item.ID] = true
	}
	return out
}

func TestDashboardSearchFiltersByTitleSubstring(t *testing.T) {
	f, d, _ := dashboardFixture(t, 1)
	ctx := context.Background()
	base := d.Definition
	targets := []string{"留存概览", "留存明细", "Revenue 100%match", "DAU_趋势"}
	created := make(map[string]string, len(targets))
	for i, title := range targets {
		draft, e := f.s.CreateDashboard(ctx, f.owner, f.project, ids.New(), reporting.DashboardCreate{
			Environment: "development",
			Definition:  titledDashboard(base, title),
		})
		if e != nil {
			t.Fatalf("create target %d: %v", i, e)
		}
		created[title] = draft.ID
	}

	// Empty search disables the filter: the fixture plus all targets are returned.
	all, e := f.s.ListDashboards(ctx, f.owner, f.project, "development", "", 100, false, "", "")
	if e != nil || len(all) != len(targets)+1 {
		t.Fatalf("unfiltered list mismatch: count=%d err=%v", len(all), e)
	}

	// Case-insensitive substring on the Chinese term.
	retained, e := f.s.ListDashboards(ctx, f.owner, f.project, "development", "", 100, false, "", reporting.EscapeLikePattern("留存"))
	if e != nil || len(retained) != 2 {
		t.Fatalf("substring search should match two dashboards: count=%d err=%v", len(retained), e)
	}
	for _, item := range retained {
		if item.ID != created["留存概览"] && item.ID != created["留存明细"] {
			t.Fatalf("search returned unrelated dashboard %q", item.Title)
		}
	}

	// Case-insensitive Latin matching.
	latin, e := f.s.ListDashboards(ctx, f.owner, f.project, "development", "", 100, false, "", reporting.EscapeLikePattern("revenue"))
	if e != nil || len(latin) != 1 || latin[0].ID != created["Revenue 100%match"] {
		t.Fatalf("case-insensitive Latin search failed: %+v %v", latin, e)
	}

	// Escaped '%' is a literal: only the title containing a percent sign matches.
	percentLiteral, e := f.s.ListDashboards(ctx, f.owner, f.project, "development", "", 100, false, "", reporting.EscapeLikePattern("%"))
	if e != nil || len(percentLiteral) != 1 || percentLiteral[0].ID != created["Revenue 100%match"] {
		t.Fatalf("escaped %% must act as a literal, got %+v %v", percentLiteral, e)
	}

	// Escaped '_' is a literal rather than a single-character wildcard.
	underscoreLiteral, e := f.s.ListDashboards(ctx, f.owner, f.project, "development", "", 100, false, "", reporting.EscapeLikePattern("_"))
	if e != nil || len(underscoreLiteral) != 1 || underscoreLiteral[0].ID != created["DAU_趋势"] {
		t.Fatalf("escaped _ must act as a literal, got %+v %v", underscoreLiteral, e)
	}

	// A substring present in no title returns an empty, non-nil page.
	none, e := f.s.ListDashboards(ctx, f.owner, f.project, "development", "", 100, false, "", reporting.EscapeLikePattern("不存在的标题"))
	if e != nil || len(none) != 0 {
		t.Fatalf("no-match search must be empty: %+v %v", none, e)
	}

	// Search stays inside the selected environment.
	stagingEmpty, e := f.s.ListDashboards(ctx, f.owner, f.project, "staging", "", 100, false, "", reporting.EscapeLikePattern("留存"))
	if e != nil || len(stagingEmpty) != 0 {
		t.Fatalf("search must not cross environments: %+v %v", stagingEmpty, e)
	}
}

func TestDashboardSearchCombinesFolderAndArchive(t *testing.T) {
	f, d, _ := dashboardFixture(t, 1)
	ctx := context.Background()
	root, e := f.s.CreateDashboardFolder(ctx, f.owner, f.project, ids.New(), reporting.DashboardFolderCreate{
		Environment: "development",
		Title:       "Search folder",
	})
	if e != nil {
		t.Fatal(e)
	}
	inFolder, e := f.s.CreateDashboard(ctx, f.owner, f.project, ids.New(), reporting.DashboardCreate{
		Environment: "development",
		FolderID:    root.ID,
		Definition:  titledDashboard(d.Definition, "文件夹内留存看板"),
	})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = f.s.CreateDashboard(ctx, f.owner, f.project, ids.New(), reporting.DashboardCreate{
		Environment: "development",
		Definition:  titledDashboard(d.Definition, "根级留存看板"),
	}); e != nil {
		t.Fatal(e)
	}

	// Search + folder scope intersects: only the in-folder match is returned.
	inScope, e := f.s.ListDashboards(ctx, f.owner, f.project, "development", "", 100, false, root.ID, reporting.EscapeLikePattern("留存"))
	if e != nil || len(inScope) != 1 || inScope[0].ID != inFolder.ID {
		t.Fatalf("search combined with folder scope failed: %+v %v", inScope, e)
	}

	// The same term without folder scope returns both active matches.
	active, e := f.s.ListDashboards(ctx, f.owner, f.project, "development", "", 100, false, "", reporting.EscapeLikePattern("留存"))
	if e != nil || len(active) != 2 {
		t.Fatalf("active search should return both matches: %+v %v", active, e)
	}

	// Archiving one match removes it from the active search and surfaces it in
	// the archived search, proving the archive predicate still applies.
	if _, e = f.s.ArchiveDashboard(ctx, f.owner, f.project, inFolder.ID); e != nil {
		t.Fatal(e)
	}
	activeAfter, e := f.s.ListDashboards(ctx, f.owner, f.project, "development", "", 100, false, "", reporting.EscapeLikePattern("留存"))
	if e != nil || len(activeAfter) != 1 || activeAfter[0].ID == inFolder.ID {
		t.Fatalf("archived dashboard must leave the active search: %+v %v", activeAfter, e)
	}
	archived, e := f.s.ListDashboards(ctx, f.owner, f.project, "development", "", 100, true, root.ID, reporting.EscapeLikePattern("留存"))
	if e != nil || len(archived) != 1 || archived[0].ID != inFolder.ID {
		t.Fatalf("archived search must still find the dashboard inside its folder: %+v %v", archived, e)
	}
}
