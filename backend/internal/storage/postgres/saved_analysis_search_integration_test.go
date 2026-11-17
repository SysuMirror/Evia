//go:build integration

package postgres

import (
	"context"
	"testing"

	"github.com/howtomakeaname/Evia/backend/internal/platform/ids"
	"github.com/howtomakeaname/Evia/backend/internal/reporting"
)

// titledSavedAnalysis clones the fixture definition with a replacement title
// so each search target is a distinct, valid analysis in the same project.
func titledSavedAnalysis(title string) reporting.SavedAnalysisDefinition {
	out := savedDefinition()
	out.Title = title
	return out
}

func TestSavedAnalysisSearchFiltersByTitleSubstring(t *testing.T) {
	f, base := savedFixture(t)
	ctx := context.Background()
	targets := []string{"留存概览", "留存明细", "Revenue 100%match", "DAU_趋势"}
	created := make(map[string]string, len(targets))
	for i, title := range targets {
		draft, e := f.s.CreateSavedAnalysis(ctx, f.owner, f.project, ids.New(), reporting.SavedAnalysisCreate{
			Environment: "development",
			Definition:  titledSavedAnalysis(title),
		})
		if e != nil {
			t.Fatalf("create target %d: %v", i, e)
		}
		created[title] = draft.ID
	}

	// Empty search disables the filter: the fixture plus all targets are returned.
	all, e := f.s.ListSavedAnalyses(ctx, f.owner, f.project, "development", "", 100, false, "")
	if e != nil || len(all) != len(targets)+1 {
		t.Fatalf("unfiltered list mismatch: count=%d err=%v", len(all), e)
	}

	// Case-insensitive substring on the Chinese term.
	retained, e := f.s.ListSavedAnalyses(ctx, f.owner, f.project, "development", "", 100, false, reporting.EscapeLikePattern("留存"))
	if e != nil || len(retained) != 2 {
		t.Fatalf("substring search should match two analyses: count=%d err=%v", len(retained), e)
	}
	for _, item := range retained {
		if item.ID != created["留存概览"] && item.ID != created["留存明细"] {
			t.Fatalf("search returned unrelated analysis %q", item.Title)
		}
	}

	// Case-insensitive Latin matching.
	latin, e := f.s.ListSavedAnalyses(ctx, f.owner, f.project, "development", "", 100, false, reporting.EscapeLikePattern("revenue"))
	if e != nil || len(latin) != 1 || latin[0].ID != created["Revenue 100%match"] {
		t.Fatalf("case-insensitive Latin search failed: %+v %v", latin, e)
	}

	// Escaped '%' is a literal: only the title containing a percent sign matches.
	percentLiteral, e := f.s.ListSavedAnalyses(ctx, f.owner, f.project, "development", "", 100, false, reporting.EscapeLikePattern("%"))
	if e != nil || len(percentLiteral) != 1 || percentLiteral[0].ID != created["Revenue 100%match"] {
		t.Fatalf("escaped %% must act as a literal, got %+v %v", percentLiteral, e)
	}

	// Escaped '_' is a literal rather than a single-character wildcard.
	underscoreLiteral, e := f.s.ListSavedAnalyses(ctx, f.owner, f.project, "development", "", 100, false, reporting.EscapeLikePattern("_"))
	if e != nil || len(underscoreLiteral) != 1 || underscoreLiteral[0].ID != created["DAU_趋势"] {
		t.Fatalf("escaped _ must act as a literal, got %+v %v", underscoreLiteral, e)
	}

	// A substring present in no title returns an empty, non-nil page.
	none, e := f.s.ListSavedAnalyses(ctx, f.owner, f.project, "development", "", 100, false, reporting.EscapeLikePattern("不存在的标题"))
	if e != nil || len(none) != 0 {
		t.Fatalf("no-match search must be empty: %+v %v", none, e)
	}

	// Search stays inside the selected environment and never leaks the base row.
	stagingEmpty, e := f.s.ListSavedAnalyses(ctx, f.owner, f.project, "staging", "", 100, false, reporting.EscapeLikePattern("留存"))
	if e != nil || len(stagingEmpty) != 0 {
		t.Fatalf("search must not cross environments: %+v %v", stagingEmpty, e)
	}
	if base.ID == "" {
		t.Fatal("fixture baseline missing")
	}
}

func TestSavedAnalysisSearchCombinesArchive(t *testing.T) {
	f, _ := savedFixture(t)
	ctx := context.Background()
	first, e := f.s.CreateSavedAnalysis(ctx, f.owner, f.project, ids.New(), reporting.SavedAnalysisCreate{
		Environment: "development",
		Definition:  titledSavedAnalysis("留存分析甲"),
	})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = f.s.CreateSavedAnalysis(ctx, f.owner, f.project, ids.New(), reporting.SavedAnalysisCreate{
		Environment: "development",
		Definition:  titledSavedAnalysis("留存分析乙"),
	}); e != nil {
		t.Fatal(e)
	}

	// The active search returns both new matches (the fixture title differs).
	active, e := f.s.ListSavedAnalyses(ctx, f.owner, f.project, "development", "", 100, false, reporting.EscapeLikePattern("留存分析"))
	if e != nil || len(active) != 2 {
		t.Fatalf("active search should return both matches: %+v %v", active, e)
	}

	// Archiving one match removes it from the active search and surfaces it in
	// the archived search, proving the archive predicate still applies.
	if _, e = f.s.ArchiveSavedAnalysis(ctx, f.owner, f.project, first.ID); e != nil {
		t.Fatal(e)
	}
	activeAfter, e := f.s.ListSavedAnalyses(ctx, f.owner, f.project, "development", "", 100, false, reporting.EscapeLikePattern("留存分析"))
	if e != nil || len(activeAfter) != 1 || activeAfter[0].ID == first.ID {
		t.Fatalf("archived analysis must leave the active search: %+v %v", activeAfter, e)
	}
	archived, e := f.s.ListSavedAnalyses(ctx, f.owner, f.project, "development", "", 100, true, reporting.EscapeLikePattern("留存分析"))
	if e != nil || len(archived) != 1 || archived[0].ID != first.ID {
		t.Fatalf("archived search must still find the analysis: %+v %v", archived, e)
	}
}
