package reporting

import (
	"context"

	"github.com/howtomakeaname/Evia/backend/internal/analysis"
	"github.com/howtomakeaname/Evia/backend/internal/platform/fault"
)

// FormulaLeafRequest builds the executable request for one pinned metric
// version. The production storage adapter derives it from the immutable metric
// version inside the authorizing enqueue transaction, so the executor never
// resolves a latest pointer or re-checks catalog membership itself.
type FormulaLeafRequest func(context.Context, MetricReference) (analysis.Request, error)

// FormulaExecutor runs a composite metric by executing each distinct referenced
// metric once through the existing analysis engine, then aligning and applying
// the formula. It introduces no second query engine and stays inside the shared
// project query budget because the reference count is bounded at normalization.
type FormulaExecutor struct {
	engine analysis.Engine
	build  FormulaLeafRequest
}

func NewFormulaExecutor(engine analysis.Engine, build FormulaLeafRequest) *FormulaExecutor {
	return &FormulaExecutor{engine: engine, build: build}
}

// Execute fans out to the leaf metrics and evaluates the formula. A failure in
// any leaf aborts the composite instead of silently treating that operand as
// null; null is reserved for missing data and divide-by-zero outcomes.
func (f *FormulaExecutor) Execute(ctx context.Context, scope analysis.Scope, def FormulaDefinition) (analysis.Result, error) {
	if f == nil || f.engine == nil || f.build == nil {
		return analysis.Result{}, fault.New(fault.Unavailable, "QUERY_STORAGE_UNAVAILABLE", "公式执行尚未配置")
	}
	normalized, e := NormalizeFormulaDefinition(def)
	if e != nil {
		return analysis.Result{}, e
	}
	refs := FormulaReferences(normalized.Expression)
	leaves := make(map[MetricReference]analysis.Result, len(refs))
	for _, ref := range refs {
		request, e := f.build(ctx, ref)
		if e != nil {
			return analysis.Result{}, e
		}
		result, e := f.engine.Execute(ctx, scope, request)
		if e != nil {
			return analysis.Result{}, e
		}
		leaves[ref] = result
	}
	return EvaluateFormulaResult(normalized, leaves)
}
