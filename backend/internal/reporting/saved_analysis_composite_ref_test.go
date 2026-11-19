package reporting

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/howtomakeaname/Evia/backend/internal/analysis"
)

const testFormulaID = "33333333-3333-4333-8333-333333333333"

func compositeRefDefinition() SavedAnalysisDefinition {
	return SavedAnalysisDefinition{
		Title:         "Composite formula card",
		MetricRef:     &MetricReference{MetricID: testFormulaID, Version: 2, Kind: MetricRefKindComposite},
		Visualization: SavedAnalysisVisualization{Kind: "metric"},
	}
}

func referencedFormulaVersion() FormulaMetricVersion {
	return FormulaMetricVersion{
		MetricID:       testFormulaID,
		ProjectID:      "project-one",
		Environment:    "development",
		Version:        2,
		Definition:     formulaServiceDef(),
		DefinitionHash: strings.Repeat("c", 64),
	}
}

func TestMetricRefKindNormalization(t *testing.T) {
	cases := map[string]string{
		"":          MetricRefKindAtomic,
		"atomic":    MetricRefKindAtomic,
		"composite": MetricRefKindComposite,
	}
	for in, want := range cases {
		got, err := NormalizedMetricRefKind(in)
		if err != nil || got != want {
			t.Fatalf("kind %q -> %q, %v want %q", in, got, err, want)
		}
	}
	if _, err := NormalizedMetricRefKind("nested"); err == nil {
		t.Fatal("unknown metric ref kind must fail closed")
	}
	atomic := MetricReference{MetricID: testMetricID, Version: 1}
	composite := MetricReference{MetricID: testFormulaID, Version: 1, Kind: MetricRefKindComposite}
	if atomic.IsComposite() || !composite.IsComposite() {
		t.Fatal("IsComposite must be true only for explicit composite kind")
	}
}

func TestCompositeMetricReferenceInputAcceptedAndNormalized(t *testing.T) {
	normalized, err := NormalizeSavedAnalysisDefinition(compositeRefDefinition())
	if err != nil {
		t.Fatal(err)
	}
	if normalized.MetricRef == nil || !normalized.MetricRef.IsComposite() || normalized.MetricRef.Kind != MetricRefKindComposite {
		t.Fatalf("composite reference lost: %+v", normalized.MetricRef)
	}
	if normalized.Query.Kind != "" {
		t.Fatal("unresolved composite input must not carry an inline query")
	}

	// An omitted kind stays atomic and is rejected if sent to the composite path.
	defaulted := compositeRefDefinition()
	defaulted.MetricRef.Kind = ""
	got, err := NormalizeSavedAnalysisDefinition(defaulted)
	if err != nil {
		t.Fatal(err)
	}
	// An empty kind stays empty in the canonical JSON (atomic references remain
	// byte-identical) while semantically treated as atomic by IsComposite.
	if got.MetricRef.IsComposite() || got.MetricRef.Kind != "" {
		t.Fatalf("empty kind must stay empty and behave as atomic: %+v", got.MetricRef)
	}
	encoded, err := json.Marshal(got.MetricRef)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), `"kind":`) {
		t.Fatalf("omitted metric ref kind must not materialize in JSON: %s", string(encoded))
	}

	badKind := compositeRefDefinition()
	badKind.MetricRef.Kind = "nested"
	if _, err = NormalizeSavedAnalysisDefinition(badKind); err == nil {
		t.Fatal("unknown metric ref kind must be rejected at input normalization")
	} else {
		savedErrorCode(t, err, "INVALID_METRIC_REFERENCE")
	}
}

