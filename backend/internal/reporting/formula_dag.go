package reporting

import (
	"context"

	"github.com/howtomakeaname/Evia/backend/internal/platform/fault"
)

// Cross-metric dependency limits. They bound the graph that results when a
// composite metric references other composite metrics, in addition to the
// syntactic expression budgets in formula.go.
const (
	// MaxFormulaDependencyDepth is the longest allowed chain of metric to
	// metric references. A formula that only references atomic metrics has
	// dependency depth 1.
	MaxFormulaDependencyDepth = 4
	// MaxFormulaExpansion bounds the number of distinct atomic metrics reached
	// after fully expanding the graph. Reused references count once, matching
	// the executor's per-reference fan-out deduplication.
	MaxFormulaExpansion = 32
)

// FormulaResolvedMetric describes one pinned metric version as seen from a
// formula. The resolver implementation owns authorization, project and
// environment ownership and publication checks; this pure layer only reasons
// about graph shape and units.
type FormulaResolvedMetric struct {
	Ref MetricReference
	// Unit is the metric version's declared result unit.
	Unit string
	// Composite marks a metric version that is itself a formula. Atomic
	// metrics report false and an empty References list.
	Composite bool
	// References are the direct pinned metric references of a composite
	// version. They are empty for atomic metrics.
	References []MetricReference
}

// FormulaMetricResolver loads an immutable metric version for formula graph
// validation. Implementations must recheck membership and metrics.read inside
// an authorized transaction and never resolve an implicit latest version.
type FormulaMetricResolver interface {
	ResolveFormulaMetric(context.Context, MetricReference) (FormulaResolvedMetric, error)
}

// FormulaGraph is the validated dependency view of one formula.
type FormulaGraph struct {
	// Direct are the distinct references appearing in the expression.
	Direct []MetricReference
	// AtomicLeaves are all distinct atomic metrics reached after expansion.
	AtomicLeaves []MetricReference
	// Units maps every direct reference key to its result unit.
	Units map[string]string

	resolved map[string]FormulaResolvedMetric
}

const (
	formulaUnvisited = 0
	formulaVisiting  = 1
	formulaDone      = 2
)

// ValidateFormulaDAG normalizes the formula, resolves every reachable metric
// version and proves the dependency graph is acyclic and within budget. The
// self reference identifies the metric being defined so it can never depend on
// itself directly or through a cycle.
func ValidateFormulaDAG(ctx context.Context, self MetricReference, def FormulaDefinition, resolver FormulaMetricResolver) (FormulaGraph, error) {
	normalized, e := NormalizeFormulaDefinition(def)
	if e != nil {
		return FormulaGraph{}, e
	}
	if resolver == nil {
		return FormulaGraph{}, fault.New(fault.Unavailable, "REPORTING_VALIDATOR_UNAVAILABLE", "指标依赖解析器不可用")
	}
	direct := FormulaReferences(normalized.Expression)
	for _, ref := range direct {
		if ref.MetricID == self.MetricID {
			return FormulaGraph{}, formulaInvalid("FORMULA_SELF_REFERENCE", "公式不能直接或间接引用自身", "/formula/expression")
		}
	}
	graph := FormulaGraph{Direct: direct, Units: map[string]string{}, resolved: map[string]FormulaResolvedMetric{}}
	state := map[string]int{}
	atomic := map[string]MetricReference{}
	var visit func(MetricReference, int) error
	visit = func(ref MetricReference, hop int) error {
		if hop > MaxFormulaDependencyDepth {
			return formulaInvalid("FORMULA_TOO_DEEP", "指标依赖链深度超过预算", "/formula/expression")
		}
		key := formulaRefKey(ref)
		switch state[key] {
		case formulaVisiting:
			return formulaInvalid("FORMULA_CYCLE", "指标依赖存在循环", "/formula/expression")
		case formulaDone:
			return nil
		}
		if e = validateMetricReference(&ref); e != nil {
			return e
		}
		if ref.MetricID == self.MetricID {
			return formulaInvalid("FORMULA_CYCLE", "指标依赖存在循环", "/formula/expression")
		}
		resolved, resolveErr := resolver.ResolveFormulaMetric(ctx, ref)
		if resolveErr != nil {
			return resolveErr
		}
		if resolved.Ref.MetricID != ref.MetricID || resolved.Ref.Version != ref.Version {
			return formulaInvalid("INVALID_METRIC_REFERENCE", "指标引用与解析结果不匹配", "/formula/expression")
		}
		if !formulaKnownUnit(resolved.Unit) {
			return formulaInvalid("INVALID_FORMULA_UNIT", "被引用指标的单位无效", "/formula/expression")
		}
		graph.resolved[key] = resolved
		state[key] = formulaVisiting
		if resolved.Composite {
			for _, child := range resolved.References {
				if e = visit(child, hop+1); e != nil {
					return e
				}
			}
		} else {
			atomic[key] = ref
			if len(atomic) > MaxFormulaExpansion {
				return formulaInvalid("FORMULA_EXPANSION_EXCEEDED", "公式展开后的原子指标数量超过预算", "/formula/expression")
			}
		}
		state[key] = formulaDone
		return nil
	}
	for _, ref := range direct {
		if e = visit(ref, 1); e != nil {
			return FormulaGraph{}, e
		}
		graph.Units[formulaRefKey(ref)] = graph.resolved[formulaRefKey(ref)].Unit
	}
	graph.AtomicLeaves = make([]MetricReference, 0, len(atomic))
	for _, ref := range atomic {
		graph.AtomicLeaves = append(graph.AtomicLeaves, ref)
	}
	sortReferences(graph.AtomicLeaves)
	return graph, nil
}
