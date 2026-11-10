package reporting

import (
	"context"
	"errors"
	"testing"
)

type fakeFormulaResolver struct {
	metrics map[string]FormulaResolvedMetric
	fail    error
}

func (r fakeFormulaResolver) ResolveFormulaMetric(_ context.Context, ref MetricReference) (FormulaResolvedMetric, error) {
	if r.fail != nil {
		return FormulaResolvedMetric{}, r.fail
	}
	resolved, ok := r.metrics[formulaRefKey(ref)]
	if !ok {
		return FormulaResolvedMetric{}, errors.New("metric not found")
	}
	return resolved, nil
}

func atomicMetric(id string, version int64, unit string) FormulaResolvedMetric {
	ref := fRef(id, version)
	return FormulaResolvedMetric{Ref: ref, Unit: unit, Composite: false}
}

func compositeMetric(id string, version int64, unit string, refs ...MetricReference) FormulaResolvedMetric {
	return FormulaResolvedMetric{Ref: fRef(id, version), Unit: unit, Composite: true, References: refs}
}

func TestValidateFormulaDAGResolvesAtomicLeaves(t *testing.T) {
	idA, idB := fUUID(1), fUUID(2)
	resolver := fakeFormulaResolver{metrics: map[string]FormulaResolvedMetric{
		formulaRefKey(fRef(idA, 1)): atomicMetric(idA, 1, "event"),
		formulaRefKey(fRef(idB, 1)): atomicMetric(idB, 1, "event"),
	}}
	def := FormulaDefinition{Expression: fRatio(idA, idB), Unit: "number"}
	graph, err := ValidateFormulaDAG(context.Background(), fRef(fUUID(9), 1), def, resolver)
	if err != nil {
		t.Fatalf("validate: %v", err)
	}
	if len(graph.Direct) != 2 || len(graph.AtomicLeaves) != 2 {
		t.Fatalf("graph %+v", graph)
	}
	if graph.Units[formulaRefKey(fRef(idA, 1))] != "event" {
		t.Fatalf("units %+v", graph.Units)
	}
}

func TestValidateFormulaDAGRejectsDirectSelfReference(t *testing.T) {
	id := fUUID(1)
	resolver := fakeFormulaResolver{metrics: map[string]FormulaResolvedMetric{}}
	def := FormulaDefinition{Expression: *fMetric(id, 1), Unit: "event"}
	if _, err := ValidateFormulaDAG(context.Background(), fRef(id, 1), def, resolver); err == nil {
		t.Fatal("expected self reference rejection")
	}
}

func TestValidateFormulaDAGRejectsCycle(t *testing.T) {
	self, idA, idB := fUUID(9), fUUID(1), fUUID(2)
	resolver := fakeFormulaResolver{metrics: map[string]FormulaResolvedMetric{
		formulaRefKey(fRef(idA, 1)): compositeMetric(idA, 1, "number", fRef(idB, 1)),
		formulaRefKey(fRef(idB, 1)): compositeMetric(idB, 1, "number", fRef(idA, 1)),
	}}
	def := FormulaDefinition{Expression: *fMetric(idA, 1), Unit: "number"}
	if _, err := ValidateFormulaDAG(context.Background(), fRef(self, 1), def, resolver); err == nil {
		t.Fatal("expected cycle rejection")
	}
}

func TestValidateFormulaDAGRejectsDeepChain(t *testing.T) {
	self := fUUID(9)
	metrics := map[string]FormulaResolvedMetric{}
	var previous string
	for i := 1; i <= MaxFormulaDependencyDepth+1; i++ {
		id := fUUID(i)
		if i == MaxFormulaDependencyDepth+1 {
			metrics[formulaRefKey(fRef(id, 1))] = atomicMetric(id, 1, "event")
		} else {
			next := fUUID(i + 1)
			metrics[formulaRefKey(fRef(id, 1))] = compositeMetric(id, 1, "event", fRef(next, 1))
		}
		previous = id
	}
	_ = previous
	root := fUUID(1)
	resolver := fakeFormulaResolver{metrics: metrics}
	def := FormulaDefinition{Expression: *fMetric(root, 1), Unit: "event"}
	if _, err := ValidateFormulaDAG(context.Background(), fRef(self, 1), def, resolver); err == nil {
		t.Fatal("expected dependency depth rejection")
	}
}

func TestValidateFormulaDAGRejectsExpansionBudget(t *testing.T) {
	self, compositeID := fUUID(9), fUUID(1)
	refs := make([]MetricReference, 0, MaxFormulaExpansion+1)
	metrics := map[string]FormulaResolvedMetric{
		formulaRefKey(fRef(compositeID, 1)): {Ref: fRef(compositeID, 1), Unit: "event", Composite: true},
	}
	for i := 0; i <= MaxFormulaExpansion; i++ {
		id := fUUID(i + 10)
		ref := fRef(id, 1)
		refs = append(refs, ref)
		metrics[formulaRefKey(ref)] = atomicMetric(id, 1, "event")
	}
	composite := metrics[formulaRefKey(fRef(compositeID, 1))]
	composite.References = refs
	metrics[formulaRefKey(fRef(compositeID, 1))] = composite
	resolver := fakeFormulaResolver{metrics: metrics}
	def := FormulaDefinition{Expression: *fMetric(compositeID, 1), Unit: "event"}
	if _, err := ValidateFormulaDAG(context.Background(), fRef(self, 1), def, resolver); err == nil {
		t.Fatal("expected expansion budget rejection")
	}
}

func TestValidateFormulaDAGExpandsNestedComposite(t *testing.T) {
	self, idA, idB, idC := fUUID(9), fUUID(1), fUUID(2), fUUID(3)
	resolver := fakeFormulaResolver{metrics: map[string]FormulaResolvedMetric{
		formulaRefKey(fRef(idA, 1)): compositeMetric(idA, 1, "number", fRef(idB, 1), fRef(idC, 1)),
		formulaRefKey(fRef(idB, 1)): atomicMetric(idB, 1, "event"),
		formulaRefKey(fRef(idC, 1)): atomicMetric(idC, 1, "event"),
	}}
	def := FormulaDefinition{Expression: *fMetric(idA, 1), Unit: "number"}
	graph, err := ValidateFormulaDAG(context.Background(), fRef(self, 1), def, resolver)
	if err != nil {
		t.Fatalf("validate: %v", err)
	}
	if len(graph.AtomicLeaves) != 2 {
		t.Fatalf("atomic leaves %+v", graph.AtomicLeaves)
	}
}

func TestValidateFormulaDAGPropagatesResolverFailure(t *testing.T) {
	id := fUUID(1)
	resolver := fakeFormulaResolver{fail: errors.New("forbidden")}
	def := FormulaDefinition{Expression: *fMetric(id, 1), Unit: "event"}
	if _, err := ValidateFormulaDAG(context.Background(), fRef(fUUID(9), 1), def, resolver); err == nil {
		t.Fatal("expected resolver failure")
	}
}

func TestValidateFormulaDAGRequiresResolver(t *testing.T) {
	id := fUUID(1)
	def := FormulaDefinition{Expression: *fMetric(id, 1), Unit: "event"}
	if _, err := ValidateFormulaDAG(context.Background(), fRef(fUUID(9), 1), def, nil); err == nil {
		t.Fatal("expected unavailable resolver")
	}
}
