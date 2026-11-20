package analysis

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/howtomakeaname/Evia/backend/internal/platform/fault"
)

func trendTemplate() Template {
	return Template{Kind: QueryKindTrend, EventVersions: []EventVersion{{EventKey: "opened", SchemaVersion: 1}}}
}

func funnelTemplate() Template {
	return Template{Kind: QueryKindFunnel, Subject: SubjectUID, Funnel: &Funnel{Mode: FunnelModeOrderedWithInterleaving, WindowSeconds: 60, Steps: []Step{{EventKey: "opened", SchemaVersion: 1}, {EventKey: "closed", SchemaVersion: 2}}, SameEntity: []EntityField{
		{StepIndex: 0, Field: Field{Kind: "property", EventKey: "opened", SchemaVersion: 1, Key: "item"}},
		{StepIndex: 1, Field: Field{Kind: "property", EventKey: "closed", SchemaVersion: 2, Key: "item"}},
	}}}
}

func templateErrorCode(t *testing.T, e error, code string) {
	t.Helper()
	var problem *fault.Error
	if !errors.As(e, &problem) || problem.Code != code {
		t.Fatalf("wanted %s, got %v", code, e)
	}
}

func TestTemplateNormalizesWithoutExecutionContextOrDerivedTypes(t *testing.T) {
	template := trendTemplate()
	template.GroupBy = []Field{{Kind: "system", Key: "event_key"}}
	if e := ValidateTemplate(&template); e != nil {
		t.Fatal(e)
	}
	if template.Limit != 200 || len(template.Metrics) != 1 || template.Metrics[0].Op != OpCount || template.Metrics[0].Accuracy != AccuracyExact || template.GroupBy[0].ResolvedType != "" {
		t.Fatal("template defaults or public field projection changed", template)
	}
	encoded, e := json.Marshal(template)
	if e != nil {
		t.Fatal(e)
	}
	for _, field := range []string{"environment", "time_range", "timezone", "identity_revision", "identity_filter", "event_keys", "cursor", "resolved_type"} {
		if strings.Contains(string(encoded), `"`+field+`"`) {
			t.Fatal("execution context or derived state escaped into template", field, string(encoded))
		}
	}
	// Revalidation of saved configuration is stable after system types are stripped.
	if e = ValidateTemplate(&template); e != nil {
		t.Fatal(e)
	}
	r := Request{Kind: QueryKindTrend, GroupBy: []Field{{Kind: "system", Key: "event_key"}}}
	if e = ValidateConfiguration(&r); e != nil || r.GroupBy[0].ResolvedType != "string" || r.Limit != 200 || !r.TimeRange.From.IsZero() || r.Timezone != "" || r.Environment != "" {
		t.Fatal("context-free validation added or required runtime context", r, e)
	}
	// Direct cross-version queries remain supported; only reusable templates need pins.
	full := request(t, baseRequest)
	if len(full.EventVersions) != 0 || len(full.Metrics) != 1 {
		t.Fatal("direct query defaults changed", full)
	}
}

