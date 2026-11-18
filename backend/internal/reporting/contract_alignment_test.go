package reporting

import (
	"encoding/json"
	"sort"
	"testing"

	"github.com/howtomakeaname/Evia/backend/internal/gen/wire"
)

// loadQueryRequestContract parses the embedded OpenAPI bundle's QueryRequest
// schema, which inlines the shared query-request JSON Schema including its
// closed vocabularies.
func loadQueryRequestContract(t *testing.T) map[string]any {
	t.Helper()
	var bundle struct {
		Defs map[string]map[string]any `json:"$defs"`
	}
	if err := json.Unmarshal(wire.Schemas, &bundle); err != nil {
		t.Fatal(err)
	}
	query, ok := bundle.Defs["QueryRequest"]
	if !ok {
		t.Fatal("embedded bundle missing QueryRequest schema")
	}
	return query
}

func contractEnum(t *testing.T, query map[string]any, property string) []string {
	t.Helper()
	props, ok := query["properties"].(map[string]any)
	if !ok {
		t.Fatal("QueryRequest schema missing properties")
	}
	node, ok := props[property].(map[string]any)
	if !ok {
		t.Fatalf("property %s missing from QueryRequest schema", property)
	}
	raw, ok := node["enum"].([]any)
	if !ok {
		t.Fatalf("property %s has no enum vocabulary", property)
	}
	out := make([]string, 0, len(raw))
	for _, value := range raw {
		text, ok := value.(string)
		if !ok {
			t.Fatalf("property %s enum contains non-string value", property)
		}
		out = append(out, text)
	}
	return out
}

func assertSameSet(t *testing.T, label string, want, got []string) {
	t.Helper()
	a := append([]string(nil), want...)
	b := append([]string(nil), got...)
	sort.Strings(a)
	sort.Strings(b)
	if len(a) != len(b) {
		t.Fatalf("%s drift: contract %v != Go %v", label, b, a)
	}
	for i := range a {
		if a[i] != b[i] {
			t.Fatalf("%s drift: contract %v != Go %v", label, b, a)
		}
	}
}

func assertSubset(t *testing.T, label string, subset, superset []string) {
	t.Helper()
	allowed := map[string]bool{}
	for _, value := range superset {
		allowed[value] = true
	}
	for _, value := range subset {
		if !allowed[value] {
			t.Fatalf("%s drift: Go value %q absent from contract %v", label, value, superset)
		}
	}
}

// TestDiscoveryVocabularyMatchesContract keeps the hardcoded discovery
// capabilities aligned with the embedded query contract, so adding or
// removing a query kind/interval/aggregation on one side fails the build
// until both sides move together.
func TestDiscoveryVocabularyMatchesContract(t *testing.T) {
	query := loadQueryRequestContract(t)

	assertSameSet(t, "query kind", discoveryQueryKinds, contractEnum(t, query, "kind"))
	assertSameSet(t, "interval", discoveryIntervals, contractEnum(t, query, "interval"))

	props := query["properties"].(map[string]any)
	retention := props["retention"].(map[string]any)["properties"].(map[string]any)
	granularity := retention["granularity"].(map[string]any)["enum"].([]any)
	contractGranularities := make([]string, 0, len(granularity))
	for _, value := range granularity {
		contractGranularities = append(contractGranularities, value.(string))
	}
	assertSameSet(t, "retention granularity", discoveryRetentionGranularities, contractGranularities)

	funnel := props["funnel"].(map[string]any)["properties"].(map[string]any)
	mode, ok := funnel["mode"].(map[string]any)["const"].(string)
	if !ok || len(discoveryFunnelModes) != 1 || discoveryFunnelModes[0] != mode {
		t.Fatalf("funnel mode drift: contract const %q != Go %v", mode, discoveryFunnelModes)
	}

	// resolved_person is an intentionally reserved contract subject; the
	// discovery list must stay a subset rather than advertise it.
	assertSubset(t, "subject", discoverySubjects, contractEnum(t, query, "subject"))

	metrics := props["metrics"].(map[string]any)["items"].(map[string]any)["properties"].(map[string]any)
	opEnum := metrics["op"].(map[string]any)["enum"].([]any)
	contractAggregations := make([]string, 0, len(opEnum))
	for _, value := range opEnum {
		contractAggregations = append(contractAggregations, value.(string))
	}
	assertSameSet(t, "aggregation", discoveryAggregations, contractAggregations)
	// Every approximate aggregation must be a member of the full operator set.
	assertSubset(t, "approximate aggregation", discoveryApproximateAggregations, discoveryAggregations)
}

