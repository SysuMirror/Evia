package analysis

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/howtomakeaname/Evia/backend/internal/platform/contract"
	"github.com/howtomakeaname/Evia/backend/internal/propertylayout"
)

func request(t *testing.T, raw string) Request {
	t.Helper()
	validator, e := contract.New()
	if e != nil {
		t.Fatal(e)
	}
	parsed, e := Parse([]byte(raw), validator)
	if e != nil {
		t.Fatal(e)
	}
	return parsed
}

const baseRequest = `{"kind":"trend","environment":"development","time_range":{"from":"2026-11-07T10:00:00Z","to":"2026-11-07T11:00:00Z"},"timezone":"UTC"}`

func TestQueryBoundariesAndStrictDSL(t *testing.T) {
	validator, e := contract.New()
	if e != nil {
		t.Fatal(e)
	}
	for _, tc := range []struct{ name, raw string }{
		{"SQL field injection", strings.Replace(baseRequest, `"timezone":"UTC"`, `"timezone":"UTC","filter":{"op":"eq","field":{"kind":"system","key":"uid) OR 1=1 --"},"value":"x"}`, 1)},
		{"unknown operator", strings.Replace(baseRequest, `"timezone":"UTC"`, `"timezone":"UTC","filter":{"op":"raw_sql","value":"1=1"}`, 1)},
		{"duplicate key", strings.Replace(baseRequest, `"kind":"trend"`, `"kind":"trend","kind":"events"`, 1)},
		{"revision without resolved subject", strings.Replace(baseRequest, `"timezone":"UTC"`, `"timezone":"UTC","identity_revision":3`, 1)},
		{"resolved journey", strings.Replace(strings.Replace(baseRequest, `"kind":"trend"`, `"kind":"journey"`, 1), `"timezone":"UTC"`, `"timezone":"UTC","subject":"resolved_person","identity_filter":{"resolved_person_id":"11111111-1111-4111-8111-111111111111"}`, 1)},
		{"resolved events without pin", strings.Replace(strings.Replace(baseRequest, `"kind":"trend"`, `"kind":"events"`, 1), `"timezone":"UTC"`, `"timezone":"UTC","subject":"resolved_person"`, 1)},
		{"reversed time", strings.Replace(baseRequest, `"to":"2026-11-07T11:00:00Z"`, `"to":"2026-11-07T09:00:00Z"`, 1)},
		{"unbounded period", strings.Replace(baseRequest, `"from":"2026-11-07T10:00:00Z"`, `"from":"2025-11-07T10:00:00Z"`, 1)},
		{"UV subject omitted", strings.Replace(baseRequest, `"timezone":"UTC"`, `"timezone":"UTC","metrics":[{"op":"unique"}]`, 1)},
		{"arbitrary server type", strings.Replace(baseRequest, `"timezone":"UTC"`, `"timezone":"UTC","filter":{"op":"eq","field":{"kind":"system","key":"uid","resolved_type":"raw_sql"},"value":"x"}`, 1)},
		{"cursor unsupported", strings.Replace(baseRequest, `"timezone":"UTC"`, `"timezone":"UTC","cursor":"unscoped"`, 1)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, e := Parse([]byte(tc.raw), validator); e == nil {
				t.Fatal("unsafe/unsupported request accepted")
			}
		})
	}
	r := request(t, baseRequest)
	deep := &Filter{Op: "eq", Field: &Field{Kind: "system", Key: "uid"}, Value: "x"}
	for i := 0; i < 10; i++ {
		deep = &Filter{Op: "and", Args: []*Filter{deep}}
	}
	r.Filter = deep
	if e := Validate(&r); e == nil {
		t.Fatal("unbounded predicate depth accepted")
	}
}
func TestPlannerBindsValuesAndPreservesScopedMissingSemantics(t *testing.T) {
	r := request(t, baseRequest)
	attack := "' OR 1=1; DROP TABLE event_facts_raw --"
	r.Filter = &Filter{Op: "eq", Field: &Field{Kind: "property", EventKey: "lesson_opened", SchemaVersion: 2, Key: "title", ResolvedType: "string"}, Value: attack}
	r.Metrics = []Metric{{Op: OpCount, Accuracy: AccuracyExact}}
	plan, e := Compile("testdb", "event_facts", Scope{ProjectID: "project-secret", Environment: "development"}, r)
	if e != nil {
		t.Fatal(e)
	}
	if strings.Contains(plan.SQL, attack) || strings.Contains(plan.SQL, "project-secret") || strings.Contains(plan.SQL, "lesson_opened") {
		t.Fatal("request values interpolated into SQL", plan.SQL)
	}
	found := false
	for _, v := range plan.Parameters {
		found = found || v == attack
	}
	if !found {
		t.Fatal("bound comparison value missing")
	}
	if !strings.Contains(plan.SQL, "JSONHas(") || !strings.Contains(plan.SQL, "schema_version=") {
		t.Fatal("property presence/version scope missing")
	}
	if _, e := Compile("testdb;DROP", "event_facts", Scope{ProjectID: "p", Environment: "development"}, r); e == nil {
		t.Fatal("unsafe configured identifier accepted")
	}
	if _, e := Compile("testdb", "event_facts", Scope{ProjectID: "p", Environment: "production"}, r); e == nil {
		t.Fatal("scope mismatch accepted")
	}
	r.Filter.Op = "is_missing"
	r.Filter.Value = nil
	plan, e = Compile("testdb", "event_facts", Scope{ProjectID: "p", Environment: "development"}, r)
	if e != nil {
		t.Fatal(e)
	}
	if !strings.Contains(plan.SQL, "AND NOT JSONHas") {
		t.Fatal("missing treated as zero/false")
	}
}
func TestPlannerPropertyExpressionsGateOnLosslessLayout(t *testing.T) {
	r := request(t, baseRequest)
	r.Filter = &Filter{Op: "eq", Field: &Field{Kind: "property", EventKey: "lesson_opened", SchemaVersion: 2, Key: "title", ResolvedType: "string"}, Value: "Home"}
	r.Metrics = []Metric{{Op: OpCount, Accuracy: AccuracyExact}}
	plan, e := Compile("testdb", "event_facts", Scope{ProjectID: "project-secret", Environment: "development"}, r)
	if e != nil {
		t.Fatal(e)
	}
	encoded := propertylayout.EncodeKey("title")
	for _, fragment := range []string{
		"property_layout_version=1",
		"mapContains(property_tokens,",
		"concat('{\"f\":',property_tokens[",
		"JSONExtractString(",
	} {
		if !strings.Contains(plan.SQL, fragment) {
			t.Fatalf("property SQL missing %q: %s", fragment, plan.SQL)
		}
	}
	if strings.Contains(plan.SQL, encoded) {
		t.Fatal("encoded property key must stay a bound parameter, not SQL text", plan.SQL)
	}
	bound := false
	for _, value := range plan.Parameters {
		bound = bound || value == encoded
	}
	if !bound {
		t.Fatal("encoded property key missing from bound parameters", plan.Parameters)
	}
}
func TestMetricPrecisionAndTypes(t *testing.T) {
	r := request(t, baseRequest)
	r.Subject = SubjectUID
	r.Metrics = []Metric{{Op: OpUnique, Accuracy: AccuracyExact}}
	plan, e := Compile("db", "event_facts", Scope{ProjectID: "p", Environment: r.Environment}, r)
	if e != nil || plan.Accuracy != AccuracyExact || !strings.Contains(plan.SQL, "uniqExactIf") {
		t.Fatal(plan, e)
	}
	r.Metrics[0].Accuracy = AccuracyApproximate
	plan, e = Compile("db", "event_facts", Scope{ProjectID: "p", Environment: r.Environment}, r)
	if e != nil || plan.Accuracy != AccuracyApproximate || !strings.Contains(plan.SQL, "uniqCombined64If") {
		t.Fatal(plan, e)
	}
	r.Metrics = []Metric{{Op: OpSum, Field: &Field{Kind: "system", Key: "uid", ResolvedType: "string"}}}
	if _, e := Compile("db", "event_facts", Scope{ProjectID: "p", Environment: r.Environment}, r); e == nil {
		t.Fatal("string sum accepted")
	}
	r.Metrics = []Metric{{Op: OpCount}}
	r.Filter = &Filter{Op: "eq", Field: &Field{Kind: "property", Key: "enabled", EventKey: "lesson_opened", SchemaVersion: 1, ResolvedType: "boolean"}, Value: json.Number("0")}
	if _, e := Compile("db", "event_facts", Scope{ProjectID: "p", Environment: r.Environment}, r); e == nil {
		t.Fatal("zero coerced into false")
	}
}
func TestMetricPercentileQuantileLevels(t *testing.T) {
	numericField := &Field{
		Kind: "property", EventKey: "page_opened", SchemaVersion: 1,
		Key: "score", ResolvedType: "number",
	}
	for _, tc := range []struct {
		op    string
		level string
	}{
		{"p50", "0.50"},
		{"p90", "0.90"},
		{"p95", "0.95"},
		{"p99", "0.99"},
	} {
		r := request(t, baseRequest)
		r.Subject = SubjectUID
		r.Metrics = []Metric{{Op: tc.op, Field: numericField, Accuracy: AccuracyExact}}
		plan, e := Compile("db", "event_facts", Scope{ProjectID: "p", Environment: r.Environment}, r)
		if e != nil {
			t.Fatalf("%s exact compile: %v", tc.op, e)
		}
		if want := "quantileExactIf(" + tc.level + ")"; !strings.Contains(plan.SQL, want) {
			t.Fatalf("%s exact SQL missing %s: %s", tc.op, want, plan.SQL)
		}

		r.Metrics[0].Accuracy = AccuracyApproximate
		plan, e = Compile("db", "event_facts", Scope{ProjectID: "p", Environment: r.Environment}, r)
		if e != nil {
			t.Fatalf("%s approximate compile: %v", tc.op, e)
		}
		if want := "quantileTDigestIf(" + tc.level + ")"; !strings.Contains(plan.SQL, want) {
			t.Fatalf("%s approximate SQL missing %s: %s", tc.op, want, plan.SQL)
		}
	}
}