func TestResolveCompositeMetricReferenceBuildsMarkerShell(t *testing.T) {
	formula := referencedFormulaVersion()
	resolved, err := ResolveCompositeMetricReference(compositeRefDefinition(), formula)
	if err != nil {
		t.Fatal(err)
	}
	if resolved.MetricRef == nil || !resolved.MetricRef.IsComposite() || resolved.MetricRef.Version != 2 {
		t.Fatalf("composite provenance lost: %+v", resolved.MetricRef)
	}
	if resolved.Query.Kind != analysis.QueryKindTrend || len(resolved.Query.EventVersions) != 0 ||
		len(resolved.Query.Metrics) != 0 || resolved.Query.Interval != "" ||
		len(resolved.Query.GroupBy) != 0 || resolved.Query.Filter != nil {
		t.Fatalf("composite query must be an empty trend marker shell: %+v", resolved.Query)
	}
	// The stored/read normalizer must accept the resolved shape.
	stored, err := NormalizeStoredSavedDefinition(resolved)
	if err != nil {
		t.Fatalf("stored composite definition rejected: %v", err)
	}
	if !stored.MetricRef.IsComposite() {
		t.Fatal("stored normalizer must dispatch composite refs to the composite normalizer")
	}
	if !savedEqual(resolved, stored) {
		t.Fatal("stored normalization diverged from resolved definition")
	}
}

func TestResolveCompositeRejectsMismatchAndAtomicInput(t *testing.T) {
	formula := referencedFormulaVersion()

	wrongVersion := formula
	wrongVersion.Version = 9
	if _, err := ResolveCompositeMetricReference(compositeRefDefinition(), wrongVersion); err == nil {
		t.Fatal("version mismatch must fail")
	} else {
		savedErrorCode(t, err, "METRIC_VERSION_NOT_FOUND")
	}

	noProject := formula
	noProject.ProjectID = ""
	if _, err := ResolveCompositeMetricReference(compositeRefDefinition(), noProject); err == nil {
		t.Fatal("missing project must fail")
	} else {
		savedErrorCode(t, err, "METRIC_VERSION_NOT_FOUND")
	}

	// An atomic reference cannot be resolved through the composite path.
	if _, err := ResolveCompositeMetricReference(metricRefDefinition(), formula); err == nil {
		t.Fatal("atomic ref must be rejected by composite resolver")
	} else {
		savedErrorCode(t, err, "INVALID_METRIC_REFERENCE")
	}
}

func TestCompositeMarkerShellRejectsAtomicBody(t *testing.T) {
	resolved, err := ResolveCompositeMetricReference(compositeRefDefinition(), referencedFormulaVersion())
	if err != nil {
		t.Fatal(err)
	}
	resolved.Query.Metrics = []analysis.Metric{{Op: analysis.OpCount, Accuracy: analysis.AccuracyExact}}
	if _, err = normalizeResolvedCompositeDefinition(resolved); err == nil {
		t.Fatal("composite marker shell must reject an atomic metric body")
	} else {
		savedErrorCode(t, err, "INVALID_METRIC_REFERENCE")
	}
}

func TestDashboardAcceptsCompositeMetricRefButRejectsUnknownKind(t *testing.T) {
	widget := DashboardWidget{
		ID:           dashboardWidgetOne,
		Title:        "Composite widget",
		ReadingOrder: 0,
		Layout:       DashboardLayout{X: 0, Y: 0, Width: 4, Height: 4},
		MetricRef:    &MetricReference{MetricID: testFormulaID, Version: 2, Kind: MetricRefKindComposite},
	}
	def := DashboardDefinition{Title: "Board", Description: "d", Widgets: []DashboardWidget{widget}}
	normalized, err := NormalizeDashboardDefinition(def)
	if err != nil {
		t.Fatalf("composite metric refs must be accepted by the dashboard normalizer: %v", err)
	}
	if normalized.Widgets[0].MetricRef == nil || !normalized.Widgets[0].MetricRef.IsComposite() {
		t.Fatalf("composite kind lost during normalization: %+v", normalized.Widgets[0].MetricRef)
	}

	badKind := widget
	badKind.MetricRef = &MetricReference{MetricID: testFormulaID, Version: 2, Kind: "nested"}
	if _, err = NormalizeDashboardDefinition(DashboardDefinition{Title: "Board", Description: "d", Widgets: []DashboardWidget{badKind}}); err == nil {
		t.Fatal("unknown metric ref kind must fail closed")
	} else {
		savedErrorCode(t, err, "INVALID_METRIC_REFERENCE")
	}
}
