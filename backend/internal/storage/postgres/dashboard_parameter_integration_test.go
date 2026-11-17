//go:build integration

package postgres

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/howtomakeaname/Evia/backend/internal/analysis"
	"github.com/howtomakeaname/Evia/backend/internal/platform/ids"
	"github.com/howtomakeaname/Evia/backend/internal/reporting"
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

// parameterRunFixture builds a one-widget dashboard pinned to the saved-run
// fixture analysis, with one numeric parameter bound to its lesson_id field.
func parameterRunFixture(t *testing.T, required bool) (catalogFixture, reporting.DashboardDraft, reporting.DashboardExecute, analysis.Field) {
	t.Helper()
	f, v, r := savedRunFixture(t)
	field := analysis.Field{Kind: "property", Key: "lesson_id", EventKey: "lesson_opened", SchemaVersion: 1}
	def := reporting.DashboardDefinition{
		Title: "Parameter dashboard",
		Parameters: []reporting.ParameterDeclaration{{
			ParameterID: "min_lesson", Label: "最小学课ID", Type: reporting.ParameterNumber, Operator: "gte", Required: required,
		}},
		Widgets: []reporting.DashboardWidget{{
			ID: ids.New(), AnalysisID: v.AnalysisID, AnalysisVersion: v.Version, Title: "Lesson total",
			ReadingOrder: 0, Layout: reporting.DashboardLayout{Width: 12, Height: 2},
			ParameterBindings: []reporting.ParameterBinding{{ParameterID: "min_lesson", Field: field}},
		}},
	}
	d, e := f.s.CreateDashboard(context.Background(), f.owner, f.project, ids.New(), reporting.DashboardCreate{Environment: "development", Definition: def})
	if e != nil {
		t.Fatal(e)
	}
	if len(d.Definition.Parameters) != 1 || len(d.Definition.Widgets[0].ParameterBindings) != 1 {
		t.Fatalf("draft must round-trip parameters and bindings: %+v", d.Definition)
	}
	return f, d, reporting.DashboardExecute{TimeRange: r.TimeRange, Timezone: r.Timezone}, field
}

func TestDashboardParameterRunCompilesFiltersAndRoundTrips(t *testing.T) {
	f, d, in, field := parameterRunFixture(t, true)
	dashboardPublish(t, f, d)
	ctx := context.Background()
	in.ParameterValues = reporting.ParameterValues{"min_lesson": float64(5)}
	run, e := f.s.EnqueueDashboardRun(ctx, f.owner, f.project, d.ID, 1, "parameter-run", in)
	if e != nil {
		t.Fatal(e)
	}
	if len(run.Widgets) != 1 {
		t.Fatalf("expected one widget run, got %+v", run.Widgets)
	}
	var op, value, fieldKey string
	if e = f.s.Pool.QueryRow(ctx, `SELECT request->'filter'->>'op', request->'filter'->>'value', request->'filter'->'field'->>'key' FROM analysis_query_jobs WHERE id=$1`, run.Widgets[0].Query.ID).Scan(&op, &value, &fieldKey); e != nil {
		t.Fatal(e)
	}
	if op != "gte" || value != "5" || fieldKey != field.Key {
		t.Fatalf("parameter predicate missing from child request: op=%s value=%s field=%s", op, value, fieldKey)
	}
	// Read-back echoes the runtime values (normalized to json.Number).
	got, e := f.s.GetDashboardRun(ctx, f.viewer, f.project, run.ID)
	if e != nil {
		t.Fatal(e)
	}
	if got.ParameterValues["min_lesson"] != json.Number("5") {
		t.Fatalf("run must echo parameter values, got %+v", got.ParameterValues)
	}
	// Same-key replay returns the identical run.
	replay, e := f.s.EnqueueDashboardRun(ctx, f.owner, f.project, d.ID, 1, "parameter-run", in)
	if e != nil || replay.ID != run.ID {
		t.Fatalf("idempotent replay changed: %+v %v", replay, e)
	}
	// A different parameter value is a different request under the same key.
	changed := in
	changed.ParameterValues = reporting.ParameterValues{"min_lesson": float64(9)}
	_, e = f.s.EnqueueDashboardRun(ctx, f.owner, f.project, d.ID, 1, "parameter-run", changed)
	faultCode(t, e, "IDEMPOTENCY_CONFLICT")
}