func TestMetricPercentileValidationClosedSet(t *testing.T) {
	numericField := &Field{Kind: "property", EventKey: "page_opened", SchemaVersion: 1, Key: "score"}
	for _, op := range []string{"p50", "p90", "p95", "p99"} {
		exact := request(t, baseRequest)
		exact.Subject = SubjectUID
		exact.Metrics = []Metric{{Op: op, Field: numericField, Accuracy: AccuracyExact}}
		if e := Validate(&exact); e != nil {
			t.Fatalf("%s exact must validate: %v", op, e)
		}
		approx := request(t, baseRequest)
		approx.Subject = SubjectUID
		approx.Metrics = []Metric{{Op: op, Field: numericField, Accuracy: AccuracyApproximate}}
		if e := Validate(&approx); e != nil {
			t.Fatalf("%s approximate must validate: %v", op, e)
		}
	}
	// Non-percentile numeric ops still reject approximate accuracy.
	sumRequest := request(t, baseRequest)
	sumRequest.Subject = SubjectUID
	sumRequest.Metrics = []Metric{{Op: OpSum, Field: numericField, Accuracy: AccuracyApproximate}}
	if e := Validate(&sumRequest); e == nil {
		t.Fatal("sum approximate must be rejected")
	}
	// An unknown operator stays outside the closed set.
	bad := request(t, baseRequest)
	bad.Subject = SubjectUID
	bad.Metrics = []Metric{{Op: "p42", Field: numericField, Accuracy: AccuracyExact}}
	if e := Validate(&bad); e == nil {
		t.Fatal("unknown percentile op must be rejected")
	}
}

