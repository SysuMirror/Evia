package analysis

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/howtomakeaname/Evia/backend/internal/platform/contract"
	"github.com/howtomakeaname/Evia/backend/internal/platform/fault"
)

func retentionRequest(granularity string, periods int) Request {
	from := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	return Request{
		Kind:        "retention",
		Environment: "development",
		Subject:     SubjectUID,
		TimeRange:   TimeRange{From: from, To: from.AddDate(0, 0, periods)},
		Timezone:    "UTC",
		Retention: &Retention{
			StartEvent:  Step{EventKey: "sign_up", SchemaVersion: 1},
			ReturnEvent: Step{EventKey: "login", SchemaVersion: 1},
			Granularity: granularity,
			Periods:     periods,
		},
	}
}

func retentionRow(subject string, when time.Time, start, ret bool) map[string]any {
	return map[string]any{
		"subject_id":    subject,
		"event_id":      subject + "-" + when.Format("01-15:04:05"),
		"event_time_ns": when.UnixNano(),
		"is_start":      start,
		"is_return":     ret,
	}
}

func TestEvaluateRetentionDayCohortMatrix(t *testing.T) {
	day := func(dayOfMonth, hour int) time.Time {
		return time.Date(2026, 11, dayOfMonth, hour, 0, 0, 0, time.UTC)
	}
	request := retentionRequest("day", 4)
	rows := []map[string]any{
		// Cohort 0: u1 and u2 enter on Nov 1.
		retentionRow("u1", day(1, 9), true, true),
		retentionRow("u1", day(3, 9), false, true),
		retentionRow("u2", day(1, 10), true, false),
		retentionRow("u2", day(4, 9), false, true),
		// A return before u2's entry must not create negative retention.
		retentionRow("u2", day(1, 8), false, true),
		// Cohort 1: u3 enters Nov 2 and returns the next day.
		retentionRow("u3", day(2, 9), true, true),
		retentionRow("u3", day(3, 12), false, true),
		// Cohort 2: u4 enters Nov 3 with no later return.
		retentionRow("u4", day(3, 15), true, false),
		// Subject without a start event never joins a cohort.
		retentionRow("u5", day(1, 9), false, true),
		// Returns beyond the matrix size are dropped.
		retentionRow("u1", day(8, 9), false, true),
	}
	result, e := EvaluateRetention(request, rows)
	if e != nil {
		t.Fatal(e)
	}
	if len(result) != 3 {
		t.Fatalf("want three non-empty cohorts, got %d: %v", len(result), result)
	}
	want := []struct {
		index    int
		start    string
		size     int
		retained []int
		rates    []float64
	}{
		{0, "2026-11-01T00:00:00Z", 2, []int{2, 0, 1, 1}, []float64{1, 0, 0.5, 0.5}},
		{1, "2026-11-02T00:00:00Z", 1, []int{1, 1, 0, 0}, []float64{1, 1, 0, 0}},
		{2, "2026-11-03T00:00:00Z", 1, []int{1, 0, 0, 0}, []float64{1, 0, 0, 0}},
	}
	for i, expected := range want {
		row := result[i]
		if row["cohort_index"] != expected.index || row["cohort_start"] != expected.start || row["cohort_size"] != expected.size {
			t.Fatalf("cohort %d header mismatch: %v", i, row)
		}
		retained, ok := row["retained"].([]int)
		if !ok || len(retained) != 4 {
			t.Fatalf("cohort %d retained shape wrong: %#v", i, row["retained"])
		}
		for offset := range expected.retained {
			if retained[offset] != expected.retained[offset] {
				t.Fatalf("cohort %d offset %d retained=%d want %d", i, offset, retained[offset], expected.retained[offset])
			}
		}
		rates, ok := row["retention_rate"].([]any)
		if !ok || len(rates) != 4 {
			t.Fatalf("cohort %d rate shape wrong: %#v", i, row["retention_rate"])
		}
		for offset, expectedRate := range expected.rates {
			got, ok := rates[offset].(float64)
			if !ok || got != expectedRate {
				t.Fatalf("cohort %d offset %d rate=%v want %v", i, offset, rates[offset], expectedRate)
			}
		}
	}
}

