//go:build integration

package postgres

import (
	"context"
	"strings"
	"testing"
)

// parameterShapeWidget is a minimal valid analysis widget the pre-parameter
// shape checks already accept; parameter cases only vary the new sections.
const parameterShapeWidget = `"widgets":[{"widget_id":"11111111-1111-4111-8111-111111111111","analysis_id":"33333333-3333-4333-8333-333333333333","analysis_version":1,"title":"Visits","reading_order":0,"layout":{"x":0,"y":0,"width":12,"height":2}`

// shapeDefinition joins root-level sections with the shared widget section.
// Extra widget keys (e.g. parameter_bindings) are passed via widgetTail.
func shapeDefinition(head, widgetTail string) string {
	var b strings.Builder
	b.WriteString(`{"title":"Dash","description":""`)
	if head != "" {
		b.WriteString(",")
		b.WriteString(head)
	}
	b.WriteString(",")
	b.WriteString(parameterShapeWidget)
	if widgetTail != "" {
		b.WriteString(",")
		b.WriteString(widgetTail)
	}
	b.WriteString("}]}")
	return b.String()
}

func shapeValid(t *testing.T, s *Store, raw string) bool {
	t.Helper()
	var ok bool
	if e := s.Pool.QueryRow(context.Background(), `SELECT dashboard_definition_valid($1::json)`, raw).Scan(&ok); e != nil {
		t.Fatalf("validator failed for %s: %v", raw, e)
	}
	return ok
}

func TestDashboardParameterShapeGate(t *testing.T) {
	s := database(t)
	ctx := context.Background()
	if e := s.Migrate(ctx); e != nil {
		t.Fatal(e)
	}
	valid := []string{
		// Pre-parameter definitions stay valid after the function replacement.
		shapeDefinition("", ""),
		// Empty parameter section and empty binding list are allowed.
		shapeDefinition(`"parameters":[]`, `"parameter_bindings":[]`),
		// Full parameter contract with a property binding and a system binding.
		shapeDefinition(`"parameters":[
		 {"parameter_id":"section","label":"板块","type":"string","operator":"eq","required":false,"default":null},
		 {"parameter_id":"vip","label":"VIP","type":"boolean","operator":"eq","required":true},
		 {"parameter_id":"limit","label":"下限","type":"number","operator":"gte","required":false,"default":3}]`,
			`"parameter_bindings":[
		 {"parameter_id":"section","field":{"kind":"property","key":"section_id","event_key":"page_opened","schema_version":1}},
		 {"parameter_id":"vip","field":{"kind":"system","key":"uid"}}]`),
	}
	for _, raw := range valid {
		if !shapeValid(t, s, raw) {
			t.Fatalf("valid shape rejected: %s", raw)
		}
	}
	invalid := []string{
		// parameters is not an array
		shapeDefinition(`"parameters":{}`, ""),
		// more than 12 parameters
		shapeDefinition(`"parameters":[`+shapeManyParams(13)+`]`, ""),
		// closed-set type violation
		shapeDefinition(`"parameters":[{"parameter_id":"x","label":"X","type":"json","operator":"eq","required":false}]`, ""),
		// operator incompatible with type
		shapeDefinition(`"parameters":[{"parameter_id":"x","label":"X","type":"number","operator":"prefix","required":false}]`, ""),
		// duplicate parameter identifier
		shapeDefinition(`"parameters":[
		 {"parameter_id":"x","label":"X","type":"string","operator":"eq","required":false},
		 {"parameter_id":"x","label":"Y","type":"string","operator":"eq","required":false}]`, ""),
		// missing required declaration keys
		shapeDefinition(`"parameters":[{"parameter_id":"x","label":"X","type":"string","operator":"eq"}]`, ""),
		// binding references an undeclared parameter
		shapeDefinition("", `"parameter_bindings":[{"parameter_id":"ghost","field":{"kind":"system","key":"uid"}}]`),
		// same parameter bound twice on one widget
		shapeDefinition(`"parameters":[{"parameter_id":"x","label":"X","type":"string","operator":"eq","required":false}]`,
			`"parameter_bindings":[
		 {"parameter_id":"x","field":{"kind":"system","key":"uid"}},
		 {"parameter_id":"x","field":{"kind":"system","key":"event_key"}}]`),
		// duplicate field binding
		shapeDefinition(`"parameters":[
		 {"parameter_id":"a","label":"A","type":"string","operator":"eq","required":false},
		 {"parameter_id":"b","label":"B","type":"string","operator":"eq","required":false}]`,
			`"parameter_bindings":[
		 {"parameter_id":"a","field":{"kind":"property","key":"k","event_key":"page_opened","schema_version":1}},
		 {"parameter_id":"b","field":{"kind":"property","key":"k","event_key":"page_opened","schema_version":1}}]`),
		// property field missing event pin
		shapeDefinition(`"parameters":[{"parameter_id":"x","label":"X","type":"string","operator":"eq","required":false}]`,
			`"parameter_bindings":[{"parameter_id":"x","field":{"kind":"property","key":"k","schema_version":1}}]`),
		// unsupported field kind
		shapeDefinition(`"parameters":[{"parameter_id":"x","label":"X","type":"string","operator":"eq","required":false}]`,
			`"parameter_bindings":[{"parameter_id":"x","field":{"kind":"json","key":"k"}}]`),
		// system field carrying an event pin
		shapeDefinition(`"parameters":[{"parameter_id":"x","label":"X","type":"string","operator":"eq","required":false}]`,
			`"parameter_bindings":[{"parameter_id":"x","field":{"kind":"system","key":"uid","event_key":"page_opened","schema_version":1}}]`),
	}
	for _, raw := range invalid {
		if shapeValid(t, s, raw) {
			t.Fatalf("invalid shape accepted: %s", raw)
		}
	}
}

func shapeManyParams(n int) string {
	parts := make([]string, 0, n)
	for i := 0; i < n; i++ {
		parts = append(parts, `{"parameter_id":"p`+string(rune('a'+i))+`","label":"P","type":"string","operator":"eq","required":false}`)
	}
	return strings.Join(parts, ",")
}
