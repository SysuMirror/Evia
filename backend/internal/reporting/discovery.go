// Package reporting owns reusable analysis configuration and its discovery policy.
package reporting

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/howtomakeaname/Evia/backend/internal/analysis"
	"github.com/howtomakeaname/Evia/backend/internal/platform/canonical"
	"github.com/howtomakeaname/Evia/backend/internal/platform/env"
	"github.com/howtomakeaname/Evia/backend/internal/platform/fault"
)

type FieldCapability struct {
	Type             string   `json:"type"`
	FilterOperators  []string `json:"filter_operators"`
	Groupable        bool     `json:"groupable"`
	MetricOperations []string `json:"metric_operations"`
}
type DiscoveryBudgets struct {
	TrendWindowSeconds        int `json:"trend_window_seconds"`
	OtherWindowSeconds        int `json:"other_window_seconds"`
	EventSelections           int `json:"event_selections"`
	GroupFields               int `json:"group_fields"`
	Metrics                   int `json:"metrics"`
	ResultRows                int `json:"result_rows"`
	FilterDepth               int `json:"filter_depth"`
	FilterNodes               int `json:"filter_nodes"`
	FilterArguments           int `json:"filter_arguments"`
	FilterValues              int `json:"filter_values"`
	FunnelSteps               int `json:"funnel_steps"`
	FunnelWindowSeconds       int `json:"funnel_window_seconds"`
	ProjectRunningQueries     int `json:"project_running_queries"`
	ProjectQueuedQueries      int `json:"project_queued_queries"`
	RetentionMaxPeriods       int `json:"retention_max_periods"`
	RetentionMaxWindowSeconds int `json:"retention_max_window_seconds"`
}
type Capabilities struct {
	ExecutionAvailable      bool              `json:"execution_available"`
	QueryKinds              []string          `json:"query_kinds"`
	Aggregations            []string          `json:"aggregations"`
	ApproximateAggregations []string          `json:"approximate_aggregations"`
	Subjects                []string          `json:"subjects"`
	Intervals               []string          `json:"intervals"`
	FunnelModes             []string          `json:"funnel_modes"`
	RetentionGranularities  []string          `json:"retention_granularities"`
	FieldTypes              []FieldCapability `json:"field_types"`
	Budgets                 DiscoveryBudgets  `json:"budgets"`
}

// Discovery vocabulary slices are the single Go-side source of truth for
// the closed sets advertised by GET analysis-capabilities. Contract
// alignment tests compare them against the embedded OpenAPI bundle so a
// schema-side vocabulary change can never silently diverge from discovery.
var (
	discoveryQueryKinds              = analysis.QueryKinds
	discoveryAggregations            = analysis.Aggregations
	discoveryApproximateAggregations = analysis.ApproximateAggregations
	// resolved_person stays a contract-level reserved subject and is never
	// advertised until the identity graph exists; discovery therefore lists
	// only the implemented subjects.
	discoverySubjects               = []string{analysis.SubjectUID, analysis.SubjectAnonymous}
	discoveryIntervals              = analysis.Intervals
	discoveryFunnelModes            = analysis.FunnelModes
	discoveryRetentionGranularities = analysis.RetentionGranularities
)

type CatalogField struct {
	Field            analysis.Field `json:"field"`
	Type             string         `json:"type"`
	Required         bool           `json:"required"`
	Nullable         bool           `json:"nullable"`
	MissingSemantics string         `json:"missing_semantics"`
	NullSemantics    string         `json:"null_semantics"`
	FilterOperators  []string       `json:"filter_operators"`
	Groupable        bool           `json:"groupable"`
	MetricOperations []string       `json:"metric_operations"`
	Metadata         map[string]any `json:"metadata"`
}
type CatalogEvent struct {
	EventKey       string         `json:"event_key"`
	SchemaVersion  int64          `json:"schema_version"`
	DisplayName    string         `json:"display_name"`
	DefinitionHash string         `json:"definition_hash"`
	CreatedAt      time.Time      `json:"created_at"`
	Published      bool           `json:"published"`
	Active         bool           `json:"active"`
	IngestionReady bool           `json:"ingestion_ready"`
	Fields         []CatalogField `json:"fields"`
}