func TestEvaluateRetentionWeekAlignmentUsesTimezone(t *testing.T) {
	shanghai, e := time.LoadLocation("Asia/Shanghai")
	if e != nil {
		t.Fatal(e)
	}
	// Monday 2026-11-02 local starts the first cohort; the matrix covers two weeks.
	from := time.Date(2026, 11, 2, 0, 0, 0, 0, shanghai)
	request := Request{
		Kind: QueryKindRetention, Environment: "development", Subject: SubjectUID,
		TimeRange: TimeRange{From: from, To: from.AddDate(0, 0, 14)},
		Timezone:  "Asia/Shanghai",
		Retention: &Retention{
			StartEvent: Step{EventKey: "sign_up", SchemaVersion: 1}, ReturnEvent: Step{EventKey: "login", SchemaVersion: 1},
			Granularity: "week", Periods: 2,
		},
	}
	rows := []map[string]any{
		retentionRow("u1", time.Date(2026, 11, 2, 10, 0, 0, 0, shanghai), true, false),
		retentionRow("u1", time.Date(2026, 11, 9, 10, 0, 0, 0, shanghai), false, true),
		retentionRow("u2", time.Date(2026, 11, 3, 8, 0, 0, 0, shanghai), true, false),
		// 00:30 local is 16:30 UTC the previous day but the same local Monday.
		retentionRow("u3", time.Date(2026, 11, 1, 16, 30, 0, 0, time.UTC), true, false),
	}
	result, e := EvaluateRetention(request, rows)
	if e != nil {
		t.Fatal(e)
	}
	if len(result) != 1 {
		t.Fatalf("all three entries must share one Monday cohort: %v", result)
	}
	row := result[0]
	if row["cohort_index"] != 0 || row["cohort_size"] != 3 {
		t.Fatalf("cohort assignment wrong: %v", row)
	}
	retained := row["retained"].([]int)
	if retained[0] != 3 || retained[1] != 1 {
		t.Fatalf("week retention wrong: %v", retained)
	}
	if row["cohort_start"] != "2026-11-01T16:00:00Z" {
		t.Fatalf("cohort start must be the timezone-aligned Monday in UTC: %v", row["cohort_start"])
	}
}

func monthRequest(periods int) Request {
	from := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	return Request{
		Kind:        "retention",
		Environment: "development",
		Subject:     SubjectUID,
		TimeRange:   TimeRange{From: from, To: time.Date(2027, 2, 1, 0, 0, 0, 0, time.UTC)},
		Timezone:    "UTC",
		Retention: &Retention{
			StartEvent:  Step{EventKey: "sign_up", SchemaVersion: 1},
			ReturnEvent: Step{EventKey: "login", SchemaVersion: 1},
			Granularity: "month",
			Periods:     periods,
		},
	}
}

