//go:build integration

package postgres

import (
	"context"
	"testing"
	"time"

	"github.com/howtomakeaname/Evia/backend/internal/analysis"
	"github.com/howtomakeaname/Evia/backend/internal/reporting"
)

// metricBackedFixture publishes an event and an immutable metric, then returns
// the fixture together with the metric's first published version.
func metricBackedFixture(t *testing.T) (catalogFixture, reporting.MetricVersion) {
	t.Helper()
	f, draft := metricFixture(t)
	ctx := context.Background()
	validation := metricValidate(t, f, draft)
	version, e := f.s.PublishMetric(ctx, f.owner, f.project, draft.ID, "metric-publish", metricPublish(draft, validation))
	if e != nil {
		t.Fatal(e)
	}
	return f, version
}

func metricBackedDefinition(metricID string) reporting.SavedAnalysisDefinition {
	return reporting.SavedAnalysisDefinition{
		Title:         "Metric-backed card",
		MetricRef:     &reporting.MetricReference{MetricID: metricID, Version: 1},
		Visualization: reporting.SavedAnalysisVisualization{Kind: "metric"},
	}
}

func TestSavedAnalysisMetricReferenceResolvesPinsAndPublishes(t *testing.T) {
	f, metric := metricBackedFixture(t)
	ctx := context.Background()

	created, e := f.s.CreateSavedAnalysis(ctx, f.owner, f.project, "metric-analysis", reporting.SavedAnalysisCreate{
		Environment: "development",
		Definition:  metricBackedDefinition(metric.MetricID),
	})
	if e != nil {
		t.Fatal(e)
	}
	if created.Definition.MetricRef == nil || created.Definition.MetricRef.MetricID != metric.MetricID || created.Definition.MetricRef.Version != 1 {
		t.Fatalf("metric reference not retained: %+v", created.Definition.MetricRef)
	}
	if created.CreatedAt.Location() != time.UTC || created.UpdatedAt.Location() != time.UTC {
		t.Fatalf("saved analysis timestamps must serialize as UTC: %s %s", created.CreatedAt, created.UpdatedAt)
	}
	q := created.Definition.Query
	if q.Kind != analysis.QueryKindTrend || len(q.Metrics) != 1 || q.Metrics[0].Op != "count" || q.Interval != "" || len(q.GroupBy) != 0 {
		t.Fatalf("derived query is not a whole-range metric card: %+v", q)
	}
	if len(q.EventVersions) != 1 || q.EventVersions[0].EventKey != "lesson_opened" || q.EventVersions[0].SchemaVersion != 1 {
		t.Fatalf("derived query lost the pinned event version: %+v", q.EventVersions)
	}
	if created.Kind != analysis.QueryKindTrend {
		t.Fatalf("stored kind should follow the derived query: %s", created.Kind)
	}

	snapshot, e := f.s.SavedAnalysisValidationSnapshot(ctx, f.owner, f.project, created.ID, reporting.SavedAnalysisValidate{DraftRevision: created.DraftRevision})
	if e != nil {
		t.Fatal(e)
	}
	validation, e := reporting.BuildSavedAnalysisValidation(snapshot)
	if e != nil {
		t.Fatal(e)
	}
	if !validation.Valid {
		t.Fatal("metric-referencing validation rejected")
	}
	pin := validation.Dependencies.Metric
	if pin == nil || pin.MetricID != metric.MetricID || pin.Version != 1 || pin.DefinitionHash != metric.DefinitionHash {
		t.Fatalf("metric pin mismatch: %+v metric: %+v", pin, metric)
	}
	if len(validation.Dependencies.EventVersions) != 1 || validation.Dependencies.EventVersions[0].EventKey != "lesson_opened" {
		t.Fatalf("metric event dependency missing: %+v", validation.Dependencies.EventVersions)
	}

	published, e := f.s.PublishSavedAnalysis(ctx, f.owner, f.project, created.ID, "publish-metric-analysis", savedPublish(created, validation))
	if e != nil {
		t.Fatal(e)
	}
	if published.Version != 1 || published.Definition.MetricRef == nil || published.Dependencies.Metric == nil {
		t.Fatalf("published version lost the metric reference: %+v", published)
	}
	// The metric's event version still gets a relational publication witness.
	var witness bool
	if e = f.s.Pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM saved_analysis_dependencies WHERE analysis_id=$1 AND event_key='lesson_opened' AND schema_version=1)`, created.ID).Scan(&witness); e != nil || !witness {
		t.Fatal("missing event dependency witness for metric analysis", e)
	}

	// Enqueue runs the pinned immutable metric through the shared query budget.
	r := queryRequest(t, "trend")
	job, e := f.s.EnqueueSavedAnalysisVersion(ctx, f.owner, f.project, created.ID, published.Version, "run-metric-analysis", reporting.SavedAnalysisExecute{TimeRange: r.TimeRange, Timezone: r.Timezone})
	if e != nil {
		t.Fatal(e)
	}
	if job.ID == "" || job.State == "" {
		t.Fatalf("metric analysis enqueue did not return a job: %+v", job)
	}
}

func TestSavedAnalysisMetricReferenceStaysImmutableAcrossMetricRepublish(t *testing.T) {
	f, metric := metricBackedFixture(t)
	ctx := context.Background()
	created, e := f.s.CreateSavedAnalysis(ctx, f.owner, f.project, "pinned-analysis", reporting.SavedAnalysisCreate{
		Environment: "development",
		Definition:  metricBackedDefinition(metric.MetricID),
	})
	if e != nil {
		t.Fatal(e)
	}
	snapshot, e := f.s.SavedAnalysisValidationSnapshot(ctx, f.owner, f.project, created.ID, reporting.SavedAnalysisValidate{DraftRevision: created.DraftRevision})
	if e != nil {
		t.Fatal(e)
	}
	validation, e := reporting.BuildSavedAnalysisValidation(snapshot)
	if e != nil {
		t.Fatal(e)
	}
	first, e := f.s.PublishSavedAnalysis(ctx, f.owner, f.project, created.ID, "publish-v1", savedPublish(created, validation))
	if e != nil {
		t.Fatal(e)
	}

	// Publish a second metric version. The existing analysis must keep pinning v1.
	updated := metricDefinition()
	updated.Title = "Changed metric title"
	metricDraft, e := f.s.GetMetricDraft(ctx, f.owner, f.project, metric.MetricID)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = f.s.SaveMetricDraft(ctx, f.owner, f.project, metric.MetricID, metricDraft.DraftRevision, updated); e != nil {
		t.Fatal(e)
	}
	metricDraft, e = f.s.GetMetricDraft(ctx, f.owner, f.project, metric.MetricID)
	if e != nil {
		t.Fatal(e)
	}
	metricValidation := metricValidate(t, f, metricDraft)
	if _, e = f.s.PublishMetric(ctx, f.owner, f.project, metric.MetricID, "metric-v2", metricPublish(metricDraft, metricValidation)); e != nil {
		t.Fatal(e)
	}

	stored, e := f.s.GetSavedAnalysisVersion(ctx, f.owner, f.project, created.ID, first.Version)
	if e != nil {
		t.Fatal(e)
	}
	if stored.Dependencies.Metric == nil || stored.Dependencies.Metric.Version != 1 || stored.Definition.MetricRef.Version != 1 {
		t.Fatalf("republishing the metric implicitly moved the old analysis: %+v", stored.Dependencies.Metric)
	}
	if stored.Dependencies.Metric.DefinitionHash != metric.DefinitionHash {
		t.Fatal("pinned metric digest changed after a new metric release")
	}
	// The old pinned version still enqueues because immutable metric v1 remains.
	r := queryRequest(t, "trend")
	if _, e = f.s.EnqueueSavedAnalysisVersion(ctx, f.owner, f.project, created.ID, first.Version, "run-pinned", reporting.SavedAnalysisExecute{TimeRange: r.TimeRange, Timezone: r.Timezone}); e != nil {
		t.Fatal("pinned metric analysis stopped running after metric republish", e)
	}

	// A new analysis can deliberately select metric v2.
	v2Definition := metricBackedDefinition(metric.MetricID)
	v2Definition.MetricRef.Version = 2
	v2, e := f.s.CreateSavedAnalysis(ctx, f.owner, f.project, "v2-analysis", reporting.SavedAnalysisCreate{Environment: "development", Definition: v2Definition})
	if e != nil {
		t.Fatal(e)
	}
	if v2.Definition.MetricRef.Version != 2 || v2.Definition.Query.Metrics[0].Op != "count" {
		t.Fatalf("metric v2 reference not resolved: %+v", v2.Definition)
	}
}

func TestSavedAnalysisMetricReferenceRejectsWrongScopeAndMissingVersion(t *testing.T) {
	f, metric := metricBackedFixture(t)
	ctx := context.Background()

	// Cross-environment reference: the metric only exists in development.
	staging := metricBackedDefinition(metric.MetricID)
	_, e := f.s.CreateSavedAnalysis(ctx, f.owner, f.project, "staging-analysis", reporting.SavedAnalysisCreate{Environment: "staging", Definition: staging})
	faultCode(t, e, "INVALID_METRIC_REFERENCE")

	// Referencing a never-published metric version fails closed.
	missing := metricBackedDefinition(metric.MetricID)
	missing.MetricRef.Version = 99
	_, e = f.s.CreateSavedAnalysis(ctx, f.owner, f.project, "missing-analysis", reporting.SavedAnalysisCreate{Environment: "development", Definition: missing})
	faultCode(t, e, "METRIC_VERSION_NOT_FOUND")

	// Viewers cannot create analyses regardless of the metric reference.
	_, e = f.s.CreateSavedAnalysis(ctx, f.viewer, f.project, "viewer-analysis", reporting.SavedAnalysisCreate{Environment: "development", Definition: metricBackedDefinition(metric.MetricID)})
	faultCode(t, e, "PROJECT_FORBIDDEN")
}
