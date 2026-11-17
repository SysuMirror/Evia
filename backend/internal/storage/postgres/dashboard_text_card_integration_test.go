//go:build integration

package postgres

import (
	"context"
	"testing"

	"github.com/howtomakeaname/Evia/backend/internal/platform/fault"
	"github.com/howtomakeaname/Evia/backend/internal/reporting"
)

func textCardDefinition(source reporting.DashboardWidget) reporting.DashboardDefinition {
	return reporting.DashboardDefinition{
		Title:       "文本卡混合看板",
		Description: "一个说明卡加一个查询卡",
		Widgets: []reporting.DashboardWidget{
			{
				ID:           "11111111-1111-4111-8111-111111111111",
				Title:        "说明卡",
				ReadingOrder: 0,
				Layout:       reporting.DashboardLayout{X: 0, Y: 0, Width: 12, Height: 3},
				TextCard:     &reporting.TextCard{Content: "# 使用说明\n\n仅允许 [文档](https://example.com) 链接。"},
			},
			{
				ID:              source.ID,
				AnalysisID:      source.AnalysisID,
				AnalysisVersion: source.AnalysisVersion,
				MetricRef:       source.MetricRef,
				Title:           source.Title,
				ReadingOrder:    1,
				Layout:          reporting.DashboardLayout{X: 0, Y: 3, Width: 12, Height: 5},
			},
		},
	}
}

func TestDashboardTextCardShapeGate(t *testing.T) {
	s := database(t)
	ctx := context.Background()
	valid := textCardShape("valid",
		`{"title":"T","description":"","widgets":[{"widget_id":"11111111-1111-4111-8111-111111111111","title":"卡","reading_order":0,"layout":{"x":0,"y":0,"width":12,"height":3},"text_card":{"content":"# 标题\n\n**加粗** 与 [链接](https://example.com)"}}]}`)
	invalid := []string{
		// Raw HTML in the Markdown payload.
		textCardShape("html",
			`{"title":"T","description":"","widgets":[{"widget_id":"11111111-1111-4111-8111-111111111111","title":"卡","reading_order":0,"layout":{"x":0,"y":0,"width":12,"height":3},"text_card":{"content":"<b>bold</b>"}}]}`),
		// Text card combined with an analysis source.
		textCardShape("combined",
			`{"title":"T","description":"","widgets":[{"widget_id":"11111111-1111-4111-8111-111111111111","title":"卡","reading_order":0,"layout":{"x":0,"y":0,"width":12,"height":3},"analysis_id":"33333333-3333-4333-8333-333333333333","analysis_version":1,"text_card":{"content":"正文"}}]}`),
		// Text card with a parameter binding.
		textCardShape("binding",
			`{"title":"T","description":"","parameters":[{"parameter_id":"p","label":"P","type":"string","operator":"eq","required":false}],"widgets":[{"widget_id":"11111111-1111-4111-8111-111111111111","title":"卡","reading_order":0,"layout":{"x":0,"y":0,"width":12,"height":3},"text_card":{"content":"正文"},"parameter_bindings":[{"parameter_id":"p","field":{"kind":"system","key":"uid"}}]}]}`),
		// Blocked javascript scheme.
		textCardShape("script",
			`{"title":"T","description":"","widgets":[{"widget_id":"11111111-1111-4111-8111-111111111111","title":"卡","reading_order":0,"layout":{"x":0,"y":0,"width":12,"height":3},"text_card":{"content":"[x](javascript:alert(1))"}}]}`),
		// Unterminated fenced code block.
		textCardShape("fence",
			"{\"title\":\"T\",\"description\":\"\",\"widgets\":[{\"widget_id\":\"11111111-1111-4111-8111-111111111111\",\"title\":\"卡\",\"reading_order\":0,\"layout\":{\"x\":0,\"y\":0,\"width\":12,\"height\":3},\"text_card\":{\"content\":\"```go\"}}]}"),
		// No source at all remains invalid.
		textCardShape("nosource",
			`{"title":"T","description":"","widgets":[{"widget_id":"11111111-1111-4111-8111-111111111111","title":"卡","reading_order":0,"layout":{"x":0,"y":0,"width":12,"height":3}}]}`),
	}
	var ok bool
	if e := s.Pool.QueryRow(ctx, `SELECT dashboard_definition_valid($1::json)`, valid).Scan(&ok); e != nil {
		t.Fatal(e)
	}
	if !ok {
		t.Fatal("valid text card shape rejected by the SQL gate")
	}
	for _, raw := range invalid {
		if e := s.Pool.QueryRow(ctx, `SELECT dashboard_definition_valid($1::json)`, raw).Scan(&ok); e != nil {
			t.Fatal(e)
		}
		if ok {
			t.Fatalf("invalid text card shape accepted: %s", raw)
		}
	}
}

