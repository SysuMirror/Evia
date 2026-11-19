package reporting

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/howtomakeaname/Evia/backend/internal/analysis"
)

const testMetricID = "11111111-1111-4111-8111-111111111111"

func metricRefDefinition() SavedAnalysisDefinition {
	return SavedAnalysisDefinition{
		Title:         "Referenced metric card",
		MetricRef:     &MetricReference{MetricID: testMetricID, Version: 1},
		Visualization: SavedAnalysisVisualization{Kind: "metric"},
	}
}

func referencedMetricVersion(t *testing.T) MetricVersion {
	t.Helper()
	d, err := NormalizeMetricDefinition(metricDefinition())
	if err != nil {
		t.Fatal(err)
	}
	return MetricVersion{
		MetricID:       testMetricID,
		ProjectID:      "project-one",
		Environment:    "development",
		Version:        1,
		Definition:     d,
		DefinitionHash: strings.Repeat("a", 64),
	}
}

func TestMetricReferenceInputIsAcceptedOnlyWithoutInlineQuery(t *testing.T) {
	normalized, err := NormalizeSavedAnalysisDefinition(metricRefDefinition())
	if err != nil {
		t.Fatal(err)
	}
	if normalized.MetricRef == nil || normalized.MetricRef.MetricID != testMetricID || normalized.MetricRef.Version != 1 {
		t.Fatalf("metric reference lost: %+v", normalized.MetricRef)
	}
	if normalized.Query.Kind != "" {
		t.Fatal("unresolved input must not carry an inline query")
	}

	badID := metricRefDefinition()
	badID.MetricRef.MetricID = "not-a-uuid"
	_, err = NormalizeSavedAnalysisDefinition(badID)
	savedErrorCode(t, err, "INVALID_METRIC_REFERENCE")

	badVersion := metricRefDefinition()
	badVersion.MetricRef.Version = 0
	_, err = NormalizeSavedAnalysisDefinition(badVersion)
	savedErrorCode(t, err, "INVALID_METRIC_REFERENCE")

	both := metricRefDefinition()
	both.Query.Kind = analysis.QueryKindTrend
	both.Query.EventVersions = []analysis.EventVersion{{EventKey: "page_opened", SchemaVersion: 1}}
	_, err = NormalizeSavedAnalysisDefinition(both)
	savedErrorCode(t, err, "INVALID_METRIC_REFERENCE")

	for _, kind := range []string{"line", "bar", "table", "funnel"} {
		unsupported := metricRefDefinition()
		unsupported.Visualization.Kind = kind
		_, err = NormalizeSavedAnalysisDefinition(unsupported)
		savedErrorCode(t, err, "INVALID_VISUALIZATION")
	}
}

func TestResolveMetricReferenceDerivesImmutableTrendCard(t *testing.T) {
	metric := referencedMetricVersion(t)
	resolved, err := ResolveMetricReference(metricRefDefinition(), metric)
	if err != nil {
		t.Fatal(err)
	}
	if resolved.MetricRef == nil || resolved.MetricRef.Version != 1 {
		t.Fatal("provenance reference lost after resolution")
	}
	if resolved.Query.Kind != analysis.QueryKindTrend || len(resolved.Query.Metrics) != 1 || resolved.Query.Interval != "" || len(resolved.Query.GroupBy) != 0 {
		t.Fatalf("derived query is not a whole-range single metric card: %+v", resolved.Query)
	}
	if len(resolved.Query.EventVersions) != 1 || resolved.Query.EventVersions[0].EventKey != "page_opened" {
		t.Fatalf("derived query does not pin the metric event version: %+v", resolved.Query.EventVersions)
	}
	encoded, err := json.Marshal(resolved)
	if err != nil {
		t.Fatal(err)
	}
	text := string(encoded)
	if !strings.Contains(text, `"metric_ref":{"metric_id":"`+testMetricID+`","version":1}`) || !strings.Contains(text, `"query":{"kind":"trend"`) {
		t.Fatalf("resolved document missing pin or query: %s", text)
	}

	// Resolution is deterministic and stable across repeated calls.
	again, err := ResolveMetricReference(metricRefDefinition(), metric)
	if err != nil {
		t.Fatal(err)
	}
	if !savedEqual(resolved, again) {
		t.Fatal("repeated resolution diverged")
	}
}