// PublishedDefinition is an adapter projection of existing catalog facts, never a
// second definition store. Definition comes from the immutable catalog version.
type PublishedDefinition struct {
	Event      CatalogEvent
	Definition map[string]any
}
type CatalogSnapshot struct {
	Revision int64
	Items    []PublishedDefinition
}
type Dependency struct {
	EventKey       string `json:"event_key"`
	SchemaVersion  int64  `json:"schema_version"`
	DefinitionHash string `json:"definition_hash"`
}

// Dependency selection mode closed set, mirrored by the
// QueryDependencyManifest.selection_mode OpenAPI enum and guarded by
// TestSelectionModeVocabularyMatchesContract.
const (
	SelectionModeEventKeys        = "event_keys"
	SelectionModeEventVersions    = "event_versions"
	SelectionModeAllEvents        = "all_events"
	SelectionModeFunnelSteps      = "funnel_steps"
	SelectionModeRetentionSteps   = "retention_steps"
	SelectionModeSegmentSteps     = "segment_steps"
	SelectionModePathSteps        = "path_steps"
	SelectionModeAttributionSteps = "attribution_steps"
)

// SelectionModes is the single Go-side source of truth for the eight
// dependency selection modes.
var SelectionModes = []string{
	SelectionModeEventKeys,
	SelectionModeEventVersions,
	SelectionModeAllEvents,
	SelectionModeFunnelSteps,
	SelectionModeRetentionSteps,
	SelectionModeSegmentSteps,
	SelectionModePathSteps,
	SelectionModeAttributionSteps,
}

type DependencyManifest struct {
	ProjectID        string       `json:"project_id"`
	Environment      string       `json:"environment"`
	SelectionMode    string       `json:"selection_mode"`
	UnfixedEventKeys []string     `json:"unfixed_event_keys"`
	EventVersions    []Dependency `json:"event_versions"`
	// Metric is present only for a saved analysis whose source is an immutable
	// metric version. It pins the exact metric content so republishing the
	// metric never implicitly changes an already published analysis.
	Metric *MetricDependency `json:"metric,omitempty"`
}

// MetricDependency is the immutable metric pin recorded for a metric-referencing
// saved analysis. DefinitionHash matches the referenced metric version's digest.
type MetricDependency struct {
	MetricID       string `json:"metric_id"`
	Version        int64  `json:"version"`
	DefinitionHash string `json:"definition_hash"`
}
type ResolvedValidation struct {
	Request         analysis.Request
	CatalogRevision int64
	Dependencies    []Dependency
}
type QueryValidation struct {
	Valid              bool               `json:"valid"`
	ExecutionPerformed bool               `json:"execution_performed"`
	ExecutionAvailable bool               `json:"execution_available"`
	CatalogRevision    int64              `json:"catalog_revision"`
	NormalizedRequest  json.RawMessage    `json:"normalized_request"`
	QueryHash          string             `json:"query_hash"`
	Dependencies       DependencyManifest `json:"dependencies"`
	DependencyHash     string             `json:"dependency_hash"`
}
type DiscoveryStore interface {
	AuthorizeAnalysisDiscovery(context.Context, string, string) error
	ListAnalysisCatalog(context.Context, string, string, string, string, int64, int) (CatalogSnapshot, error)
	ValidateAnalysisQuery(context.Context, string, string, analysis.Request) (ResolvedValidation, error)
}
type DiscoveryService interface {
	Capabilities(context.Context, string, string) (Capabilities, error)
	Catalog(context.Context, string, string, string, string, int64, int) (CatalogSnapshot, error)
	ValidateQuery(context.Context, string, string, json.RawMessage) (QueryValidation, error)
}
type Discovery struct {
	store              DiscoveryStore
	validator          analysis.Validator
	executionAvailable bool
}

