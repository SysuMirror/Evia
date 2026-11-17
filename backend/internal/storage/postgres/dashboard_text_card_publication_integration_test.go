//go:build integration

package postgres

import (
	"context"
	"testing"

	"github.com/howtomakeaname/Evia/backend/internal/reporting"
)

func textOnlyDashboard() reporting.DashboardDefinition {
	return reporting.DashboardDefinition{
		Title: "纯文本卡看板",
		Widgets: []reporting.DashboardWidget{{
			ID:           "11111111-1111-4111-8111-111111111111",
			Title:        "说明",
			ReadingOrder: 0,
			Layout:       reporting.DashboardLayout{X: 0, Y: 0, Width: 12, Height: 3},
			TextCard:     &reporting.TextCard{Content: "# 仅说明\n\n本看板不产生查询。"},
		}},
	}
}

func TestDashboardTextCardOnlyPublication(t *testing.T) {
	f, d, _ := dashboardFixture(t, 1)
	ctx := context.Background()
	next, e := f.s.SaveDashboardDraft(ctx, f.owner, f.project, d.ID, d.DraftRevision, textOnlyDashboard(), "")
	if e != nil {
		t.Fatal(e)
	}
	// A presentation-only board publishes with zero witnesses; the manifest
	// must still be a well-formed object whose analyses is an empty array.
	published := dashboardPublish(t, f, next)
	if len(published.Dependencies.Analyses) != 0 {
		t.Fatalf("text-only board must carry zero dependency witnesses, got %d", len(published.Dependencies.Analyses))
	}
	version, e := f.s.GetDashboardVersion(ctx, f.viewer, f.project, d.ID, 1)
	if e != nil {
		t.Fatal(e)
	}
	if len(version.Definition.Widgets) != 1 || version.Definition.Widgets[0].TextCard == nil {
		t.Fatal("text card must survive publication in the immutable version")
	}
}
