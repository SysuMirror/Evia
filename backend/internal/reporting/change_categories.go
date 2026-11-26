package reporting

// Diff change category vocabulary. These constants are the single Go-side
// source of truth for the category values emitted by semantic diff builders;
// contract alignment tests compare the slices below against the embedded
// OpenAPI enums so a new category cannot ship on only one side.
const (
	// Shared across resources.
	ChangeCategoryDefinition = "definition"
	ChangeCategoryMetadata   = "metadata"

	// Saved analysis query block and references.
	ChangeCategoryKind          = "kind"
	ChangeCategoryEventVersions = "event_versions"
	ChangeCategoryFilter        = "filter"
	ChangeCategorySubject       = "subject"
	ChangeCategoryMetrics       = "metrics"
	ChangeCategoryGroupBy       = "group_by"
	ChangeCategoryInterval      = "interval"
	ChangeCategoryFunnel        = "funnel"
	ChangeCategoryRetention     = "retention"
	ChangeCategorySegment       = "segment"
	ChangeCategoryPath          = "path"
	ChangeCategoryAttribution   = "attribution"
	ChangeCategoryExperiment    = "experiment"
	ChangeCategoryLimit         = "limit"
	ChangeCategoryMetricRef     = "metric_ref"
	ChangeCategoryVisualization = "visualization"

	// Atomic metric specific.
	ChangeCategoryAggregation = "aggregation"
	ChangeCategoryUnit        = "unit"

	// Dashboard specific.
	ChangeCategoryParameters   = "parameters"
	ChangeCategoryWidgets      = "widgets"
	ChangeCategoryDependencies = "dependencies"
	ChangeCategoryLayout       = "layout"

	// Composite formula metric specific.
	ChangeCategoryFormula = "formula"
)

// Change category slices mirror the four OpenAPI *Change.category enums.
var (
	SavedAnalysisChangeCategories = []string{
		ChangeCategoryDefinition, ChangeCategoryMetadata, ChangeCategoryKind,
		ChangeCategoryEventVersions, ChangeCategoryFilter, ChangeCategorySubject,
		ChangeCategoryMetrics, ChangeCategoryGroupBy, ChangeCategoryInterval,
		ChangeCategoryFunnel, ChangeCategoryRetention, ChangeCategorySegment,
		ChangeCategoryPath, ChangeCategoryAttribution, ChangeCategoryExperiment, ChangeCategoryLimit,
		ChangeCategoryMetricRef, ChangeCategoryVisualization,
	}
	MetricChangeCategories = []string{
		ChangeCategoryDefinition, ChangeCategoryMetadata, ChangeCategoryEventVersions,
		ChangeCategoryFilter, ChangeCategorySubject, ChangeCategoryAggregation,
		ChangeCategoryUnit,
	}
	DashboardChangeCategories = []string{
		ChangeCategoryDefinition, ChangeCategoryMetadata, ChangeCategoryParameters,
		ChangeCategoryWidgets, ChangeCategoryDependencies, ChangeCategoryLayout,
	}
	FormulaMetricChangeCategories = []string{
		ChangeCategoryDefinition, ChangeCategoryMetadata, ChangeCategoryFormula,
	}
)