func NewDiscovery(store DiscoveryStore, validator analysis.Validator, executionAvailable bool) *Discovery {
	return &Discovery{store: store, validator: validator, executionAvailable: executionAvailable}
}
func FieldCapabilities(kind string) (FieldCapability, bool) {
	c := FieldCapability{Type: kind, FilterOperators: []string{"eq", "neq", "in", "not_in", "is_missing", "is_present"}, Groupable: true, MetricOperations: []string{"unique"}}
	switch kind {
	case "string":
		c.FilterOperators = append(c.FilterOperators, "prefix", "not_prefix", "suffix", "not_suffix", "contains", "not_contains")
	case "boolean":
	case "number", "integer":
		c.FilterOperators = append(c.FilterOperators, "gt", "gte", "lt", "lte")
		c.MetricOperations = append(c.MetricOperations, "sum", "avg", "min", "max", "p50", "p90", "p95", "p99")
	case "datetime":
		c.FilterOperators = append(c.FilterOperators, "gt", "gte", "lt", "lte")
	case "array:string", "array:boolean", "array:number", "array:integer":
		c.FilterOperators = []string{"eq", "neq", "contains", "not_contains", "is_missing", "is_present"}
		c.Groupable = false
		c.MetricOperations = []string{}
	default:
		return FieldCapability{}, false
	}
	return c, true
}
func (s *Discovery) Capabilities(ctx context.Context, actor, project string) (Capabilities, error) {
	if e := s.store.AuthorizeAnalysisDiscovery(ctx, actor, project); e != nil {
		return Capabilities{}, e
	}
	out := Capabilities{
		ExecutionAvailable:      s.executionAvailable,
		QueryKinds:              discoveryQueryKinds,
		Aggregations:            discoveryAggregations,
		ApproximateAggregations: discoveryApproximateAggregations,
		Subjects:                discoverySubjects,
		Intervals:               discoveryIntervals,
		FunnelModes:             discoveryFunnelModes,
		RetentionGranularities:  discoveryRetentionGranularities,
		FieldTypes:              []FieldCapability{},
		Budgets: DiscoveryBudgets{
			TrendWindowSeconds:        analysis.BudgetTrendWindowSeconds,
			OtherWindowSeconds:        analysis.BudgetOtherWindowSeconds,
			EventSelections:           analysis.BudgetEventSelections,
			GroupFields:               analysis.BudgetGroupFields,
			Metrics:                   analysis.BudgetMetrics,
			ResultRows:                analysis.BudgetResultRows,
			FilterDepth:               analysis.BudgetFilterDepth,
			FilterNodes:               analysis.BudgetFilterNodes,
			FilterArguments:           analysis.BudgetFilterArguments,
			FilterValues:              analysis.BudgetFilterValues,
			FunnelSteps:               analysis.BudgetFunnelSteps,
			FunnelWindowSeconds:       analysis.BudgetFunnelWindowSeconds,
			ProjectRunningQueries:     analysis.BudgetProjectRunningQueries,
			ProjectQueuedQueries:      analysis.BudgetProjectQueuedQueries,
			RetentionMaxPeriods:       analysis.BudgetRetentionMaxPeriods,
			RetentionMaxWindowSeconds: analysis.BudgetRetentionMaxWindowSeconds,
		},
	}
	for _, kind := range []string{"string", "boolean", "number", "integer", "datetime", "array:string", "array:boolean", "array:number", "array:integer"} {
		c, _ := FieldCapabilities(kind)
		out.FieldTypes = append(out.FieldTypes, c)
	}
	return out, nil
}

var catalogPropertyKey = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)