func TestEvaluateRetentionMonthCohortMatrix(t *testing.T) {
	request := monthRequest(3)
	if e := Validate(&request); e != nil {
		t.Fatalf("month granularity must pass validation: %v", e)
	}
	at := func(year int, month time.Month, day, hour int) time.Time {
		return time.Date(year, month, day, hour, 0, 0, 0, time.UTC)
	}
	rows := []map[string]any{
		// November 2026 cohort: u1 and u2.
		retentionRow("u1", at(2026, time.November, 5, 9), true, false),
		retentionRow("u1", at(2026, time.December, 10, 9), false, true),
		retentionRow("u2", at(2026, time.November, 20, 10), true, false),
		retentionRow("u2", at(2026, time.November, 25, 10), false, true),
		retentionRow("u2", at(2027, time.January, 15, 9), false, true),
		// December 2026 cohort: u3 returns the following January.
		retentionRow("u3", at(2026, time.December, 8, 9), true, false),
		retentionRow("u3", at(2027, time.January, 2, 9), false, true),
		// January 2027 cohort: u4 enters with no later return.
		retentionRow("u4", at(2027, time.January, 10, 9), true, false),
	}
	result, e := EvaluateRetention(request, rows)
	if e != nil {
		t.Fatal(e)
	}
	want := []struct {
		index    int
		start    string
		size     int
		retained []int
		rates    []float64
	}{
		{0, "2026-11-01T00:00:00Z", 2, []int{2, 1, 1}, []float64{1, 0.5, 0.5}},
		{1, "2026-12-01T00:00:00Z", 1, []int{1, 1, 0}, []float64{1, 1, 0}},
		{2, "2027-01-01T00:00:00Z", 1, []int{1, 0, 0}, []float64{1, 0, 0}},
	}
	if len(result) != len(want) {
		t.Fatalf("want %d month cohorts, got %d: %v", len(want), len(result), result)
	}
	for i, expected := range want {
		row := result[i]
		if row["cohort_index"] != expected.index || row["cohort_start"] != expected.start || row["cohort_size"] != expected.size {
			t.Fatalf("month cohort %d header mismatch: %v", i, row)
		}
		retained, ok := row["retained"].([]int)
		if !ok || len(retained) != 3 {
			t.Fatalf("month cohort %d retained shape wrong: %#v", i, row["retained"])
		}
		for offset := range expected.retained {
			if retained[offset] != expected.retained[offset] {
				t.Fatalf("month cohort %d offset %d retained=%d want %d", i, offset, retained[offset], expected.retained[offset])
			}
		}
		rates, ok := row["retention_rate"].([]any)
		if !ok || len(rates) != 3 {
			t.Fatalf("month cohort %d rate shape wrong: %#v", i, row["retention_rate"])
		}
		for offset, expectedRate := range expected.rates {
			if got, ok := rates[offset].(float64); !ok || got != expectedRate {
				t.Fatalf("month cohort %d offset %d rate=%v want %v", i, offset, rates[offset], expectedRate)
			}
		}
	}
}

func TestEvaluateRetentionMonthAlignmentUsesTimezone(t *testing.T) {
	shanghai, e := time.LoadLocation("Asia/Shanghai")
	if e != nil {
		t.Fatal(e)
	}
	from := time.Date(2026, 11, 1, 0, 0, 0, 0, shanghai)
	request := Request{
		Kind: QueryKindRetention, Environment: "development", Subject: SubjectUID,
		TimeRange: TimeRange{From: from, To: from.AddDate(0, 2, 0)},
		Timezone:  "Asia/Shanghai",
		Retention: &Retention{
			StartEvent: Step{EventKey: "sign_up", SchemaVersion: 1}, ReturnEvent: Step{EventKey: "login", SchemaVersion: 1},
			Granularity: "month", Periods: 2,
		},
	}
	rows := []map[string]any{
		retentionRow("u1", time.Date(2026, 11, 2, 10, 0, 0, 0, shanghai), true, false),
		retentionRow("u2", time.Date(2026, 11, 3, 8, 0, 0, 0, shanghai), true, false),
		// 16:30 UTC is 00:30 next-day local and still belongs to the November local month.
		retentionRow("u3", time.Date(2026, 10, 31, 16, 30, 0, 0, time.UTC), true, false),
	}
	result, e := EvaluateRetention(request, rows)
	if e != nil {
		t.Fatal(e)
	}
	if len(result) != 1 {
		t.Fatalf("all three entries must share one local month cohort: %v", result)
	}
	row := result[0]
	if row["cohort_index"] != 0 || row["cohort_size"] != 3 {
		t.Fatalf("month cohort assignment wrong: %v", row)
	}
	if row["cohort_start"] != "2026-10-31T16:00:00Z" {
		t.Fatalf("cohort start must be the timezone-aligned first month day in UTC: %v", row["cohort_start"])
	}
}

