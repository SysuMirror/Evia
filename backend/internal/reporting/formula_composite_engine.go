package reporting

import (
	"context"

	"github.com/howtomakeaname/Evia/backend/internal/analysis"
	"github.com/howtomakeaname/Evia/backend/internal/platform/fault"
)

// FormulaExecutionLoader resolves a durable formula job into its fully
// expanded execution plan: every nested composite definition and one
// executable request per distinct atomic leaf. The storage implementation
// rechecks the immutable pin inside the worker's context and builds every
// leaf through the same metric template path.
type FormulaExecutionLoader interface {
	LoadFormulaExecution(context.Context, analysis.Scope, MetricReference, MetricExecute) (FormulaExecutionPlan, error)
}

// CompositeFormulaEngine implements analysis.Engine by routing internal formula
// jobs to a bounded fan-out and delegating every ordinary request unchanged. It
// introduces no second storage or query engine: leaves run through the inner
// engine and the existing project query budget still governs admission.
type CompositeFormulaEngine struct {
	inner  analysis.Engine
	loader FormulaExecutionLoader
}

func NewCompositeFormulaEngine(inner analysis.Engine, loader FormulaExecutionLoader) *CompositeFormulaEngine {
	return &CompositeFormulaEngine{inner: inner, loader: loader}
}

var _ analysis.Engine = (*CompositeFormulaEngine)(nil)

func (e *CompositeFormulaEngine) Execute(ctx context.Context, scope analysis.Scope, request analysis.Request) (analysis.Result, error) {
	if e == nil || e.inner == nil {
		return analysis.Result{}, fault.New(fault.Unavailable, "QUERY_STORAGE_UNAVAILABLE", "分析执行尚未配置")
	}
	if request.Formula == nil {
		return e.inner.Execute(ctx, scope, request)
	}
	if e.loader == nil {
		return analysis.Result{}, fault.New(fault.Unavailable, "QUERY_STORAGE_UNAVAILABLE", "公式执行尚未配置")
	}
	ref := MetricReference{MetricID: request.Formula.MetricID, Version: request.Formula.Version}
	execute := metricExecuteFromRequest(request)
	plan, err := e.loader.LoadFormulaExecution(ctx, scope, ref, execute)
	if err != nil {
		return analysis.Result{}, err
	}
	results := make(map[MetricReference]analysis.Result, len(plan.AtomicRequests))
	for leafRef, leafRequest := range plan.AtomicRequests {
		// Defense in depth: leaf requests must never themselves be formula jobs.
		leafRequest.Formula = nil
		result, err := e.inner.Execute(ctx, scope, leafRequest)
		if err != nil {
			return analysis.Result{}, err
		}
		results[leafRef] = result
	}
	return EvaluateFormulaPlan(plan, results)
}

// metricExecuteFromRequest recovers the runtime parameters the enqueue path
// stored on the durable marker request. Structural validation already happened
// at enqueue, so this only maps fields without re-admitting anything.
func metricExecuteFromRequest(request analysis.Request) MetricExecute {
	return MetricExecute{
		TimeRange: request.TimeRange,
		Timezone:  request.Timezone,
		Interval:  request.Interval,
		GroupBy:   request.GroupBy,
		Filter:    request.Filter,
		Limit:     request.Limit,
	}
}
