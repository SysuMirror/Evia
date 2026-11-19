package reporting

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/howtomakeaname/Evia/backend/internal/analysis"
	"github.com/howtomakeaname/Evia/backend/internal/platform/fault"
)

func metricDefinition() MetricDefinition {
	return MetricDefinition{Title: "  Visits  ", Subject: analysis.SubjectUID, EventVersions: []analysis.EventVersion{{EventKey: "page_opened", SchemaVersion: 1}}, Aggregation: analysis.Metric{Op: "count"}, Unit: "event"}
}
func metricSnapshot(t *testing.T) MetricSnapshot {
	t.Helper()
	d, err := NormalizeMetricDefinition(metricDefinition())
	if err != nil {
		t.Fatal(err)
	}
	return MetricSnapshot{Draft: MetricDraft{MetricRecord: MetricRecord{ID: "metric-one", ProjectID: "project-one", Environment: "development", DraftRevision: 1}, Definition: d}, Dependencies: DependencyManifest{ProjectID: "project-one", Environment: "development", SelectionMode: "event_versions", UnfixedEventKeys: []string{}, EventVersions: []Dependency{{EventKey: "page_opened", SchemaVersion: 1, DefinitionHash: strings.Repeat("a", 64)}}}}
}
func TestMetricUnitPolicy(t *testing.T) {
	field := &analysis.Field{Kind: "property", EventKey: "page_opened", SchemaVersion: 1, Key: "score"}
	units := []string{"event", "raw_uid", "anonymous_id", "distinct_value", "number", "millisecond", "second", "byte", "unknown", ""}
	for _, tc := range []struct {
		op, subject string
		field       *analysis.Field
		valid       []string
	}{
		{"count", "uid", nil, []string{"event"}},
		{"unique", "uid", nil, []string{"raw_uid"}},
		{"unique", "anonymous", nil, []string{"anonymous_id"}},
		{"unique", "uid", field, []string{"distinct_value"}},
		{"sum", "uid", field, []string{"number", "millisecond", "second", "byte"}},
		{"avg", "uid", field, []string{"number", "millisecond", "second", "byte"}},
		{"min", "uid", field, []string{"number", "millisecond", "second", "byte"}},
		{"max", "uid", field, []string{"number", "millisecond", "second", "byte"}},
		{"p50", "uid", field, []string{"number", "millisecond", "second", "byte"}},
		{"p90", "uid", field, []string{"number", "millisecond", "second", "byte"}},
		{"p95", "uid", field, []string{"number", "millisecond", "second", "byte"}},
		{"p99", "uid", field, []string{"number", "millisecond", "second", "byte"}},
	} {
		for _, unit := range units {
			t.Run(tc.op+tc.subject+unit, func(t *testing.T) {
				d := metricDefinition()
				d.Subject = tc.subject
				d.Aggregation = analysis.Metric{Op: tc.op, Field: tc.field}
				d.Unit = unit
				_, err := NormalizeMetricDefinition(d)
				valid := false
				for _, want := range tc.valid {
					valid = valid || want == unit
				}
				if (err == nil) != valid {
					t.Fatalf("valid=%v error=%v", valid, err)
				}
			})
		}
	}
}
func TestMetricNormalizationPinsAndOwnsConfiguration(t *testing.T) {
	d := metricDefinition()
	d.EventVersions = append(d.EventVersions, analysis.EventVersion{EventKey: "a_opened", SchemaVersion: 2})
	d.Filter = &analysis.Filter{Op: "in", Field: &analysis.Field{Kind: "property", EventKey: "page_opened", SchemaVersion: 1, Key: "score", ResolvedType: "number"}, Value: []any{0, 1}}
	normalized, err := NormalizeMetricDefinition(d)
	if err != nil {
		t.Fatal(err)
	}
	if normalized.Title != "Visits" || normalized.Aggregation.Accuracy != analysis.AccuracyExact || normalized.Filter.Field.ResolvedType != "" || normalized.EventVersions[0].EventKey != "a_opened" {
		t.Fatalf("normalization %+v", normalized)
	}
	normalized.EventVersions[0].EventKey = "changed"
	normalized.Filter.Field.Key = "changed"
	normalized.Filter.Value.([]any)[0] = json.Number("99")
	if d.EventVersions[0].EventKey != "page_opened" || d.Filter.Field.Key != "score" || d.Filter.Value.([]any)[0] != 0 {
		t.Fatal("normalization shares input")
	}
	for _, mutate := range []func(*MetricDefinition){func(d *MetricDefinition) { d.EventVersions = nil }, func(d *MetricDefinition) { d.EventVersions[0].SchemaVersion = 0 }, func(d *MetricDefinition) { d.Subject = "" }, func(d *MetricDefinition) { d.Subject = analysis.SubjectResolvedPerson }, func(d *MetricDefinition) { d.Aggregation.Op = "formula" }, func(d *MetricDefinition) { d.Aggregation = analysis.Metric{Op: "sum"} }, func(d *MetricDefinition) { d.Filter.Field.SchemaVersion = 2 }} {
		bad := metricDefinition()
		bad.Filter = &analysis.Filter{Op: "eq", Field: &analysis.Field{Kind: "property", EventKey: "page_opened", SchemaVersion: 1, Key: "score"}, Value: 0}
		mutate(&bad)
		if _, err := NormalizeMetricDefinition(bad); err == nil {
			t.Fatal("invalid independent definition accepted")
		}
	}
}
func TestMetricStrictDefinitionContract(t *testing.T) {
	validator := savedValidator(t)
	raw := `{"title":"Visits","event_versions":[{"event_key":"page_opened","schema_version":1}],"subject":"uid","aggregation":{"op":"count"},"unit":"event","filter":{"op":"eq","field":{"kind":"property","event_key":"page_opened","schema_version":1,"key":"enabled"},"value":false}}`
	d, err := ParseMetricDefinition([]byte(raw), validator)
	if err != nil || d.Filter.Value != false {
		t.Fatalf("false changed %v %v", d, err)
	}
	for _, bad := range []string{raw + `{}`, strings.Replace(raw, `"title":`, `"title":"first","title":`, 1), strings.Replace(raw, `"key":"enabled"`, `"key":"enabled","resolved_type":"boolean"`, 1), strings.Replace(raw, `"unit":"event"`, `"unit":"number"`, 1), strings.Replace(raw, `"subject":"uid"`, `"subject":"resolved_person"`, 1)} {
		if _, err := ParseMetricDefinition([]byte(bad), validator); err == nil {
			t.Fatal("invalid definition accepted", bad)
		}
	}
	for _, name := range []string{"environment", "query", "visualization", "time_range", "formula", "metric_id"} {
		bad := strings.Replace(raw, `"title":`, `"`+name+`":{},"title":`, 1)
		if _, err := ParseMetricDefinition([]byte(bad), validator); err == nil {
			t.Fatal("non-atomic field accepted", name)
		}
	}
	_, err = ParseMetricDefinition([]byte(strings.Repeat(" ", MaxMetricDefinitionBytes+1)), validator)
	savedErrorCode(t, err, "BODY_TOO_LARGE")
}
func TestMetricTemplateCombinesFiltersAndOwnsRuntime(t *testing.T) {
	d := metricDefinition()
	d.Filter = &analysis.Filter{Op: "eq", Field: &analysis.Field{Kind: "property", EventKey: "page_opened", SchemaVersion: 1, Key: "enabled"}, Value: false}
	execute := MetricExecute{Interval: "day", GroupBy: []analysis.Field{{Kind: "system", Key: "event_key"}}, Filter: &analysis.Filter{Op: "eq", Field: &analysis.Field{Kind: "property", EventKey: "page_opened", SchemaVersion: 1, Key: "score"}, Value: 0}, Limit: 20}
	template, err := MetricTemplate(d, execute)
	if err != nil {
		t.Fatal(err)
	}
	if template.Filter.Op != "and" || len(template.Filter.Args) != 2 || template.Interval != "day" || template.Limit != 20 || len(template.Metrics) != 1 || template.Metrics[0].Op != "count" {
		t.Fatalf("template %+v", template)
	}
	template.Filter.Args[1].Field.Key = "changed"
	template.GroupBy[0].Key = "uid"
	if execute.Filter.Field.Key != "score" || execute.GroupBy[0].Key != "event_key" {
		t.Fatal("runtime aliases caller")
	}
	for _, bad := range []MetricExecute{{Interval: "week"}, {Interval: "hour", Limit: 501}, {Interval: "day", GroupBy: []analysis.Field{{Kind: "property", EventKey: "page_opened", SchemaVersion: 2, Key: "score"}}}} {
		if _, err := MetricTemplate(d, bad); err == nil {
			t.Fatal("invalid runtime accepted", bad)
		}
	}
}
func TestMetricValidationBindsScopeRevisionDependenciesAndSemanticDiff(t *testing.T) {
	snapshot := metricSnapshot(t)
	original, err := BuildMetricValidation(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if !original.Valid || original.ExecutionPerformed || len(original.Diff) != 1 {
		t.Fatalf("validation %+v", original)
	}
	for _, mutate := range []func(*MetricSnapshot){func(s *MetricSnapshot) { s.Draft.ID = "metric-two" }, func(s *MetricSnapshot) { s.Draft.ProjectID = "project-two"; s.Dependencies.ProjectID = "project-two" }, func(s *MetricSnapshot) { s.Draft.Environment = "staging"; s.Dependencies.Environment = "staging" }, func(s *MetricSnapshot) { s.Draft.DraftRevision++ }, func(s *MetricSnapshot) { s.Dependencies.EventVersions[0].DefinitionHash = strings.Repeat("b", 64) }} {
		changed := metricSnapshot(t)
		mutate(&changed)
		got, err := BuildMetricValidation(changed)
		if err != nil {
			t.Fatal(err)
		}
		if got.ValidationHash == original.ValidationHash {
			t.Fatal("scope/revision/dependencies omitted from digest")
		}
	}
	snapshot.Draft.PublishedVersion = 1
	snapshot.Baseline = &MetricVersion{MetricID: snapshot.Draft.ID, ProjectID: snapshot.Draft.ProjectID, Environment: snapshot.Draft.Environment, Version: 1, Definition: snapshot.Draft.Definition}
	snapshot.Draft.Definition.Subject = analysis.SubjectAnonymous
	snapshot.Draft.Definition.Aggregation = analysis.Metric{Op: "unique"}
	snapshot.Draft.Definition.Unit = "anonymous_id"
	got, err := BuildMetricValidation(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	categories := map[string]bool{}
	for _, change := range got.Diff {
		categories[change.Category] = true
	}
	if len(got.Diff) != 3 || !categories["subject"] || !categories["aggregation"] || !categories["unit"] {
		t.Fatalf("diff %+v", got.Diff)
	}
	snapshot.Baseline.Environment = "production"
	_, err = BuildMetricValidation(snapshot)
	savedErrorCode(t, err, "PUBLISHED_VERSION_CONFLICT")
	for _, mutate := range []func(*MetricSnapshot){func(s *MetricSnapshot) { s.Dependencies.ProjectID = "other" }, func(s *MetricSnapshot) { s.Dependencies.UnfixedEventKeys = []string{"page_opened"} }, func(s *MetricSnapshot) { s.Dependencies.EventVersions[0].SchemaVersion = 2 }, func(s *MetricSnapshot) { s.Dependencies.EventVersions[0].DefinitionHash = "bad" }} {
		bad := metricSnapshot(t)
		mutate(&bad)
		if _, err := BuildMetricValidation(bad); err == nil {
			t.Fatal("invalid dependency accepted")
		}
	}
}

type metricServiceStore struct {
	MetricStore
	calls, limit int
}

func (s *metricServiceStore) CreateMetric(_ context.Context, _, _, _ string, in MetricCreate) (MetricDraft, error) {
	s.calls++
	return MetricDraft{Definition: in.Definition}, nil
}
func (s *metricServiceStore) ListMetrics(_ context.Context, _, _, _, _ string, limit int, _ bool, _ string) ([]MetricRecord, error) {
	s.calls++
	s.limit = limit
	return nil, nil
}
func (s *metricServiceStore) EnqueueMetricVersion(_ context.Context, _, _, _ string, _ int64, _ string, _ MetricExecute) (analysis.Job, error) {
	s.calls++
	return analysis.Job{ID: "query-one"}, nil
}
func TestMetricServiceBoundsAndAvailabilityBeforeWrites(t *testing.T) {
	store := &metricServiceStore{}
	service := NewMetrics(store, savedValidator(t), true)
	created, err := service.CreateMetric(context.Background(), "actor", "project", "key", MetricCreate{Environment: "development", Definition: metricDefinition()})
	if err != nil || created.Definition.Title != "Visits" {
		t.Fatalf("create %+v %v", created, err)
	}
	_, err = service.ListMetrics(context.Background(), "actor", "project", "development", "", 100, false, "")
	if err != nil || store.limit != 101 {
		t.Fatalf("pagination %d %v", store.limit, err)
	}
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	valid := MetricExecute{TimeRange: analysis.TimeRange{From: now, To: now.Add(time.Hour)}, Timezone: "UTC", Interval: "hour"}
	for _, mutate := range []func(*MetricExecute){func(e *MetricExecute) { e.Interval = "week" }, func(e *MetricExecute) { e.Timezone = "Local" }, func(e *MetricExecute) { e.TimeRange.To = e.TimeRange.From.Add(32 * 24 * time.Hour) }, func(e *MetricExecute) { e.Limit = 501 }} {
		bad := valid
		mutate(&bad)
		calls := store.calls
		if _, err := service.EnqueueMetricVersion(context.Background(), "actor", "project", "metric", 1, "key", bad); err == nil || store.calls != calls {
			t.Fatal("invalid execution reached store")
		}
	}
	service.executionAvailable = false
	_, err = service.EnqueueMetricVersion(context.Background(), "actor", "project", "metric", 1, "key", valid)
	if err == nil || fault.Public(err).Code != "QUERY_STORAGE_UNAVAILABLE" {
		t.Fatal(err)
	}
	service.executionAvailable = true
	if _, err := service.EnqueueMetricVersion(context.Background(), "actor", "project", "metric", 1, "key", valid); err != nil {
		t.Fatal(err)
	}
}

func TestMetricWholeWindowUniqueKeepsExactSubject(t *testing.T) {
	d := metricDefinition()
	d.Aggregation = analysis.Metric{Op: "unique"}
	d.Unit = "raw_uid"
	template, err := MetricTemplate(d, MetricExecute{})
	if err != nil {
		t.Fatal(err)
	}
	from := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	request, err := template.Bind("development", analysis.TimeRange{From: from, To: from.Add(7 * 24 * time.Hour)}, "UTC")
	if err != nil {
		t.Fatal(err)
	}
	if request.Interval != "" || len(request.GroupBy) != 0 || request.Subject != analysis.SubjectUID || len(request.Metrics) != 1 || request.Metrics[0].Op != "unique" || request.Metrics[0].Accuracy != analysis.AccuracyExact {
		t.Fatalf("whole-window unique %+v", request)
	}
}
