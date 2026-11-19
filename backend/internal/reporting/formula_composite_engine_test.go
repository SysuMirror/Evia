package reporting

import (
	"context"
	"errors"
	"testing"

	"github.com/howtomakeaname/Evia/backend/internal/analysis"
)

type recordingEngine struct {
	requests []analysis.Request
	results  map[string]analysis.Result
	fail     error
}

func (e *recordingEngine) Execute(_ context.Context, _ analysis.Scope, request analysis.Request) (analysis.Result, error) {
	e.requests = append(e.requests, request)
	if e.fail != nil {
		return analysis.Result{}, e.fail
	}
	if len(request.EventVersions) == 0 {
		return analysis.Result{}, nil
	}
	return e.results[request.EventVersions[0].EventKey], nil
}

type stubFormulaLoader struct {
	plan    FormulaExecutionPlan
	err     error
	called  bool
	execute MetricExecute
}

func (l *stubFormulaLoader) LoadFormulaExecution(_ context.Context, _ analysis.Scope, _ MetricReference, execute MetricExecute) (FormulaExecutionPlan, error) {
	l.called = true
	l.execute = execute
	return l.plan, l.err
}

func markerRequest() analysis.Request {
	return analysis.Request{
		Kind:        "trend",
		Environment: "development",
		Formula:     &analysis.FormulaJobRef{MetricID: "11111111-1111-4111-8111-111111111111", Version: 1},
		TimeRange:   analysis.TimeRange{},
		Timezone:    "UTC",
		Interval:    analysis.IntervalDay,
	}
}

func TestCompositeEngineDelegatesOrdinaryRequest(t *testing.T) {
	inner := &recordingEngine{results: map[string]analysis.Result{}}
	loader := &stubFormulaLoader{}
	engine := NewCompositeFormulaEngine(inner, loader)
	ordinary := analysis.Request{Kind: analysis.QueryKindTrend, Environment: "development"}
	if _, e := engine.Execute(context.Background(), analysis.Scope{}, ordinary); e != nil {
		t.Fatal(e)
	}
	if loader.called || len(inner.requests) != 1 || inner.requests[0].Kind != analysis.QueryKindTrend {
		t.Fatal("ordinary request must bypass the loader and reach the inner engine")
	}
}

func TestCompositeEngineFansOutFormula(t *testing.T) {
	idA, idB := fUUID(1), fUUID(2)
	refA, refB := fRef(idA, 1), fRef(idB, 1)
	columns := []analysis.Column{{Key: formulaValueColumn, Type: "number"}}
	inner := &recordingEngine{results: map[string]analysis.Result{
		idA: fResult(columns, map[string]any{formulaValueColumn: 10.0}),
		idB: fResult(columns, map[string]any{formulaValueColumn: 2.0}),
	}}
	root := fRef("11111111-1111-4111-8111-111111111111", 1)
	def := FormulaDefinition{Expression: fRatio(idA, idB), Unit: "number"}
	leaves := map[MetricReference]analysis.Request{
		refA: {Kind: analysis.QueryKindTrend, Environment: "development", EventVersions: []analysis.EventVersion{{EventKey: idA, SchemaVersion: 1}}, Metrics: []analysis.Metric{{Op: "count"}}},
		refB: {Kind: analysis.QueryKindTrend, Environment: "development", EventVersions: []analysis.EventVersion{{EventKey: idB, SchemaVersion: 1}}, Metrics: []analysis.Metric{{Op: "count"}}},
	}
	loader := &stubFormulaLoader{plan: FormulaExecutionPlan{Root: root, Composite: map[MetricReference]FormulaDefinition{root: def}, AtomicRequests: leaves}}
	engine := NewCompositeFormulaEngine(inner, loader)
	out, e := engine.Execute(context.Background(), analysis.Scope{ProjectID: "project", Environment: "development"}, markerRequest())
	if e != nil {
		t.Fatal(e)
	}
	if !loader.called || len(inner.requests) != 2 {
		t.Fatalf("expected two leaf executions, got %d", len(inner.requests))
	}
	for _, request := range inner.requests {
		if request.Formula != nil {
			t.Fatal("leaf request must not carry a formula marker")
		}
	}
	if out.Rows[0][formulaValueColumn] != 5.0 {
		t.Fatalf("expected ratio 5, got %#v", out.Rows[0][formulaValueColumn])
	}
	if loader.execute.Interval != analysis.IntervalDay || loader.execute.Timezone != "UTC" {
		t.Fatalf("runtime parameters not forwarded: %+v", loader.execute)
	}
}