// TestFormulaAndComparisonVocabularyMatchesContract guards the formula
// expression operators and dashboard comparison calendar units against the
// component schemas in the embedded bundle.
func TestFormulaAndComparisonVocabularyMatchesContract(t *testing.T) {
	var bundle struct {
		Defs map[string]map[string]any `json:"$defs"`
	}
	if err := json.Unmarshal(wire.Schemas, &bundle); err != nil {
		t.Fatal(err)
	}
	formulaOps := bundle.Defs["FormulaExpression"]["properties"].(map[string]any)["op"].(map[string]any)["enum"].([]any)
	gotOps := make([]string, 0, len(formulaOps))
	for _, value := range formulaOps {
		gotOps = append(gotOps, value.(string))
	}
	wantOps := []string{
		FormulaConstant, FormulaMetric, FormulaAdd, FormulaSubtract,
		FormulaMultiply, FormulaDivide, FormulaConvert,
	}
	assertSameSet(t, "formula op", wantOps, gotOps)

	units := bundle.Defs["ComparisonSpec"]["properties"].(map[string]any)["unit"].(map[string]any)["enum"].([]any)
	gotUnits := make([]string, 0, len(units))
	for _, value := range units {
		gotUnits = append(gotUnits, value.(string))
	}
	wantUnits := []string{
		ComparisonPreviousPeriod, ComparisonDay, ComparisonWeek, ComparisonMonth, ComparisonYear,
	}
	assertSameSet(t, "comparison unit", wantUnits, gotUnits)
}

// TestSelectionModeVocabularyMatchesContract keeps the eight dependency
// selection modes aligned with the QueryDependencyManifest.selection_mode
// enum, covering both the reporting policy builders and the Postgres
// manifest construction that consume these constants.
func TestSelectionModeVocabularyMatchesContract(t *testing.T) {
	var bundle struct {
		Defs map[string]map[string]any `json:"$defs"`
	}
	if err := json.Unmarshal(wire.Schemas, &bundle); err != nil {
		t.Fatal(err)
	}
	props, ok := bundle.Defs["QueryDependencyManifest"]["properties"].(map[string]any)
	if !ok {
		t.Fatal("QueryDependencyManifest schema missing properties")
	}
	raw, ok := props["selection_mode"].(map[string]any)["enum"].([]any)
	if !ok {
		t.Fatal("selection_mode enum missing from embedded bundle")
	}
	got := make([]string, 0, len(raw))
	for _, value := range raw {
		got = append(got, value.(string))
	}
	assertSameSet(t, "selection mode", SelectionModes, got)
}

// TestParameterVocabularyMatchesContract guards dashboard parameter scalar
// types and the operator union against the ParameterDeclaration schema.
func TestParameterVocabularyMatchesContract(t *testing.T) {
	var bundle struct {
		Defs map[string]map[string]any `json:"$defs"`
	}
	if err := json.Unmarshal(wire.Schemas, &bundle); err != nil {
		t.Fatal(err)
	}
	props, ok := bundle.Defs["ParameterDeclaration"]["properties"].(map[string]any)
	if !ok {
		t.Fatal("ParameterDeclaration schema missing properties")
	}
	enumOf := func(property string) []string {
		raw, ok := props[property].(map[string]any)["enum"].([]any)
		if !ok {
			t.Fatalf("ParameterDeclaration.%s enum missing", property)
		}
		out := make([]string, 0, len(raw))
		for _, value := range raw {
			out = append(out, value.(string))
		}
		return out
	}
	assertSameSet(t, "parameter type", ParameterTypes, enumOf("type"))
	assertSameSet(t, "parameter operator", ParameterOperators, enumOf("operator"))
}