func TestTemplateRejectsUnsupportedOrUnpinnedConfiguration(t *testing.T) {
	for _, tc := range []struct {
		name, code string
		change     func(*Template)
	}{
		{"event details", "UNSUPPORTED_QUERY_KIND", func(t *Template) { t.Kind = QueryKindEvents }},
		{"journey", "UNSUPPORTED_QUERY_KIND", func(t *Template) { t.Kind = QueryKindJourney }},
		{"missing pins", "INVALID_EVENT_SELECTION", func(t *Template) { t.EventVersions = nil }},
		{"empty pins", "INVALID_EVENT_SELECTION", func(t *Template) { t.EventVersions = []EventVersion{} }},
		{"duplicate pair", "INVALID_EVENT_VERSIONS", func(t *Template) { t.EventVersions = append(t.EventVersions, t.EventVersions[0]) }},
		{"multiple versions of event", "INVALID_EVENT_VERSIONS", func(t *Template) {
			t.EventVersions = append(t.EventVersions, EventVersion{EventKey: "opened", SchemaVersion: 2})
		}},
		{"unsafe version", "INVALID_EVENT_VERSIONS", func(t *Template) { t.EventVersions[0].SchemaVersion = MaxSafeInteger + 1 }},
		{"event injection", "INVALID_EVENT_VERSIONS", func(t *Template) { t.EventVersions[0].EventKey = "opened') OR 1=1" }},
		{"server field type", "INVALID_FIELD", func(t *Template) { t.GroupBy = []Field{{Kind: "system", Key: "uid", ResolvedType: "string"}} }},
		{"UV without subject", "SUBJECT_REQUIRED", func(t *Template) { t.Metrics = []Metric{{Op: OpUnique}} }},
		{"unknown interval", "INVALID_INTERVAL", func(t *Template) { t.Interval = "year" }},
		{"too many groups", "QUERY_COST_EXCEEDED", func(t *Template) { t.GroupBy = make([]Field, 4) }},
		{"too many metrics", "QUERY_COST_EXCEEDED", func(t *Template) { t.Metrics = make([]Metric, 11) }},
		{"too many events", "QUERY_COST_EXCEEDED", func(t *Template) { t.EventVersions = make([]EventVersion, 21) }},
		{"row budget", "INVALID_LIMIT", func(t *Template) { t.Limit = 501 }},
		{"resolved funnel without pin is fine but unknown interval is not", "INVALID_INTERVAL", func(t *Template) { t.Subject = SubjectResolvedPerson; t.Interval = "year" }},
		{"bad JSON value", "INVALID_QUERY", func(t *Template) {
			t.Filter = &Filter{Op: "eq", Field: &Field{Kind: "system", Key: "uid"}, Value: make(chan string)}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			template := trendTemplate()
			tc.change(&template)
			templateErrorCode(t, ValidateTemplate(&template), tc.code)
		})
	}
	templateErrorCode(t, ValidateTemplate(nil), "INVALID_QUERY")
	maximum := trendTemplate()
	maximum.EventVersions = nil
	for i := range 20 {
		maximum.EventVersions = append(maximum.EventVersions, EventVersion{EventKey: fmt.Sprintf("event_%d", i), SchemaVersion: MaxSafeInteger})
	}
	maximum.GroupBy = []Field{{Kind: "system", Key: "uid"}, {Kind: "system", Key: "event_key"}, {Kind: "system", Key: "anonymous_id"}}
	for range 10 {
		maximum.Metrics = append(maximum.Metrics, Metric{Op: OpCount})
	}
	maximum.Limit = 500
	if e := ValidateTemplate(&maximum); e != nil {
		t.Fatal("bounded maximum configuration rejected", e)
	}
}

func TestTemplatePropertyDependenciesUseExactSelectedVersions(t *testing.T) {
	for _, placement := range []string{"filter", "group", "metric", "funnel global filter", "funnel step filter", "funnel same entity"} {
		for _, selected := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/selected=%t", placement, selected), func(t *testing.T) {
				template := trendTemplate()
				field := Field{Kind: "property", EventKey: "opened", SchemaVersion: 99, Key: "score"}
				if selected {
					field.SchemaVersion = 1
				}
				filter := &Filter{Op: "and", Args: []*Filter{{Op: "eq", Field: &field, Value: json.Number("0")}}}
				switch placement {
				case "filter":
					template.Filter = filter
				case "group":
					template.GroupBy = []Field{field}
				case "metric":
					template.Metrics = []Metric{{Op: OpSum, Field: &field}}
				case "funnel global filter":
					template = funnelTemplate()
					template.Filter = filter
				case "funnel step filter":
					template = funnelTemplate()
					template.Funnel.Steps[0].Filter = filter
				case "funnel same entity":
					template = funnelTemplate()
					template.Funnel.SameEntity[0].Field = field
				}
				e := ValidateTemplate(&template)
				if selected && e != nil {
					t.Fatal("selected property rejected", e)
				}
				if !selected {
					templateErrorCode(t, e, "QUERY_FIELD_VERSION_NOT_SELECTED")
				}
			})
		}
	}
	funnel := funnelTemplate()
	funnel.Funnel.SameEntity[0].Field = funnel.Funnel.SameEntity[1].Field
	templateErrorCode(t, ValidateTemplate(&funnel), "INVALID_SAME_ENTITY")
	funnel = funnelTemplate()
	funnel.Funnel.SameEntity[0].StepIndex = 2
	templateErrorCode(t, ValidateTemplate(&funnel), "INVALID_SAME_ENTITY")
	funnel = funnelTemplate()
	funnel.Funnel.Steps[0].SchemaVersion = 0
	templateErrorCode(t, ValidateTemplate(&funnel), "INVALID_FUNNEL_STEP")
	funnel = funnelTemplate()
	funnel.Funnel.Mode = "ordered"
	templateErrorCode(t, ValidateTemplate(&funnel), "INVALID_FUNNEL")
	funnel = funnelTemplate()
	funnel.EventVersions = []EventVersion{}
	templateErrorCode(t, ValidateTemplate(&funnel), "INVALID_EVENT_SELECTION")
}