func TestCompositeEngineEvaluatesNestedComposite(t *testing.T) {
	idA, idB, idC := fUUID(1), fUUID(2), fUUID(3)
	refA, refB, refC := fRef(idA, 1), fRef(idB, 1), fRef(idC, 1)
	root := fRef("11111111-1111-4111-8111-111111111111", 1)
	columns := []analysis.Column{{Key: formulaValueColumn, Type: "number"}}
	inner := &recordingEngine{results: map[string]analysis.Result{
		idA: fResult(columns, map[string]any{formulaValueColumn: 10.0}),
		idB: fResult(columns, map[string]any{formulaValueColumn: 2.0}),
	}}
	// C = A / B = 5; root = C * A = 50.
	plan := FormulaExecutionPlan{
		Root: root,
		Composite: map[MetricReference]FormulaDefinition{
			root: {Expression: *fBinary(FormulaMultiply, fMetric(idC, 1), fMetric(idA, 1)), Unit: "event"},
			refC: {Expression: fRatio(idA, idB), Unit: "number"},
		},
		AtomicRequests: map[MetricReference]analysis.Request{
			refA: {Kind: analysis.QueryKindTrend, EventVersions: []analysis.EventVersion{{EventKey: idA, SchemaVersion: 1}}},
			refB: {Kind: analysis.QueryKindTrend, EventVersions: []analysis.EventVersion{{EventKey: idB, SchemaVersion: 1}}},
		},
	}
	engine := NewCompositeFormulaEngine(inner, &stubFormulaLoader{plan: plan})
	out, e := engine.Execute(context.Background(), analysis.Scope{ProjectID: "project", Environment: "development"}, markerRequest())
	if e != nil {
		t.Fatal(e)
	}
	// Only the two distinct atomic leaves hit the inner engine.
	if len(inner.requests) != 2 {
		t.Fatalf("nested fan-out must dedupe shared leaves, got %d", len(inner.requests))
	}
	if out.Rows[0][formulaValueColumn] != 50.0 {
		t.Fatalf("expected nested result 50, got %#v", out.Rows[0][formulaValueColumn])
	}
}

func TestCompositeEnginePropagatesLoaderError(t *testing.T) {
	inner := &recordingEngine{results: map[string]analysis.Result{}}
	loader := &stubFormulaLoader{err: errors.New("pin conflict")}
	engine := NewCompositeFormulaEngine(inner, loader)
	if _, e := engine.Execute(context.Background(), analysis.Scope{}, markerRequest()); e == nil {
		t.Fatal("expected loader error")
	}
	if len(inner.requests) != 0 {
		t.Fatal("inner engine must not run when loading fails")
	}
}

func TestCompositeEngineAbortsOnLeafError(t *testing.T) {
	idA, idB := fUUID(1), fUUID(2)
	root := fRef("11111111-1111-4111-8111-111111111111", 1)
	inner := &recordingEngine{fail: errors.New("clickhouse down")}
	leaves := map[MetricReference]analysis.Request{
		fRef(idA, 1): {Kind: analysis.QueryKindTrend, EventVersions: []analysis.EventVersion{{EventKey: idA, SchemaVersion: 1}}},
		fRef(idB, 1): {Kind: analysis.QueryKindTrend, EventVersions: []analysis.EventVersion{{EventKey: idB, SchemaVersion: 1}}},
	}
	plan := FormulaExecutionPlan{
		Root:           root,
		Composite:      map[MetricReference]FormulaDefinition{root: {Expression: fRatio(idA, idB), Unit: "number"}},
		AtomicRequests: leaves,
	}
	engine := NewCompositeFormulaEngine(inner, &stubFormulaLoader{plan: plan})
	if _, e := engine.Execute(context.Background(), analysis.Scope{}, markerRequest()); e == nil {
		t.Fatal("expected leaf execution error to abort the composite")
	}
}