// TestVisualizationVocabularyMatchesContract guards the five saved analysis
// visualization kinds against the SavedAnalysisVisualization schema.
func TestVisualizationVocabularyMatchesContract(t *testing.T) {
	var bundle struct {
		Defs map[string]map[string]any `json:"$defs"`
	}
	if err := json.Unmarshal(wire.Schemas, &bundle); err != nil {
		t.Fatal(err)
	}
	props, ok := bundle.Defs["SavedAnalysisVisualization"]["properties"].(map[string]any)
	if !ok {
		t.Fatal("SavedAnalysisVisualization schema missing properties")
	}
	raw, ok := props["kind"].(map[string]any)["enum"].([]any)
	if !ok {
		t.Fatal("SavedAnalysisVisualization.kind enum missing")
	}
	got := make([]string, 0, len(raw))
	for _, value := range raw {
		got = append(got, value.(string))
	}
	assertSameSet(t, "visualization kind", VisualizationKinds, got)
}

// TestDashboardRunStateVocabularyMatchesContract guards the seven dashboard
// run aggregate states produced by DashboardRunState against the
// DashboardRun.state enum. Widget query-job states are a separate six-value
// vocabulary (QueryJob.state) and intentionally not part of this set because
// partial exists only at the run aggregate level.
func TestDashboardRunStateVocabularyMatchesContract(t *testing.T) {
	var bundle struct {
		Defs map[string]map[string]any `json:"$defs"`
	}
	if err := json.Unmarshal(wire.Schemas, &bundle); err != nil {
		t.Fatal(err)
	}
	props, ok := bundle.Defs["DashboardRun"]["properties"].(map[string]any)
	if !ok {
		t.Fatal("DashboardRun schema missing properties")
	}
	raw, ok := props["state"].(map[string]any)["enum"].([]any)
	if !ok {
		t.Fatal("DashboardRun.state enum missing")
	}
	got := make([]string, 0, len(raw))
	for _, value := range raw {
		got = append(got, value.(string))
	}
	assertSameSet(t, "dashboard run state", DashboardRunStates, got)
}

// TestFormulaUnitVocabularyMatchesContract guards the measurement unit closed
// set and the convertible target-unit pair against the formula/metric schemas.
func TestFormulaUnitVocabularyMatchesContract(t *testing.T) {
	var bundle struct {
		Defs map[string]map[string]any `json:"$defs"`
	}
	if err := json.Unmarshal(wire.Schemas, &bundle); err != nil {
		t.Fatal(err)
	}
	enumOf := func(schema, property string) []string {
		props, ok := bundle.Defs[schema]["properties"].(map[string]any)
		if !ok {
			t.Fatalf("%s schema missing properties", schema)
		}
		raw, ok := props[property].(map[string]any)["enum"].([]any)
		if !ok {
			t.Fatalf("%s.%s enum missing", schema, property)
		}
		out := make([]string, 0, len(raw))
		for _, value := range raw {
			out = append(out, value.(string))
		}
		return out
	}
	assertSameSet(t, "metric unit", KnownUnits, enumOf("MetricDefinition", "unit"))
	assertSameSet(t, "formula unit", KnownUnits, enumOf("FormulaDefinition", "unit"))
	assertSameSet(t, "convert target unit",
		[]string{UnitMillisecond, UnitSecond}, enumOf("FormulaExpression", "target_unit"))
}
