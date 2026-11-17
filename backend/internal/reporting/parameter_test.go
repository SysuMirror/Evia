package reporting

import (
	"testing"

	"github.com/howtomakeaname/Evia/backend/internal/analysis"
)

func sectionField() analysis.Field {
	return analysis.Field{Kind: "property", Key: "section_id", EventKey: "lesson_opened", SchemaVersion: 1}
}

func TestValidateDeclarationClosedSet(t *testing.T) {
	valid := []ParameterDeclaration{
		{ParameterID: "section", Label: "板块", Type: ParameterString, Operator: "eq"},
		{ParameterID: "count", Label: "次数", Type: ParameterNumber, Operator: "gte"},
		{ParameterID: "vip", Label: "VIP", Type: ParameterBoolean, Operator: "eq"},
	}
	for _, decl := range valid {
		if e := ValidateDeclaration(decl); e != nil {
			t.Fatalf("declaration %+v should be valid: %v", decl, e)
		}
	}
	bad := []ParameterDeclaration{
		{ParameterID: "x", Label: "Bad type", Type: "json", Operator: "eq"},
		{ParameterID: "x", Label: "Bad operator", Type: ParameterBoolean, Operator: "contains"},
		{ParameterID: "x", Label: "Number prefix", Type: ParameterNumber, Operator: "prefix"},
		{ParameterID: "x", Label: "", Type: ParameterString, Operator: "eq"},
		{ParameterID: "x", Label: "Default mismatch", Type: ParameterNumber, Operator: "eq", Default: "not-a-number"},
	}
	for _, decl := range bad {
		if e := ValidateDeclaration(decl); e == nil {
			t.Fatalf("declaration %+v must be rejected", decl)
		}
	}
}

func TestCompileWidgetParameterFiltersSuccess(t *testing.T) {
	declarations := []ParameterDeclaration{
		{ParameterID: "section", Label: "板块", Type: ParameterString, Operator: "eq"},
		{ParameterID: "limit", Label: "下限", Type: ParameterNumber, Operator: "gte"},
	}
	bindings := []ParameterBinding{
		{ParameterID: "section", Field: sectionField()},
		{ParameterID: "limit", Field: analysis.Field{Kind: "property", Key: "read_count", EventKey: "lesson_opened", SchemaVersion: 1}},
	}
	filters, e := CompileWidgetParameterFilters(declarations, bindings, ParameterValues{
		"section": "forum",
		"limit":   float64(10),
	})
	if e != nil {
		t.Fatal(e)
	}
	if len(filters) != 2 {
		t.Fatalf("expected two filters, got %d", len(filters))
	}
	if filters[0].Op != "eq" || filters[0].Value != "forum" || filters[0].Field.Key != "section_id" {
		t.Fatalf("unexpected section filter: %+v", filters[0])
	}
	if filters[1].Value != float64(10) || filters[1].Op != "gte" {
		t.Fatalf("unexpected limit filter: %+v", filters[1])
	}
}

func TestCompileParameterDefaultsAndOptionalSkip(t *testing.T) {
	declarations := []ParameterDeclaration{
		{ParameterID: "section", Label: "板块", Type: ParameterString, Operator: "eq", Default: "all"},
		{ParameterID: "vip", Label: "VIP", Type: ParameterBoolean, Operator: "eq"},
	}
	bindings := []ParameterBinding{
		{ParameterID: "section", Field: sectionField()},
		{ParameterID: "vip", Field: analysis.Field{Kind: "property", Key: "is_vip", EventKey: "lesson_opened", SchemaVersion: 1}},
	}
	// No runtime values: section falls back to its default, optional vip is skipped.
	filters, e := CompileWidgetParameterFilters(declarations, bindings, ParameterValues{})
	if e != nil {
		t.Fatal(e)
	}
	if len(filters) != 1 || filters[0].Value != "all" {
		t.Fatalf("expected only the default filter, got %+v", filters)
	}

	required := []ParameterDeclaration{
		{ParameterID: "section", Label: "板块", Type: ParameterString, Operator: "eq", Required: true},
	}
	if _, e := CompileWidgetParameterFilters(required, bindings[:1], ParameterValues{}); e == nil {
		t.Fatal("missing required parameter must fail closed")
	}
}

func TestCompileParameterFailClosedCases(t *testing.T) {
	field := sectionField()
	declarations := []ParameterDeclaration{
		{ParameterID: "section", Label: "板块", Type: ParameterString, Operator: "eq"},
	}
	bindings := []ParameterBinding{{ParameterID: "section", Field: field}}
	for _, tc := range []struct {
		name     string
		decls    []ParameterDeclaration
		bindings []ParameterBinding
		values   ParameterValues
	}{
		{"unknown value key", declarations, bindings, ParameterValues{"ghost": "x"}},
		{"binding to undeclared parameter", declarations, []ParameterBinding{{ParameterID: "ghost", Field: field}}, ParameterValues{}},
		{"duplicate field binding", declarations, []ParameterBinding{
			{ParameterID: "section", Field: field},
			{ParameterID: "section", Field: field},
		}, ParameterValues{"section": "a"}},
		{"type mismatch", declarations, bindings, ParameterValues{"section": float64(7)}},
		{"string too long", declarations, bindings, ParameterValues{"section": stringsRepeat(1025)}},
		{"in-list over cap", []ParameterDeclaration{{ParameterID: "section", Label: "S", Type: ParameterString, Operator: "in"}},
			bindings, ParameterValues{"section": manyStrings()}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, e := CompileWidgetParameterFilters(tc.decls, tc.bindings, tc.values); e == nil {
				t.Fatalf("%s must be rejected", tc.name)
			}
		})
	}
}

func TestCompileParameterInListAcceptsHomogeneousArray(t *testing.T) {
	declarations := []ParameterDeclaration{
		{ParameterID: "section", Label: "板块", Type: ParameterString, Operator: "in"},
	}
	bindings := []ParameterBinding{{ParameterID: "section", Field: sectionField()}}
	filters, e := CompileWidgetParameterFilters(declarations, bindings, ParameterValues{
		"section": []any{"forum", "blog"},
	})
	if e != nil {
		t.Fatal(e)
	}
	list, ok := filters[0].Value.([]any)
	if !ok || len(list) != 2 {
		t.Fatalf("expected homogeneous in-list, got %+v", filters[0].Value)
	}
}

func stringsRepeat(n int) string {
	runes := make([]rune, n)
	for i := range runes {
		runes[i] = '中'
	}
	return string(runes)
}

func manyStrings() []any {
	out := make([]any, MaxParameterListValues+1)
	for i := range out {
		out[i] = "x"
	}
	return out
}