func TestEvaluateRetentionHandlesEdgeCases(t *testing.T) {
	request := retentionRequest("day", 2)
	for _, tc := range []struct {
		name string
		rows []map[string]any
	}{
		{"empty pull", nil},
		{"blank subjects skipped", []map[string]any{{"subject_id": "", "event_time_ns": int64(1), "is_start": true, "is_return": true}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			result, e := EvaluateRetention(request, tc.rows)
			if e != nil || len(result) != 0 {
				t.Fatalf("empty evidence must produce an empty matrix: %v %v", result, e)
			}
		})
	}
	t.Run("invalid event time fails closed", func(t *testing.T) {
		rows := []map[string]any{{"subject_id": "u1", "event_time_ns": "not-a-time", "is_start": true, "is_return": false}}
		if _, e := EvaluateRetention(request, rows); e == nil {
			t.Fatal("corrupt event time must not be accepted")
		}
	})
	t.Run("earliest start pins the cohort", func(t *testing.T) {
		day := func(dayOfMonth int) time.Time {
			return time.Date(2026, 11, dayOfMonth, 9, 0, 0, 0, time.UTC)
		}
		rows := []map[string]any{
			retentionRow("u1", day(2), true, false),
			retentionRow("u1", day(1), true, false),
		}
		result, e := EvaluateRetention(request, rows)
		if e != nil || len(result) != 1 || result[0]["cohort_index"] != 0 {
			t.Fatalf("cohort must use the earliest start: %v %v", result, e)
		}
	})
	t.Run("uint8 and bool flags are equivalent", func(t *testing.T) {
		day := time.Date(2026, 11, 1, 9, 0, 0, 0, time.UTC)
		rows := []map[string]any{
			{"subject_id": "u1", "event_time_ns": day.UnixNano(), "is_start": int64(1), "is_return": int64(0)},
		}
		result, e := EvaluateRetention(request, rows)
		if e != nil || len(result) != 1 || result[0]["cohort_size"] != 1 {
			t.Fatalf("UInt8 flags must count: %v %v", result, e)
		}
	})
}