func TestResolveMetricReferenceRejectsMismatchAndNonReference(t *testing.T) {
	metric := referencedMetricVersion(t)

	wrongVersion := metric
	wrongVersion.Version = 2
	_, err := ResolveMetricReference(metricRefDefinition(), wrongVersion)
	savedErrorCode(t, err, "METRIC_VERSION_NOT_FOUND")

	wrongID := metric
	wrongID.MetricID = "22222222-2222-4222-8222-222222222222"
	_, err = ResolveMetricReference(metricRefDefinition(), wrongID)
	savedErrorCode(t, err, "METRIC_VERSION_NOT_FOUND")

	noProject := metric
	noProject.ProjectID = ""
	_, err = ResolveMetricReference(metricRefDefinition(), noProject)
	savedErrorCode(t, err, "METRIC_VERSION_NOT_FOUND")

	inline := savedDefinition()
	_, err = ResolveMetricReference(inline, metric)
	savedErrorCode(t, err, "INVALID_METRIC_REFERENCE")
}

func metricResolvedSnapshot(t *testing.T) SavedAnalysisSnapshot {
	t.Helper()
	resolved, err := ResolveMetricReference(metricRefDefinition(), referencedMetricVersion(t))
	if err != nil {
		t.Fatal(err)
	}
	dependencies := DependencyManifest{
		ProjectID:        "project-one",
		Environment:      "development",
		SelectionMode:    "event_versions",
		UnfixedEventKeys: []string{},
		EventVersions:    []Dependency{{EventKey: "page_opened", SchemaVersion: 1, DefinitionHash: strings.Repeat("a", 64)}},
		Metric:           &MetricDependency{MetricID: testMetricID, Version: 1, DefinitionHash: strings.Repeat("a", 64)},
	}
	return SavedAnalysisSnapshot{
		Draft: SavedAnalysisDraft{
			SavedAnalysis: SavedAnalysis{ID: "analysis-metric", ProjectID: "project-one", Environment: "development", DraftRevision: 1, Title: resolved.Title, Kind: resolved.Query.Kind},
			Definition:    resolved,
		},
		Dependencies: dependencies,
	}
}

