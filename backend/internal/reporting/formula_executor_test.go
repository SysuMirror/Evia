package reporting

import (
	"context"
	"errors"
	"testing"

	"github.com/howtomakeaname/Evia/backend/internal/analysis"
)

type fakeFormulaEngine struct {
	results map[string]analysis.Result
	calls   []analysis.Request
	fail    error
}

func (e *fakeFormulaEngine) Execute(_ context.Context, _ analysis.Scope, request analysis.Request) (analysis.Result, error) {
	e.calls = append(e.calls, request)
	if e.fail != nil {
		return analysis.Result{}, e.fail
	}
	return e.results[request.EventVersions[0].EventKey], nil
}

func formulaLeafBuild(ref MetricReference) analysis.Request {
	return analysis.Request{
		Kind:          "trend",
		Environment:   "development",
		EventVersions: []analysis.EventVersion{{EventKey: ref.MetricID, SchemaVersion: ref.Version}},
		Metrics:       []analysis.Metric{{Op: "count"}},
	}
}

func TestFormulaExecutorFansOutAndEvaluates(t *testing.T) {
	idA, idB := fUUID(1), fUUID(2)
	columns := []analysis.Column{{Key: formulaValueColumn, Type: "number"}}
	engine := &fakeFormulaEngine{results: map[string]analysis.Result{
		idA: fResult(columns, map[string]any{formulaValueColumn: 10.0}),
		idB: fResult(columns, map[string]any{formulaValueColumn: 4.0}),
	}}
	executor := NewFormulaExecutor(engine, func(_ context.Context, ref MetricReference) (analysis.Request, error) {
		return formulaLeafBuild(ref), nil
	})
	out, err := executor.Execute(context.Background(), analysis.Scope{ProjectID: "project", Environment: "development"}, FormulaDefinition{Expression: fRatio(idA, idB), Unit: "number"})
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if len(engine.calls) != 2 {
		t.Fatalf("expected two leaf executions, got %d", len(engine.calls))
	}
	if out.Rows[0][formulaValueColumn] != 2.5 {
		t.Fatalf("expected 2.5, got %#v", out.Rows[0][formulaValueColumn])
	}
}

func TestFormulaExecutorDeduplicatesRepeatedReference(t *testing.T) {
	id := fUUID(1)
	columns := []analysis.Column{{Key: formulaValueColumn, Type: "number"}}
	engine := &fakeFormulaEngine{results: map[string]analysis.Result{
		id: fResult(columns, map[string]any{formulaValueColumn: 4.0}),
	}}
	executor := NewFormulaExecutor(engine, func(_ context.Context, ref MetricReference) (analysis.Request, error) {
		return formulaLeafBuild(ref), nil
	})
	expr := fBinary(FormulaDivide, fMetric(id, 1), fMetric(id, 1))
	out, err := executor.Execute(context.Background(), analysis.Scope{ProjectID: "project", Environment: "development"}, FormulaDefinition{Expression: *expr, Unit: "number"})
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if len(engine.calls) != 1 {
		t.Fatalf("repeated reference must execute once, got %d calls", len(engine.calls))
	}
	if out.Rows[0][formulaValueColumn] != 1.0 {
		t.Fatalf("expected 1, got %#v", out.Rows[0][formulaValueColumn])
	}
}

func TestFormulaExecutorPropagatesBuildError(t *testing.T) {
	idA, idB := fUUID(1), fUUID(2)
	engine := &fakeFormulaEngine{results: map[string]analysis.Result{}}
	executor := NewFormulaExecutor(engine, func(context.Context, MetricReference) (analysis.Request, error) {
		return analysis.Request{}, errors.New("build failed")
	})
	if _, err := executor.Execute(context.Background(), analysis.Scope{}, FormulaDefinition{Expression: fRatio(idA, idB), Unit: "number"}); err == nil {
		t.Fatal("expected build error")
	}
	if len(engine.calls) != 0 {
		t.Fatal("engine must not run when request building fails")
	}
}

func TestFormulaExecutorPropagatesEngineError(t *testing.T) {
	idA, idB := fUUID(1), fUUID(2)
	engine := &fakeFormulaEngine{fail: errors.New("clickhouse unavailable")}
	executor := NewFormulaExecutor(engine, func(_ context.Context, ref MetricReference) (analysis.Request, error) {
		return formulaLeafBuild(ref), nil
	})
	if _, err := executor.Execute(context.Background(), analysis.Scope{}, FormulaDefinition{Expression: fRatio(idA, idB), Unit: "number"}); err == nil {
		t.Fatal("expected engine error")
	}
}

func TestFormulaExecutorRequiresConfiguration(t *testing.T) {
	id := fUUID(1)
	executor := NewFormulaExecutor(nil, nil)
	if _, err := executor.Execute(context.Background(), analysis.Scope{}, FormulaDefinition{Expression: *fMetric(id, 1), Unit: "event"}); err == nil {
		t.Fatal("expected unavailable executor")
	}
}
