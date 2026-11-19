package analysis

import (
	"encoding/json"
	"strings"
	"testing"
)

func resolvedProperty(key, kind string) *Field {
	return &Field{Kind: "property", EventKey: "lesson_opened", SchemaVersion: 2, Key: key, ResolvedType: kind}
}

func clientProperty(key string) *Field {
	return &Field{Kind: "property", EventKey: "lesson_opened", SchemaVersion: 2, Key: key}
}

// TestFilterNegationPlannerSQLPreservesGuard proves each negation keeps the same
// scope/presence/validity guard as its positive counterpart and only inverts the
// inner comparison, so missing/null/invalid rows stay excluded (no SQL NOT over
// the whole guard, which would fold missing rows back into the result).
func TestFilterNegationPlannerSQLPreservesGuard(t *testing.T) {
	cases := []struct {
		name      string
		field     *Field
		op        string
		value     any
		wantFrag  string
		forbidden string
	}{
		{"not_in", resolvedProperty("score", "number"), "not_in", []any{json.Number("1"), json.Number("2")}, "NOT IN (", ""},
		{"not_prefix", resolvedProperty("title", "string"), "not_prefix", "home", "NOT startsWith(", ""},
		{"not_contains string", resolvedProperty("title", "string"), "not_contains", "home", ")=0)", ""},
		{"not_contains array", resolvedProperty("tags", "array:string"), "not_contains", "home", "NOT has(", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := request(t, baseRequest)
			r.Metrics = []Metric{{Op: OpCount, Accuracy: AccuracyExact}}
			r.Filter = &Filter{Op: tc.op, Field: tc.field, Value: tc.value}
			plan, e := Compile("db", "event_facts", Scope{ProjectID: "p", Environment: r.Environment}, r)
			if e != nil {
				t.Fatalf("%s compile: %v", tc.name, e)
			}
			if !strings.Contains(plan.SQL, "JSONHas(") {
				t.Fatalf("%s dropped the presence guard: %s", tc.name, plan.SQL)
			}
			if !strings.Contains(plan.SQL, tc.wantFrag) {
				t.Fatalf("%s SQL missing %q: %s", tc.name, tc.wantFrag, plan.SQL)
			}
			if tc.forbidden != "" && strings.Contains(plan.SQL, tc.forbidden) {
				t.Fatalf("%s SQL unexpectedly contains %q: %s", tc.name, tc.forbidden, plan.SQL)
			}
		})
	}

	// Empty not_in keeps the guard but drops membership: every present/valid row
	// matches, while an empty in still collapses to the constant-false "0".
	r := request(t, baseRequest)
	r.Metrics = []Metric{{Op: OpCount, Accuracy: AccuracyExact}}
	r.Filter = &Filter{Op: "not_in", Field: resolvedProperty("title", "string"), Value: []any{}}
	plan, e := Compile("db", "event_facts", Scope{ProjectID: "p", Environment: r.Environment}, r)
	if e != nil {
		t.Fatal(e)
	}
	if plan.SQL == "0" || !strings.Contains(plan.SQL, "JSONHas(") || strings.Contains(plan.SQL, "NOT IN") {
		t.Fatalf("empty not_in must retain the guard without a membership test: %s", plan.SQL)
	}
	r.Filter.Op = "in"
	inPlan, e := Compile("db", "event_facts", Scope{ProjectID: "p", Environment: r.Environment}, r)
	if e != nil || !strings.Contains(inPlan.SQL, "AND 0") || strings.Contains(inPlan.SQL, " IN (") {
		t.Fatalf("empty in must stay constant-false, got %v %q", e, inPlan.SQL)
	}
}

// TestFilterNegationValidationClosedSet exercises structural validation using the
// client field shape (no server ResolvedType).
func TestFilterNegationValidationClosedSet(t *testing.T) {
	accepted := []struct {
		op    string
		value any
	}{
		{"not_in", []any{"a", "b"}},
		{"not_prefix", "home"},
		{"not_contains", "home"},
	}
	for _, tc := range accepted {
		r := request(t, baseRequest)
		r.Filter = &Filter{Op: tc.op, Field: clientProperty("title"), Value: tc.value}
		if e := Validate(&r); e != nil {
			t.Fatalf("%s must validate: %v", tc.op, e)
		}
	}
	rejected := []struct {
		name  string
		op    string
		value any
	}{
		{"not_in requires array", "not_in", "scalar"},
		{"not_prefix rejects array", "not_prefix", []any{"a"}},
		{"not_contains rejects array", "not_contains", []any{"a"}},
		{"unknown negation stays closed", "not_gt", 1},
	}
	for _, tc := range rejected {
		r := request(t, baseRequest)
		r.Filter = &Filter{Op: tc.op, Field: clientProperty("title"), Value: tc.value}
		if e := Validate(&r); e == nil {
			t.Fatalf("%s was accepted", tc.name)
		}
	}
}

// TestFilterNegationResolvedTypeRules confirms the per-type engine rules after
// field resolution, mirroring the positive operator constraints.
func TestFilterNegationResolvedTypeRules(t *testing.T) {
	ok := []struct {
		name  string
		field *Field
		op    string
		value any
	}{
		{"string not_in array", resolvedProperty("title", "string"), "not_in", []any{"a"}},
		{"number not_in array", resolvedProperty("score", "number"), "not_in", []any{json.Number("1")}},
		{"string not_prefix", resolvedProperty("title", "string"), "not_prefix", "x"},
		{"array not_contains scalar", resolvedProperty("tags", "array:string"), "not_contains", "x"},
	}
	for _, tc := range ok {
		r := request(t, baseRequest)
		r.Filter = &Filter{Op: tc.op, Field: tc.field, Value: tc.value}
		if e := CheckResolved(r); e != nil {
			t.Fatalf("%s must pass resolved checks: %v", tc.name, e)
		}
	}
	bad := []struct {
		name  string
		field *Field
		op    string
		value any
	}{
		{"text negation on number", resolvedProperty("score", "number"), "not_prefix", "x"},
		{"array not_contains needs scalar", resolvedProperty("tags", "array:string"), "not_contains", []any{"x"}},
		{"array has no not_in", resolvedProperty("tags", "array:string"), "not_in", []any{"x"}},
	}
	for _, tc := range bad {
		r := request(t, baseRequest)
		r.Filter = &Filter{Op: tc.op, Field: tc.field, Value: tc.value}
		if e := CheckResolved(r); e == nil {
			t.Fatalf("%s was accepted", tc.name)
		}
	}
}