func (s *Discovery) Catalog(ctx context.Context, actor, project, environment, afterKey string, afterVersion int64, limit int) (CatalogSnapshot, error) {
	if !env.Valid(environment) {
		return CatalogSnapshot{}, analysis.Invalid("INVALID_ENVIRONMENT", "环境无效", "/environment")
	}
	if limit < 1 || limit > 200 {
		return CatalogSnapshot{}, analysis.Invalid("INVALID_LIMIT", "分页大小超出范围", "/limit")
	}
	out, e := s.store.ListAnalysisCatalog(ctx, actor, project, environment, afterKey, afterVersion, limit+1)
	if e != nil {
		return CatalogSnapshot{}, e
	}
	for i := range out.Items {
		out.Items[i].Event.Fields = ProjectFields(out.Items[i])
		out.Items[i].Definition = nil
	}
	return out, nil
}

// ProjectFields deliberately mirrors the resolver's supported type subset. A
// nullable union, nested object or unconstrained array is not queryable today.
func ProjectFields(in PublishedDefinition) []CatalogField {
	schema, _ := in.Definition["schema"].(map[string]any)
	properties, _ := schema["properties"].(map[string]any)
	metadata, _ := in.Definition["field_metadata"].(map[string]any)
	required := map[string]bool{}
	if list, ok := schema["required"].([]any); ok {
		for _, v := range list {
			if key, ok := v.(string); ok {
				required[key] = true
			}
		}
	}
	keys := make([]string, 0, len(properties))
	for key := range properties {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	out := []CatalogField{}
	for _, key := range keys {
		if !catalogPropertyKey.MatchString(key) {
			continue
		}
		definition, ok := properties[key].(map[string]any)
		if !ok {
			continue
		}
		kind, _ := definition["type"].(string)
		if kind == "array" {
			items, _ := definition["items"].(map[string]any)
			item, _ := items["type"].(string)
			kind = "array:" + item
		}
		// datetime is a server system type; a property format does not change its string type.
		if kind == "datetime" {
			continue
		}
		capability, ok := FieldCapabilities(kind)
		if !ok {
			continue
		}
		meta, _ := metadata[key].(map[string]any)
		if meta == nil {
			meta = map[string]any{}
		}
		out = append(out, CatalogField{Field: analysis.Field{Kind: "property", EventKey: in.Event.EventKey, SchemaVersion: in.Event.SchemaVersion, Key: key}, Type: kind, Required: required[key], Nullable: false, MissingSemantics: CatalogMissingSemanticsAbsentKey, NullSemantics: CatalogNullSemanticsPresentButNotATypedValue, FilterOperators: capability.FilterOperators, Groupable: capability.Groupable, MetricOperations: capability.MetricOperations, Metadata: meta})
	}
	return out
}
func (s *Discovery) ValidateQuery(ctx context.Context, actor, project string, raw json.RawMessage) (QueryValidation, error) {
	r, e := analysis.Parse(raw, s.validator)
	if e != nil {
		return QueryValidation{}, e
	}
	resolved, e := s.store.ValidateAnalysisQuery(ctx, actor, project, r)
	if e != nil {
		return QueryValidation{}, validationFieldError(e, r)
	}
	// Clone and remove internal resolved types before producing a valid public request.
	normalized := resolved.Request
	data, e := json.Marshal(normalized)
	if e != nil {
		return QueryValidation{}, e
	}
	var safe analysis.Request
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	if e = decoder.Decode(&safe); e != nil {
		return QueryValidation{}, e
	}
	if e = analysis.WalkFields(&safe, func(f *analysis.Field) error { f.ResolvedType = ""; return nil }); e != nil {
		return QueryValidation{}, e
	}
	if safe.EventVersions != nil {
		sort.Slice(safe.EventVersions, func(i, j int) bool { return safe.EventVersions[i].EventKey < safe.EventVersions[j].EventKey })
	}
	sort.Strings(safe.EventKeys)
	data, e = canonical.JSON(safe)
	if e != nil {
		return QueryValidation{}, e
	}
	manifest := DependencyManifest{ProjectID: project, Environment: r.Environment, SelectionMode: SelectionModeAllEvents, UnfixedEventKeys: []string{}, EventVersions: resolved.Dependencies}
	if manifest.EventVersions == nil {
		manifest.EventVersions = []Dependency{}
	}
	switch {
	case r.Funnel != nil:
		manifest.SelectionMode = SelectionModeFunnelSteps
	case r.EventVersions != nil:
		manifest.SelectionMode = SelectionModeEventVersions
	case len(r.EventKeys) > 0:
		manifest.SelectionMode = SelectionModeEventKeys
		manifest.UnfixedEventKeys = append(manifest.UnfixedEventKeys, safe.EventKeys...)
	}
	sort.Slice(manifest.EventVersions, func(i, j int) bool {
		a, b := manifest.EventVersions[i], manifest.EventVersions[j]
		return a.EventKey < b.EventKey || a.EventKey == b.EventKey && a.SchemaVersion < b.SchemaVersion
	})
	queryHash, e := canonical.Hash(struct {
		ProjectID string          `json:"project_id"`
		Request   json.RawMessage `json:"request"`
	}{project, data})
	if e != nil {
		return QueryValidation{}, e
	}
	dependencyHash, e := canonical.Hash(manifest)
	if e != nil {
		return QueryValidation{}, e
	}
	return QueryValidation{Valid: true, ExecutionAvailable: s.executionAvailable, CatalogRevision: resolved.CatalogRevision, NormalizedRequest: data, QueryHash: queryHash, Dependencies: manifest, DependencyHash: dependencyHash}, nil
}

// Existing query faults without paths gain safe input section pointers here;
// never add event names, filter constants or internal database error text.
func validationFieldError(err error, r analysis.Request) error {
	var problem *fault.Error
	if !errors.As(err, &problem) || len(problem.Fields) > 0 {
		return err
	}
	var paths []string
	switch problem.Code {
	case "QUERY_EVENT_VERSION_NOT_PUBLISHED":
		if len(r.EventVersions) > 0 {
			paths = append(paths, "/event_versions")
		}
		if r.Funnel != nil {
			paths = append(paths, "/funnel/steps")
		}
	case "INVALID_METRIC_TYPE", "UNSUPPORTED_METRIC_TYPE":
		paths = []string{"/metrics"}
	case "UNSUPPORTED_GROUP_TYPE":
		paths = []string{"/group_by"}
	case "INVALID_SAME_ENTITY":
		paths = []string{"/funnel/same_entity"}
	case "INVALID_FILTER_TYPE":
		if r.Filter != nil {
			paths = append(paths, "/filter")
		}
		if r.Funnel != nil {
			paths = append(paths, "/funnel/steps")
		}
	case "QUERY_PROPERTY_NOT_DEFINED", "UNSUPPORTED_FIELD_TYPE":
		if r.Filter != nil {
			paths = append(paths, "/filter")
		}
		if len(r.GroupBy) > 0 {
			paths = append(paths, "/group_by")
		}
		if len(r.Metrics) > 0 {
			paths = append(paths, "/metrics")
		}
		if r.Funnel != nil {
			paths = append(paths, "/funnel")
		}
	}
	if len(paths) == 0 {
		return err
	}
	copy := *problem
	copy.Fields = paths
	return &copy
}

// CatalogPosition is stable by event key and numeric schema version.
func CatalogPosition(event CatalogEvent) string {
	data, _ := json.Marshal([]any{event.EventKey, event.SchemaVersion})
	return string(data)
}
func ParseCatalogPosition(value string) (string, int64, error) {
	if value == "" {
		return "", 0, nil
	}
	var p []json.RawMessage
	var key string
	var version int64
	if json.Unmarshal([]byte(value), &p) != nil || len(p) != 2 || json.Unmarshal(p[0], &key) != nil || json.Unmarshal(p[1], &version) != nil || !catalogPropertyKey.MatchString(strings.TrimPrefix(key, "sys.")) || version < 1 || version > analysis.MaxSafeInteger {
		return "", 0, analysis.Invalid("INVALID_CURSOR", "分页游标已失效，请重新加载", "/cursor")
	}
	return key, version, nil
}