func TestFunnelOrderingWindowAndEntity(t *testing.T) {
	r := Request{Kind: QueryKindFunnel, Funnel: &Funnel{Steps: []Step{{EventKey: "open", SchemaVersion: 1}, {EventKey: "finish", SchemaVersion: 1}}, WindowSeconds: 30, SameEntity: []EntityField{{StepIndex: 0}, {StepIndex: 1}}}}
	row := func(subject, id string, when, seq int64, session string, step int, entity string) map[string]any {
		return map[string]any{"subject_id": subject, "event_id": id, "source_id": "s", "event_time_ns": when * 1000000, "sequence": seq, "session_id": session, "tab_id": "tab-one", "step_0": step == 0, "step_1": step == 1, "entity_0": entity, "entity_1": entity}
	}
	rows := []map[string]any{row("later-start", "1", 0, 1, "s", 0, "a"), row("later-start", "2", 90000, 2, "s", 0, "a"), row("later-start", "3", 100000, 3, "s", 1, "a"), row("wrong-entity", "1", 0, 1, "s", 0, "a"), row("wrong-entity", "2", 10000, 2, "s", 1, "b"), row("equal-time", "1", 0, 1, "s", 0, "a"), row("equal-time", "2", 0, 2, "s", 1, "a"), row("uncertain-time", "1", 0, 0, "", 0, "a"), row("uncertain-time", "2", 0, 0, "", 1, "a"), row("wrong-order", "1", 1000, 1, "s", 1, "a"), row("wrong-order", "2", 2000, 2, "s", 0, "a")}
	crossTabStart := row("cross-tab", "1", 0, 1, "s", 0, "a")
	crossTabEnd := row("cross-tab", "2", 0, 2, "s", 1, "a")
	crossTabEnd["tab_id"] = "other-tab"
	crossSourceStart := row("cross-source", "1", 0, 1, "s", 0, "a")
	crossSourceEnd := row("cross-source", "2", 0, 2, "s", 1, "a")
	crossSourceEnd["source_id"] = "other-source"
	preciseStart := row("precise-time", "1", 0, 0, "", 0, "a")
	preciseEnd := row("precise-time", "2", 0, 0, "", 1, "a")
	preciseStart["event_time_ns"] = int64(100000)
	preciseEnd["event_time_ns"] = int64(200000)
	rows = append(rows, crossTabStart, crossTabEnd, crossSourceStart, crossSourceEnd, preciseStart, preciseEnd)
	result, e := EvaluateFunnel(r, rows)
	if e != nil || result[0]["subjects"] != 8 || result[1]["subjects"] != 3 {
		t.Fatal(result, e)
	}
}
