//go:build integration

package postgres

import (
	"context"
	"testing"

	"github.com/howtomakeaname/Evia/backend/internal/platform/ids"
	"github.com/howtomakeaname/Evia/backend/internal/reporting"
	"github.com/howtomakeaname/Evia/backend/internal/reports"
)

// titledReportInput clones the fixture report input with a replacement title so
// each search target is a distinct, valid report in the same project.
func titledReportInput(f catalogFixture, environment, title string, versions []reporting.SavedAnalysisVersion) reports.Input {
	in := reportInput(f, environment, versions[:1])
	in.Definition.Title = title
	in.IdempotencyKey = ids.New()
	in.RequestHash = reportRequestHash(in.Definition)
	return in
}

func TestReportSearchFiltersByTitleSubstring(t *testing.T) {
	f, versions := reportFixture(t, 2)
	ctx := context.Background()
	svc := reports.NewService(f.s)

	base, e := svc.Create(ctx, reportInput(f, "development", versions[:1]), reports.CreateRevision)
	if e != nil {
		t.Fatal(e)
	}
	targets := []string{"留存概览", "留存明细", "Revenue 100%match", "DAU_趋势"}
	created := make(map[string]string, len(targets))
	for i, title := range targets {
		report, err := svc.Create(ctx, titledReportInput(f, "development", title, versions), reports.CreateRevision)
		if err != nil {
			t.Fatalf("create target %d: %v", i, err)
		}
		created[title] = report.ID
	}

	// The store expects an already-escaped literal; empty search disables the filter.
	all, e := f.s.List(ctx, f.viewer, f.project, "development", "", 100, "")
	if e != nil || len(all) != len(targets)+1 {
		t.Fatalf("unfiltered list mismatch: count=%d err=%v", len(all), e)
	}

	// Case-insensitive substring on the Chinese term.
	retained, e := f.s.List(ctx, f.viewer, f.project, "development", "", 100, reports.EscapeLikePattern("留存"))
	if e != nil || len(retained) != 2 {
		t.Fatalf("substring search should match two reports: count=%d err=%v", len(retained), e)
	}
	for _, item := range retained {
		if item.ID != created["留存概览"] && item.ID != created["留存明细"] {
			t.Fatalf("search returned unrelated report %q", item.Definition.Title)
		}
	}

	// Case-insensitive Latin matching.
	latin, e := f.s.List(ctx, f.viewer, f.project, "development", "", 100, reports.EscapeLikePattern("revenue"))
	if e != nil || len(latin) != 1 || latin[0].ID != created["Revenue 100%match"] {
		t.Fatalf("case-insensitive Latin search failed: %+v %v", latin, e)
	}

	// Escaped '%' is a literal: only the title containing a percent sign matches.
	percentLiteral, e := f.s.List(ctx, f.viewer, f.project, "development", "", 100, reports.EscapeLikePattern("%"))
	if e != nil || len(percentLiteral) != 1 || percentLiteral[0].ID != created["Revenue 100%match"] {
		t.Fatalf("escaped %% must act as a literal, got %+v %v", percentLiteral, e)
	}

	// Escaped '_' is a literal rather than a single-character wildcard.
	underscoreLiteral, e := f.s.List(ctx, f.viewer, f.project, "development", "", 100, reports.EscapeLikePattern("_"))
	if e != nil || len(underscoreLiteral) != 1 || underscoreLiteral[0].ID != created["DAU_趋势"] {
		t.Fatalf("escaped _ must act as a literal, got %+v %v", underscoreLiteral, e)
	}

	// A substring present in no title returns an empty, non-nil page.
	none, e := f.s.List(ctx, f.viewer, f.project, "development", "", 100, reports.EscapeLikePattern("不存在的标题"))
	if e != nil || len(none) != 0 {
		t.Fatalf("no-match search must be empty: %+v %v", none, e)
	}

	// All reports are development-only; a staging search must return nothing and
	// never leak the development rows across environments.
	stagingEmpty, e := f.s.List(ctx, f.viewer, f.project, "staging", "", 100, reports.EscapeLikePattern("留存"))
	if e != nil || len(stagingEmpty) != 0 {
		t.Fatalf("search must not cross environments: %+v %v", stagingEmpty, e)
	}
	if base.ID == "" {
		t.Fatal("fixture baseline missing")
	}
}