func TestDashboardParameterRunMergesAheadOfLocalFilter(t *testing.T) {
	f, d, in, _ := parameterRunFixture(t, false)
	dashboardPublish(t, f, d)
	ctx := context.Background()
	in.ParameterValues = reporting.ParameterValues{"min_lesson": float64(5)}
	in.WidgetFilters = []reporting.DashboardWidgetFilter{{
		WidgetID: d.Definition.Widgets[0].ID,
		Filter:   &analysis.Filter{Op: "eq", Field: &analysis.Field{Kind: "system", Key: "uid"}, Value: "user-1"},
	}}
	run, e := f.s.EnqueueDashboardRun(ctx, f.owner, f.project, d.ID, 1, "parameter-local-run", in)
	if e != nil {
		t.Fatal(e)
	}
	var argOps, argFields []string
	rows, e := f.s.Pool.Query(ctx, `SELECT arg->>'op', arg->'field'->>'key' FROM analysis_query_jobs, jsonb_array_elements(request->'filter'->'args') arg WHERE id=$1`, run.Widgets[0].Query.ID)
	if e != nil {
		t.Fatal(e)
	}
	defer rows.Close()
	for rows.Next() {
		var op, key string
		if e = rows.Scan(&op, &key); e != nil {
			t.Fatal(e)
		}
		argOps = append(argOps, op)
		argFields = append(argFields, key)
	}
	if e = rows.Err(); e != nil {
		t.Fatal(e)
	}
	if len(argOps) != 2 || argOps[0] != "gte" || argFields[0] != "lesson_id" || argOps[1] != "eq" || argFields[1] != "uid" {
		t.Fatalf("expected parameter AND local ordering, got ops=%v fields=%v", argOps, argFields)
	}
}

func TestDashboardParameterRunFailClosed(t *testing.T) {
	ctx := context.Background()
	// Required parameter without a value must be rejected.
	f, d, in, _ := parameterRunFixture(t, true)
	dashboardPublish(t, f, d)
	_, e := f.s.EnqueueDashboardRun(ctx, f.owner, f.project, d.ID, 1, "missing-required", in)
	faultCode(t, e, "MISSING_PARAMETER_VALUE")
	bad := in
	bad.ParameterValues = reporting.ParameterValues{"ghost": float64(1)}
	_, e = f.s.EnqueueDashboardRun(ctx, f.owner, f.project, d.ID, 1, "unknown-key", bad)
	faultCode(t, e, "INVALID_PARAMETER")
	typed := in
	typed.ParameterValues = reporting.ParameterValues{"min_lesson": "not-a-number"}
	_, e = f.s.EnqueueDashboardRun(ctx, f.owner, f.project, d.ID, 1, "type-mismatch", typed)
	faultCode(t, e, "INVALID_PARAMETER_VALUE")

	// Optional parameter with no value and no default compiles to no filter.
	fo, do, io, _ := parameterRunFixture(t, false)
	dashboardPublish(t, fo, do)
	run, e := fo.s.EnqueueDashboardRun(ctx, fo.owner, fo.project, do.ID, 1, "optional-skip", io)
	if e != nil {
		t.Fatal(e)
	}
	var hasFilter bool
	if e = fo.s.Pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM analysis_query_jobs WHERE id=$1 AND request ? 'filter')`, run.Widgets[0].Query.ID).Scan(&hasFilter); e != nil {
		t.Fatal(e)
	}
	if hasFilter {
		t.Fatal("skipped optional parameter must not produce a filter predicate")
	}
}

func TestDashboardParameterBindingRejectedAtDraftSave(t *testing.T) {
	f, v, _ := savedRunFixture(t)
	def := reporting.DashboardDefinition{
		Title:      "Broken bindings",
		Parameters: nil,
		Widgets: []reporting.DashboardWidget{{
			ID: ids.New(), AnalysisID: v.AnalysisID, AnalysisVersion: v.Version, Title: "Lesson total",
			ReadingOrder: 0, Layout: reporting.DashboardLayout{Width: 12, Height: 2},
			ParameterBindings: []reporting.ParameterBinding{{
				ParameterID: "ghost",
				Field:       analysis.Field{Kind: "property", Key: "lesson_id", EventKey: "lesson_opened", SchemaVersion: 1},
			}},
		}},
	}
	_, e := f.s.CreateDashboard(context.Background(), f.owner, f.project, ids.New(), reporting.DashboardCreate{Environment: "development", Definition: def})
	faultCode(t, e, "INVALID_PARAMETER_BINDING")
}
