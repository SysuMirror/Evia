package reporting

import (
	"strings"
	"testing"
)

const dashboardMetricID = "44444444-4444-4444-8444-444444444444"

func metricWidget(id string, order int) DashboardWidget {
	return DashboardWidget{
		ID:           id,
		MetricRef:    &MetricReference{MetricID: dashboardMetricID, Version: 2},
		Title:        "Metric card",
		ReadingOrder: order,
		Layout:       DashboardLayout{Width: 12, Height: 2},
	}
}

func TestNormalizeDashboardAcceptsMetricWidget(t *testing.T) {
	def := DashboardDefinition{
		Title:       "Mixed",
		Description: "one analysis and one metric widget",
		Widgets: []DashboardWidget{
			{ID: dashboardWidgetOne, AnalysisID: dashboardAnalysisID, AnalysisVersion: 1, Title: "Analysis", Layout: DashboardLayout{X: 0, Y: 0, Width: 6, Height: 2}},
			{ID: dashboardWidgetTwo, MetricRef: &MetricReference{MetricID: dashboardMetricID, Version: 2}, Title: "Metric", ReadingOrder: 1, Layout: DashboardLayout{X: 6, Y: 0, Width: 6, Height: 2}},
		},
	}
	normalized, e := NormalizeDashboardDefinition(def)
	if e != nil {
		t.Fatalf("mixed source dashboard should normalize: %v", e)
	}
	if normalized.Widgets[1].MetricRef == nil || normalized.Widgets[1].MetricRef.Version != 2 {
		t.Fatalf("metric ref lost: %+v", normalized.Widgets[1])
	}
	if normalized.Widgets[1].AnalysisID != "" {
		t.Fatalf("metric widget must not carry analysis id: %+v", normalized.Widgets[1])
	}
}

func TestNormalizeDashboardRejectsAmbiguousWidgetSource(t *testing.T) {
	base := func() DashboardDefinition {
		return DashboardDefinition{Title: "Bad", Widgets: []DashboardWidget{metricWidget(dashboardWidgetOne, 0)}}
	}
	for _, tc := range []struct {
		name string
		edit func(*DashboardWidget)
	}{
		{"both sources", func(w *DashboardWidget) {
			w.AnalysisID = dashboardAnalysisID
			w.AnalysisVersion = 1
		}},
		{"neither source", func(w *DashboardWidget) {
			w.MetricRef = nil
		}},
		{"bad metric id", func(w *DashboardWidget) {
			w.MetricRef.MetricID = "not-a-uuid"
		}},
		{"bad metric version", func(w *DashboardWidget) {
			w.MetricRef.Version = 0
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			def := base()
			tc.edit(&def.Widgets[0])
			if _, e := NormalizeDashboardDefinition(def); e == nil {
				t.Fatalf("expected rejection for %s", tc.name)
			}
		})
	}
}

func TestBuildDashboardValidationMatchesMetricDependency(t *testing.T) {
	def, e := NormalizeDashboardDefinition(DashboardDefinition{
		Title:   "Metric only",
		Widgets: []DashboardWidget{metricWidget(dashboardWidgetOne, 0)},
	})
	if e != nil {
		t.Fatal(e)
	}
	draft := DashboardDraft{Dashboard: Dashboard{ID: "d", ProjectID: "p", Environment: "development", DraftRevision: 1}, Definition: def}
	deps := DashboardDependencies{
		ProjectID:   "p",
		Environment: "development",
		Analyses: []DashboardDependency{{
			WidgetID:       dashboardWidgetOne,
			MetricID:       dashboardMetricID,
			MetricVersion:  2,
			DefinitionHash: strings.Repeat("a", 64),
			DependencyHash: strings.Repeat("b", 64),
			ValidationHash: strings.Repeat("c", 64),
		}},
	}
	if _, e = BuildDashboardValidation(DashboardSnapshot{Draft: draft, Dependencies: deps}); e != nil {
		t.Fatalf("matching metric dependency should validate: %v", e)
	}

	// A witness that still carries analysis columns for a metric widget fails.
	mismatch := deps
	mismatch.Analyses[0].MetricID = ""
	mismatch.Analyses[0].AnalysisID = dashboardAnalysisID
	mismatch.Analyses[0].AnalysisVersion = 1
	if _, e = BuildDashboardValidation(DashboardSnapshot{Draft: draft, Dependencies: mismatch}); e == nil {
		t.Fatal("expected mismatched metric dependency rejection")
	}
}
