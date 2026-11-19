//go:build integration

package postgres

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/howtomakeaname/Evia/backend/internal/analysis"
	"github.com/howtomakeaname/Evia/backend/internal/reporting"
)

// publishCompositeFormula provisions the shared formula fixture and publishes
// one composite metric referencing its two atomic leaves.
func publishCompositeFormula(t *testing.T) (formulaFixture, reporting.FormulaMetricVersion) {
	t.Helper()
	f := formulaDB(t)
	version := publishFormula(t, f, "saved-composite-formula", "Lesson ratio", formulaMetricDef(f.atomicA, f.atomicB))
	return f, version
}

func compositeBackedDefinition(formulaID string) reporting.SavedAnalysisDefinition {
	return reporting.SavedAnalysisDefinition{
		Title:         "Composite-backed card",
		MetricRef:     &reporting.MetricReference{MetricID: formulaID, Version: 1, Kind: reporting.MetricRefKindComposite},
		Visualization: reporting.SavedAnalysisVisualization{Kind: "metric"},
	}
}

func TestSavedAnalysisCompositeReferenceResolvesAndEnqueuesMarker(t *testing.T) {
	f, formula := publishCompositeFormula(t)
	ctx := context.Background()

	created, e := f.s.CreateSavedAnalysis(ctx, f.owner, f.project, "composite-analysis", reporting.SavedAnalysisCreate{
		Environment: "development",
		Definition:  compositeBackedDefinition(formula.MetricID),
	})
	if e != nil {
		t.Fatal(e)
	}
	ref := created.Definition.MetricRef
	if ref == nil || !ref.IsComposite() || ref.Kind != reporting.MetricRefKindComposite {
		t.Fatalf("composite reference not retained: %+v", ref)
	}
	// The stored query is a trend marker shell, never an atomic template.
	q := created.Definition.Query
	if q.Kind != analysis.QueryKindTrend || len(q.Metrics) != 0 || len(q.EventVersions) != 0 || q.Interval != "" || len(q.GroupBy) != 0 || q.Filter != nil {
		t.Fatalf("composite definition must carry an empty trend marker shell: %+v", q)
	}
	if created.Kind != analysis.QueryKindTrend {
		t.Fatalf("stored kind should follow the marker shell: %s", created.Kind)
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
		t.Fatal("composite-referencing validation rejected")
	}
	pin := validation.Dependencies.Metric
	if pin == nil || pin.MetricID != formula.MetricID || pin.Version != formula.Version || pin.DefinitionHash != formula.DefinitionHash {
		t.Fatalf("composite pin mismatch: %+v formula: %+v", pin, formula)
	}

	published, e := f.s.PublishSavedAnalysis(ctx, f.owner, f.project, created.ID, "publish-composite-analysis", savedPublish(created, validation))
	if e != nil {
		t.Fatal(e)
	}
	if published.Version != 1 || published.Definition.MetricRef == nil || !published.Definition.MetricRef.IsComposite() {
		t.Fatalf("published version lost the composite reference: %+v", published.Definition.MetricRef)
	}

	r := queryRequest(t, "trend")
	job, e := f.s.EnqueueSavedAnalysisVersion(ctx, f.owner, f.project, created.ID, published.Version, "run-composite-analysis", reporting.SavedAnalysisExecute{TimeRange: r.TimeRange, Timezone: r.Timezone})
	if e != nil {
		t.Fatal(e)
	}
	var stored []byte
	if e = f.s.Pool.QueryRow(ctx, `SELECT request FROM analysis_query_jobs WHERE id=$1`, job.ID).Scan(&stored); e != nil {
		t.Fatal(e)
	}
	var marker struct {
		Formula *struct {
			MetricID string `json:"metric_id"`
			Version  int64  `json:"version"`
		} `json:"formula_ref"`
	}
	if e = json.Unmarshal(stored, &marker); e != nil {
		t.Fatal(e)
	}
	if marker.Formula == nil || marker.Formula.MetricID != formula.MetricID || marker.Formula.Version != formula.Version {
		t.Fatalf("enqueued job must carry the internal formula_ref marker: %s", string(stored))
	}
}

func TestSavedAnalysisCompositeReferenceRejectsMissingVersion(t *testing.T) {
	f, formula := publishCompositeFormula(t)
	ctx := context.Background()

	missing := compositeBackedDefinition(formula.MetricID)
	missing.MetricRef.Version = 99
	if _, e := f.s.CreateSavedAnalysis(ctx, f.owner, f.project, "composite-missing", reporting.SavedAnalysisCreate{
		Environment: "development",
		Definition:  missing,
	}); e == nil {
		t.Fatal("missing composite version must fail closed")
	}

	// An atomic sent to a nonexistent row still resolves through the atomic table.
	wrongKind := compositeBackedDefinition(formula.MetricID)
	wrongKind.MetricRef.Kind = "nested"
	if _, e := f.s.CreateSavedAnalysis(ctx, f.owner, f.project, "composite-bad-kind", reporting.SavedAnalysisCreate{
		Environment: "development",
		Definition:  wrongKind,
	}); e == nil {
		t.Fatal("unknown metric ref kind must fail closed")
	}
}