func TestRetentionValidationClosedSet(t *testing.T) {
	validator, e := contract.New()
	if e != nil {
		t.Fatal(e)
	}
	valid := `{"kind":"retention","environment":"development","subject":"uid","time_range":{"from":"2026-11-01T00:00:00Z","to":"2026-11-05T00:00:00Z"},"timezone":"UTC","retention":{"start_event":{"event_key":"sign_up","schema_version":1},"return_event":{"event_key":"login","schema_version":1},"granularity":"day","periods":4}}`
	if _, e := Parse([]byte(valid), validator); e != nil {
		t.Fatalf("valid retention request rejected: %v", e)
	}
	anonymous := `{"kind":"retention","environment":"development","subject":"anonymous","time_range":{"from":"2026-11-01T00:00:00Z","to":"2026-11-05T00:00:00Z"},"timezone":"UTC","retention":{"start_event":{"event_key":"sign_up","schema_version":1},"return_event":{"event_key":"login","schema_version":1},"granularity":"day","periods":4}}`
	if _, e := Parse([]byte(anonymous), validator); e != nil {
		t.Fatalf("anonymous retention request must be accepted: %v", e)
	}
	// Constraints also expressed in the JSON schema are rejected at parse
	// time; semantic constraints the schema cannot express reach Go validation.
	for _, tc := range []struct{ name, raw, code string }{
		{"subject missing", `{"kind":"retention","environment":"development","time_range":{"from":"2026-11-01T00:00:00Z","to":"2026-11-05T00:00:00Z"},"timezone":"UTC","retention":{"start_event":{"event_key":"sign_up","schema_version":1},"return_event":{"event_key":"login","schema_version":1},"granularity":"day","periods":4}}`, "INVALID_QUERY"},
		{"retention block missing", `{"kind":"retention","environment":"development","subject":"uid","time_range":{"from":"2026-11-01T00:00:00Z","to":"2026-11-05T00:00:00Z"},"timezone":"UTC"}`, "INVALID_QUERY"},
		{"retention on trend kind", `{"kind":"trend","environment":"development","time_range":{"from":"2026-11-01T00:00:00Z","to":"2026-11-02T00:00:00Z"},"timezone":"UTC","retention":{"start_event":{"event_key":"sign_up","schema_version":1},"return_event":{"event_key":"login","schema_version":1},"granularity":"day","periods":4}}`, "INVALID_QUERY_OPTIONS"},
		{"top level event keys", `{"kind":"retention","environment":"development","subject":"uid","event_keys":["login"],"time_range":{"from":"2026-11-01T00:00:00Z","to":"2026-11-05T00:00:00Z"},"timezone":"UTC","retention":{"start_event":{"event_key":"sign_up","schema_version":1},"return_event":{"event_key":"login","schema_version":1},"granularity":"day","periods":4}}`, "INVALID_QUERY"},
		{"group by rejected", `{"kind":"retention","environment":"development","subject":"uid","group_by":[{"kind":"system","key":"uid"}],"time_range":{"from":"2026-11-01T00:00:00Z","to":"2026-11-05T00:00:00Z"},"timezone":"UTC","retention":{"start_event":{"event_key":"sign_up","schema_version":1},"return_event":{"event_key":"login","schema_version":1},"granularity":"day","periods":4}}`, "INVALID_QUERY_OPTIONS"},
		{"span exceeds matrix window", `{"kind":"retention","environment":"development","subject":"uid","time_range":{"from":"2026-11-01T00:00:00Z","to":"2026-11-06T00:00:00Z"},"timezone":"UTC","retention":{"start_event":{"event_key":"sign_up","schema_version":1},"return_event":{"event_key":"login","schema_version":1},"granularity":"day","periods":4}}`, "QUERY_COST_EXCEEDED"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, e := Parse([]byte(tc.raw), validator)
			var problem *fault.Error
			if e == nil {
				t.Fatal("malformed retention request accepted")
			}
			if !errors.As(e, &problem) || problem.Code != tc.code {
				t.Fatalf("want %s, got %v", tc.code, e)
			}
		})
	}

	// Defense-in-depth: internally constructed requests bypass the JSON schema,
	// so Go validation must still close every structural boundary.
	for _, tc := range []struct {
		name string
		edit func(*Request)
		code string
	}{
		{"nil retention block", func(r *Request) { r.Retention = nil }, "INVALID_RETENTION"},
		{"bad granularity", func(r *Request) { r.Retention.Granularity = "hour" }, "INVALID_RETENTION_GRANULARITY"},
		{"zero periods", func(r *Request) { r.Retention.Periods = 0 }, "INVALID_RETENTION_PERIODS"},
		{"periods beyond cap", func(r *Request) { r.Retention.Periods = 13 }, "INVALID_RETENTION_PERIODS"},
		{"bad step event key", func(r *Request) { r.Retention.StartEvent.EventKey = "Sign-Up" }, "INVALID_RETENTION_STEP"},
		{"event keys pinned at top level", func(r *Request) { r.EventKeys = []string{"login"} }, "INVALID_EVENT_SELECTION"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := retentionRequest("day", 4)
			tc.edit(&r)
			e := Validate(&r)
			var problem *fault.Error
			if e == nil || !errors.As(e, &problem) || problem.Code != tc.code {
				t.Fatalf("want %s, got %v", tc.code, e)
			}
		})
	}
}

func retentionTemplate() Template {
	return Template{
		Kind:    "retention",
		Subject: SubjectUID,
		Retention: &Retention{
			StartEvent:  Step{EventKey: "sign_up", SchemaVersion: 1},
			ReturnEvent: Step{EventKey: "login", SchemaVersion: 1},
			Granularity: "day",
			Periods:     4,
		},
	}
}