func textCardShape(name, raw string) string {
	_ = name
	return raw
}

func TestDashboardTextCardPublishAndRunFanout(t *testing.T) {
	f, d, execute := dashboardFixture(t, 1)
	ctx := context.Background()
	// Replace the plain definition with a mixed text+analysis dashboard.
	definition := textCardDefinition(d.Definition.Widgets[0])
	next, e := f.s.SaveDashboardDraft(ctx, f.owner, f.project, d.ID, d.DraftRevision, definition, "")
	if e != nil {
		t.Fatal(e)
	}
	published := dashboardPublish(t, f, next)
	if len(published.Dependencies.Analyses) != 1 {
		t.Fatalf("only the executable widget gets a dependency witness, got %d", len(published.Dependencies.Analyses))
	}

	run, e := f.s.EnqueueDashboardRun(ctx, f.owner, f.project, d.ID, 1, "text-run", reporting.DashboardExecute{
		TimeRange: execute.TimeRange,
		Timezone:  execute.Timezone,
	})
	if e != nil {
		t.Fatal(e)
	}
	if len(run.Widgets) != 1 {
		t.Fatalf("run must fan out only the executable widget, got %d", len(run.Widgets))
	}
	if run.Widgets[0].WidgetID != d.Definition.Widgets[0].ID {
		t.Fatalf("unexpected run widget %s", run.Widgets[0].WidgetID)
	}
	var runRows int
	if e = f.s.Pool.QueryRow(ctx,
		`SELECT count(*) FROM dashboard_run_widgets WHERE run_id=$1`, run.ID).Scan(&runRows); e != nil {
		t.Fatal(e)
	}
	if runRows != 1 {
		t.Fatalf("expected one run_widget row for the executable card, got %d", runRows)
	}
	// The text card content remains readable through the immutable version.
	version, e := f.s.GetDashboardVersion(ctx, f.viewer, f.project, d.ID, 1)
	if e != nil {
		t.Fatal(e)
	}
	card := version.Definition.Widgets[0]
	if card.TextCard == nil || card.TextCard.Content == "" {
		t.Fatal("text card must survive publication in the immutable version")
	}
}

func TestDashboardTextCardOnlyRunRejected(t *testing.T) {
	f, d, execute := dashboardFixture(t, 1)
	ctx := context.Background()
	next, e := f.s.SaveDashboardDraft(ctx, f.owner, f.project, d.ID, d.DraftRevision, textOnlyDashboard(), "")
	if e != nil {
		t.Fatal(e)
	}
	dashboardPublish(t, f, next)
	_, e = f.s.EnqueueDashboardRun(ctx, f.owner, f.project, d.ID, 1, "text-only-run", reporting.DashboardExecute{
		TimeRange: execute.TimeRange,
		Timezone:  execute.Timezone,
	})
	if e == nil || fault.Public(e).Code != "DASHBOARD_NO_EXECUTABLE_WIDGET" {
		t.Fatalf("expected DASHBOARD_NO_EXECUTABLE_WIDGET, got %v", e)
	}
	var runRows int
	if e = f.s.Pool.QueryRow(ctx, `SELECT count(*) FROM dashboard_runs WHERE dashboard_id=$1`, d.ID).Scan(&runRows); e != nil {
		t.Fatal(e)
	}
	if runRows != 0 {
		t.Fatalf("rejected admission must not persist a ghost run, got %d rows", runRows)
	}
}