func TestTemplateBindingClonesConfigurationAndPreservesScalars(t *testing.T) {
	when := time.Date(2026, 11, 7, 10, 0, 0, 123456789, time.UTC)
	window := TimeRange{From: when, To: when.Add(time.Hour)}
	for _, value := range []any{false, json.Number("0")} {
		template := trendTemplate()
		template.Filter = &Filter{Op: "and", Args: []*Filter{{Op: "eq", Field: &Field{Kind: "property", EventKey: "opened", SchemaVersion: 1, Key: "value"}, Value: value}}}
		template.GroupBy = []Field{{Kind: "system", Key: "event_key"}}
		bound, e := template.Bind("staging", window, "Asia/Shanghai")
		if e != nil {
			t.Fatal(e)
		}
		if bound.Environment != "staging" || bound.TimeRange != window || bound.Timezone != "Asia/Shanghai" || bound.Filter.Args[0].Value != value || bound.GroupBy[0].ResolvedType != "string" {
			t.Fatal("binding changed runtime context or scalar", bound)
		}
		bound.EventVersions[0].EventKey = "changed"
		bound.Filter.Args[0].Value = "changed"
		bound.Filter.Args[0].Field.Key = "changed"
		bound.GroupBy[0].Key = "uid"
		bound.Metrics[0].Op = OpUnique
		if template.EventVersions[0].EventKey != "opened" || template.Filter.Args[0].Value != value || template.Filter.Args[0].Field.Key != "value" || template.GroupBy[0].Key != "event_key" || template.GroupBy[0].ResolvedType != "" || template.Metrics != nil || template.Limit != 0 {
			t.Fatal("binding mutated saved configuration", template)
		}
	}
	funnel := funnelTemplate()
	bound, e := funnel.Bind("development", window, "UTC")
	if e != nil {
		t.Fatal(e)
	}
	bound.Funnel.Steps[0].EventKey = "changed"
	bound.Funnel.SameEntity[0].Field.Key = "changed"
	if funnel.Funnel.Steps[0].EventKey != "opened" || funnel.Funnel.SameEntity[0].Field.Key != "item" {
		t.Fatal("funnel binding retained aliases", funnel)
	}
}

func TestTemplateBindRequiresActualValidRuntimeContext(t *testing.T) {
	when := time.Date(2026, 11, 7, 10, 0, 0, 0, time.UTC)
	window := TimeRange{From: when, To: when.Add(time.Hour)}
	template := trendTemplate()
	for _, tc := range []struct {
		environment, timezone, code string
		window                      TimeRange
	}{
		{"", "UTC", "INVALID_ENVIRONMENT", window},
		{"development", "UTC", "INVALID_TIME_RANGE", TimeRange{}},
		{"development", "UTC", "INVALID_TIME_RANGE", TimeRange{From: when, To: when}},
		{"development", "UTC", "QUERY_COST_EXCEEDED", TimeRange{From: when, To: when.Add(32 * 24 * time.Hour)}},
		{"development", "", "INVALID_TIMEZONE", window},
		{"development", "not/a/timezone", "INVALID_TIMEZONE", window},
	} {
		_, e := template.Bind(tc.environment, tc.window, tc.timezone)
		templateErrorCode(t, e, tc.code)
	}
	// The shorter funnel execution budget is checked only against supplied times.
	template = funnelTemplate()
	_, e := template.Bind("development", TimeRange{From: when, To: when.Add(8 * 24 * time.Hour)}, "UTC")
	templateErrorCode(t, e, "QUERY_COST_EXCEEDED")
	invalid := trendTemplate()
	invalid.Metrics = []Metric{{Op: OpCount, Field: &Field{Kind: "system", Key: "uid"}}}
	templateErrorCode(t, ValidateTemplate(&invalid), "INVALID_METRIC")
	if invalid.Metrics[0].Accuracy != "" || invalid.Metrics[0].Field.ResolvedType != "" || invalid.Limit != 0 {
		t.Fatal("failed validation mutated caller configuration", invalid)
	}
}