func TestRetentionSavedTemplateValidatesPinsAndBinds(t *testing.T) {
	template := retentionTemplate()
	if e := ValidateTemplate(&template); e != nil {
		t.Fatalf("retention template must be reusable: %v", e)
	}
	if template.Retention == nil || template.Retention.StartEvent.EventKey != "sign_up" || len(template.EventVersions) != 0 {
		t.Fatalf("retention block or pin projection changed: %+v", template)
	}
	encoded, e := json.Marshal(template)
	if e != nil {
		t.Fatal(e)
	}
	for _, leaked := range []string{"environment", "time_range", "timezone", "event_versions", "funnel"} {
		if strings.Contains(string(encoded), `"`+leaked+`"`) {
			t.Fatal("runtime context escaped into retention template", leaked)
		}
	}
	from := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	bound, e := template.Bind("development", TimeRange{From: from, To: from.AddDate(0, 0, 4)}, "UTC")
	if e != nil {
		t.Fatal(e)
	}
	if bound.Kind != QueryKindRetention || bound.Environment != "development" || bound.Retention == nil || bound.Retention.Periods != 4 {
		t.Fatalf("retention bind lost configuration: %+v", bound)
	}

	for _, tc := range []struct {
		name, code string
		change     func(*Template)
	}{
		{"top level event versions", "INVALID_EVENT_SELECTION", func(t *Template) {
			t.EventVersions = []EventVersion{{EventKey: "sign_up", SchemaVersion: 1}}
		}},
		{"missing retention block", "INVALID_RETENTION", func(t *Template) { t.Retention = nil }},
		{"missing subject", "SUBJECT_REQUIRED", func(t *Template) { t.Subject = "" }},
		{"bad granularity", "INVALID_RETENTION_GRANULARITY", func(t *Template) { t.Retention.Granularity = "hour" }},
		{"periods out of range", "INVALID_RETENTION_PERIODS", func(t *Template) { t.Retention.Periods = 13 }},
		{"unpinned start event", "INVALID_RETENTION_STEP", func(t *Template) { t.Retention.StartEvent.SchemaVersion = 0 }},
		{"trend-only metrics", "INVALID_QUERY_OPTIONS", func(t *Template) { t.Metrics = []Metric{{Op: "count"}} }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			invalid := retentionTemplate()
			tc.change(&invalid)
			var problem *fault.Error
			err := ValidateTemplate(&invalid)
			if err == nil || !errors.As(err, &problem) || problem.Code != tc.code {
				t.Fatalf("want %s, got %v", tc.code, err)
			}
		})
	}
}

func TestRetentionPlannerEmitsBoundedRawPull(t *testing.T) {
	r := retentionRequest("day", 4)
	plan, e := Compile("analytics", "event_facts", Scope{ProjectID: "project-one", Environment: "development"}, r)
	if e != nil {
		t.Fatal(e)
	}
	for _, fragment := range []string{"is_start", "is_return", "LIMIT 100001", "isNotNull(uid)", "uid AS subject_id"} {
		if !strings.Contains(plan.SQL, fragment) {
			t.Fatalf("retention SQL missing %s: %s", fragment, plan.SQL)
		}
	}
	if strings.Contains(plan.SQL, "anonymous_id") {
		t.Fatalf("uid retention must not read the anonymous column: %s", plan.SQL)
	}
	if strings.Contains(plan.SQL, "'sign_up'") || strings.Contains(plan.SQL, "'login'") {
		t.Fatal("event identities must be bound parameters, not inline literals")
	}
	if len(plan.Columns) != 5 || plan.Columns[0].Key != "cohort_index" || plan.Accuracy != AccuracyExact {
		t.Fatalf("retention result contract wrong: %+v", plan.Columns)
	}

	anonymous := retentionRequest("day", 4)
	anonymous.Subject = SubjectAnonymous
	anonymousPlan, e := Compile("analytics", "event_facts", Scope{ProjectID: "project-one", Environment: "development"}, anonymous)
	if e != nil {
		t.Fatal(e)
	}
	for _, fragment := range []string{"anonymous_id AS subject_id", "isNotNull(anonymous_id)", "anonymous_id!=''"} {
		if !strings.Contains(anonymousPlan.SQL, fragment) {
			t.Fatalf("anonymous retention SQL missing %s: %s", fragment, anonymousPlan.SQL)
		}
	}
	if strings.Contains(anonymousPlan.SQL, "uid AS subject_id") {
		t.Fatalf("anonymous retention must not read the uid column: %s", anonymousPlan.SQL)
	}
}