func TestMetricReferenceValidationPinsManifestAndRejectsDrift(t *testing.T) {
	snapshot := metricResolvedSnapshot(t)
	validation, err := BuildSavedAnalysisValidation(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if !validation.Valid || validation.Dependencies.Metric == nil || validation.Dependencies.Metric.DefinitionHash != strings.Repeat("a", 64) {
		t.Fatalf("metric pin missing from validated dependencies: %+v", validation.Dependencies)
	}
	if len(validation.Diff) != 1 || validation.Diff[0].Path != "/definition" {
		t.Fatalf("first publication should show one definition change: %+v", validation.Diff)
	}

	for _, change := range []func(*SavedAnalysisSnapshot){
		func(s *SavedAnalysisSnapshot) { s.Dependencies.Metric = nil },
		func(s *SavedAnalysisSnapshot) { s.Dependencies.Metric.Version = 2 },
		func(s *SavedAnalysisSnapshot) { s.Dependencies.Metric.DefinitionHash = "fake" },
		func(s *SavedAnalysisSnapshot) {
			s.Dependencies.Metric.MetricID = "22222222-2222-4222-8222-222222222222"
		},
		func(s *SavedAnalysisSnapshot) { s.Dependencies.EventVersions = nil },
	} {
		drifted := metricResolvedSnapshot(t)
		change(&drifted)
		_, err = BuildSavedAnalysisValidation(drifted)
		savedErrorCode(t, err, "INVALID_ANALYSIS_DEPENDENCIES")
	}

	// An inline analysis must never carry a metric dependency.
	inline := savedSnapshot(t, savedDefinition())
	inline.Dependencies.Metric = &MetricDependency{MetricID: testMetricID, Version: 1, DefinitionHash: strings.Repeat("a", 64)}
	_, err = BuildSavedAnalysisValidation(inline)
	savedErrorCode(t, err, "INVALID_ANALYSIS_DEPENDENCIES")
}

func TestMetricReferenceDiffAndInlineBackwardCompatibility(t *testing.T) {
	snapshot := metricResolvedSnapshot(t)
	snapshot.Draft.PublishedVersion = 1
	snapshot.Baseline = &SavedAnalysisVersion{
		AnalysisID: snapshot.Draft.ID, ProjectID: snapshot.Draft.ProjectID, Environment: snapshot.Draft.Environment,
		Version: 1, Definition: savedDefinition(),
	}
	validation, err := BuildSavedAnalysisValidation(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	var hasMetricRefChange bool
	for _, change := range validation.Diff {
		if change.Path == "/metric_ref" {
			hasMetricRefChange = true
		}
	}
	if !hasMetricRefChange {
		t.Fatalf("expected a /metric_ref diff entry: %+v", validation.Diff)
	}

	// Existing inline definitions remain byte-compatible and never emit metric_ref.
	inline, err := NormalizeSavedAnalysisDefinition(savedDefinition())
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(inline)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "metric_ref") {
		t.Fatalf("inline definition must not serialize metric_ref: %s", encoded)
	}
}

func TestSavedDefinitionMarshalJSONPreservesStorageContract(t *testing.T) {
	mv := MetricVersion{
		MetricID: testMetricID, ProjectID: "p", Environment: "development", Version: 1,
		Definition: metricDefinition(),
	}
	// A metric write input omits the derived query but must keep the description
	// key required by the saved_analyses storage check.
	input := SavedAnalysisDefinition{
		Title:         "调试",
		MetricRef:     &MetricReference{MetricID: testMetricID, Version: 1},
		Visualization: SavedAnalysisVisualization{Kind: "metric"},
	}
	rawInput, e := json.Marshal(input)
	if e != nil {
		t.Fatal(e)
	}
	var inputMap map[string]any
	if e = json.Unmarshal(rawInput, &inputMap); e != nil {
		t.Fatal(e)
	}
	if _, ok := inputMap["query"]; ok {
		t.Fatalf("metric write input must omit the query: %s", rawInput)
	}
	if _, ok := inputMap["description"]; !ok {
		t.Fatalf("description key must always be present for the storage check: %s", rawInput)
	}

	// After resolution the derived query is included alongside the pin.
	resolved, err := ResolveMetricReference(input, mv)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	resolvedBytes, e := json.Marshal(resolved)
	if e != nil {
		t.Fatal(e)
	}
	var resolvedMap map[string]any
	if e = json.Unmarshal(resolvedBytes, &resolvedMap); e != nil {
		t.Fatal(e)
	}
	query, ok := resolvedMap["query"].(map[string]any)
	if !ok || query["kind"] != "trend" {
		t.Fatalf("resolved definition must carry the derived query: %s", resolvedBytes)
	}
	if _, ok = resolvedMap["metric_ref"]; !ok {
		t.Fatalf("resolved definition must retain the metric pin: %s", resolvedBytes)
	}

	// Inline definitions keep the exact same representation (no metric_ref, query present).
	inline := savedDefinition()
	inlineBytes, e := json.Marshal(inline)
	if e != nil {
		t.Fatal(e)
	}
	var inlineMap map[string]any
	if e = json.Unmarshal(inlineBytes, &inlineMap); e != nil {
		t.Fatal(e)
	}
	if _, ok = inlineMap["metric_ref"]; ok {
		t.Fatalf("inline definition must not carry a metric_ref: %s", inlineBytes)
	}
	if _, ok = inlineMap["query"]; !ok {
		t.Fatalf("inline definition must carry its query: %s", inlineBytes)
	}
}
