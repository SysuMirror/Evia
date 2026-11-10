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
	definition FormulaDefinition
	leaves     map[MetricReference]analysis.Request
	err        error
	called     bool
	execute    MetricExecute
}

func (l *stubFormulaLoader) LoadFormulaExecution(_ context.Context, _ analysis.Scope, _ MetricReference, execute MetricExecute) (FormulaDefinition, map[MetricReference]analysis.Request, error) {
	l.called = true
	l.execute = execute
	return l.definition, l.leaves, l.err
}

func markerRequest() analysis.Request {
	return analysis.Request{
		Kind:        "trend",
		Environment: "development",
		Formula:     &analysis.FormulaJobRef{MetricID: "11111111-1111-4111-8111-111111111111", Version: 1},
		TimeRange:   analysis.TimeRange{},
		Timezone:    "UTC",
		Interval:    "day",
	}
}

func TestCompositeEngineDelegatesOrdinaryRequest(t *testing.T) {
	inner := &recordingEngine{results: map[string]analysis.Result{}}
	loader := &stubFormulaLoader{}
	engine := NewCompositeFormulaEngine(inner, loader)
	ordinary := analysis.Request{Kind: "trend", Environment: "development"}
	if _, e := engine.Execute(context.Background(), analysis.Scope{}, ordinary); e != nil {
		t.Fatal(e)
	}
	if loader.called || len(inner.requests) != 1 || inner.requests[0].Kind != "trend" {
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
	def := FormulaDefinition{Expression: fRatio(idA, idB), Unit: "number"}
	leaves := map[MetricReference]analysis.Request{
		refA: {Kind: "trend", Environment: "development", EventVersions: []analysis.EventVersion{{EventKey: idA, SchemaVersion: 1}}, Metrics: []analysis.Metric{{Op: "count"}}},
		refB: {Kind: "trend", Environment: "development", EventVersions: []analysis.EventVersion{{EventKey: idB, SchemaVersion: 1}}, Metrics: []analysis.Metric{{Op: "count"}}},
	}
	loader := &stubFormulaLoader{definition: def, leaves: leaves}
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
	if loader.execute.Interval != "day" || loader.execute.Timezone != "UTC" {
		t.Fatalf("runtime parameters not forwarded: %+v", loader.execute)
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
	inner := &recordingEngine{fail: errors.New("clickhouse down")}
	leaves := map[MetricReference]analysis.Request{
		fRef(idA, 1): {Kind: "trend", EventVersions: []analysis.EventVersion{{EventKey: idA, SchemaVersion: 1}}},
		fRef(idB, 1): {Kind: "trend", EventVersions: []analysis.EventVersion{{EventKey: idB, SchemaVersion: 1}}},
	}
	loader := &stubFormulaLoader{definition: FormulaDefinition{Expression: fRatio(idA, idB), Unit: "number"}, leaves: leaves}
	engine := NewCompositeFormulaEngine(inner, loader)
	if _, e := engine.Execute(context.Background(), analysis.Scope{}, markerRequest()); e == nil {
		t.Fatal("expected leaf execution error to abort the composite")
	}
}
