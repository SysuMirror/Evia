package analysis

import (
	"strings"
	"testing"
)

// TestFilterSuffixPlannerSQLPreservesGuard proves suffix/not_suffix keep the
// same scope/presence/validity guard as prefix and only add an endsWith test,
// so absent/null/non-typed values stay excluded rather than folding back in.
func TestFilterSuffixPlannerSQLPreservesGuard(t *testing.T) {
	cases := []struct {
		name, op, wantFrag string
	}{
		{"suffix", "suffix", "endsWith("},
		{"not_suffix", "not_suffix", "NOT endsWith("},
	}
	for _, tc := range cases {
		r := request(t, baseRequest)
		r.Metrics = []Metric{{Op: "count", Accuracy: AccuracyExact}}
		r.Filter = &Filter{Op: tc.op, Field: resolvedProperty("title", "string"), Value: ".csv"}
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
	}
}

// TestFilterSuffixValidationClosedSet uses the client field shape (no server
// ResolvedType) to exercise the structural closed set and scalar value rule.
func TestFilterSuffixValidationClosedSet(t *testing.T) {
	for _, op := range []string{"suffix", "not_suffix"} {
		r := request(t, baseRequest)
		r.Filter = &Filter{Op: op, Field: clientProperty("title"), Value: ".csv"}
		if e := Validate(&r); e != nil {
			t.Fatalf("%s must validate with a scalar string: %v", op, e)
		}
		bad := request(t, baseRequest)
		bad.Filter = &Filter{Op: op, Field: clientProperty("title"), Value: []any{".csv"}}
		if e := Validate(&bad); e == nil {
			t.Fatalf("%s must reject an array value", op)
		}
	}
	r := request(t, baseRequest)
	r.Filter = &Filter{Op: "not_suffix_x", Field: clientProperty("title"), Value: ".csv"}
	if e := Validate(&r); e == nil {
		t.Fatal("unknown operator must stay rejected")
	}
}

// TestFilterSuffixResolvedTypeRules confirms text operators remain string-only
// after field resolution, mirroring prefix/contains.
func TestFilterSuffixResolvedTypeRules(t *testing.T) {
	ok := request(t, baseRequest)
	ok.Filter = &Filter{Op: "suffix", Field: resolvedProperty("title", "string"), Value: ".csv"}
	if e := CheckResolved(ok); e != nil {
		t.Fatalf("string suffix must pass resolved checks: %v", e)
	}
	bad := request(t, baseRequest)
	bad.Filter = &Filter{Op: "suffix", Field: resolvedProperty("score", "number"), Value: ".csv"}
	if e := CheckResolved(bad); e == nil {
		t.Fatal("suffix on a number field must be rejected")
	}
}
