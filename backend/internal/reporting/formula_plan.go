package reporting

import (
	"github.com/howtomakeaname/Evia/backend/internal/analysis"
)

// FormulaExecutionPlan is the fully expanded executable graph of one formula
// job. Composite maps every formula node (the root plus each nested composite
// operand) to its normalized formula; AtomicRequests holds one executable
// trend request per distinct atomic leaf. The storage adapter builds the plan
// inside an authorized worker transaction and never resolves an implicit
// latest version.
type FormulaExecutionPlan struct {
	Root           MetricReference
	Composite      map[MetricReference]FormulaDefinition
	AtomicRequests map[MetricReference]analysis.Request
}

// EvaluateFormulaPlan evaluates an expanded graph bottom-up. Atomic leaf
// results must cover every distinct leaf; composite nodes are evaluated from
// their direct operand results and shared sub-formulas are computed once.
// Publish-time DAG validation already proved the graph acyclic, but the
// visiting set still fails closed on any unexpected cycle so a corrupt plan
// can never recurse forever.
func EvaluateFormulaPlan(plan FormulaExecutionPlan, atomicResults map[MetricReference]analysis.Result) (analysis.Result, error) {
	memo := make(map[string]analysis.Result, len(plan.Composite)+len(atomicResults))
	visiting := map[string]bool{}
	var evaluate func(ref MetricReference) (analysis.Result, error)
	evaluate = func(ref MetricReference) (analysis.Result, error) {
		key := formulaRefKey(ref)
		if result, ok := memo[key]; ok {
			return result, nil
		}
		if result, ok := atomicResults[ref]; ok {
			memo[key] = result
			return result, nil
		}
		def, ok := plan.Composite[ref]
		if !ok {
			return analysis.Result{}, formulaInvalid("INVALID_FORMULA", "公式执行计划缺少指标节点", "/formula/expression")
		}
		if visiting[key] {
			return analysis.Result{}, formulaInvalid("FORMULA_CYCLE", "指标依赖存在循环", "/formula/expression")
		}
		visiting[key] = true
		leaves := make(map[MetricReference]analysis.Result)
		for _, child := range FormulaReferences(def.Expression) {
			result, e := evaluate(child)
			if e != nil {
				return analysis.Result{}, e
			}
			leaves[child] = result
		}
		visiting[key] = false
		result, e := EvaluateFormulaResult(def, leaves)
		if e != nil {
			return analysis.Result{}, e
		}
		memo[key] = result
		return result, nil
	}
	return evaluate(plan.Root)
}
