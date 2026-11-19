package analysis

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/howtomakeaname/Evia/backend/internal/platform/contract"
	"github.com/howtomakeaname/Evia/backend/internal/platform/fault"
)

func segmentRequest() Request {
	from := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	return Request{
		Kind:        "segment",
		Environment: "development",
		Subject:     SubjectUID,
		TimeRange:   TimeRange{From: from, To: from.AddDate(0, 0, 7)},
		Timezone:    "UTC",
		Segment: &Segment{
			Event: Step{EventKey: "sign_up", SchemaVersion: 1},
			Field: Field{Kind: "property", EventKey: "sign_up", SchemaVersion: 1, Key: "plan", ResolvedType: "string"},
		},
	}
}

func segmentRow(subject string, seconds int, present bool, value any) map[string]any {
	return map[string]any{
		"subject_id":      subject,
		"event_id":        subject + "-" + time.Unix(int64(seconds), 0).Format("15:04:05"),
		"event_time_ns":   time.Date(2026, 11, 1, 0, 0, seconds, 0, time.UTC).UnixNano(),
		"segment_present": present,
		"segment_value":   value,
	}
}

func TestEvaluateSegmentUniqueOwnershipAndMissingGroup(t *testing.T) {
	rows := []map[string]any{
		// u1's earliest event carries "pro"; a later "enterprise" must not move it.
		segmentRow("u1", 10, true, "pro"),
		segmentRow("u1", 30, true, "enterprise"),
		segmentRow("u2", 20, true, "pro"),
		segmentRow("u3", 40, true, "enterprise"),
		// u4 has an absent value and joins the missing group.
		segmentRow("u4", 50, false, nil),
		// Blank subjects are skipped entirely.
		segmentRow("", 70, true, "pro"),
	}
	result, e := EvaluateSegment(segmentRequest(), rows)
	if e != nil {
		t.Fatal(e)
	}
	if len(result) != 3 {
		t.Fatalf("want pro, enterprise, missing groups, got %v", result)
	}
	want := []struct {
		value    string
		subjects int
		share    float64
	}{
		{"pro", 2, 0.5},
		{segmentMissingLabel, 1, 0.25},
		{"enterprise", 1, 0.25},
	}
	for i, expected := range want {
		row := result[i]
		if row["segment_value"] != expected.value || row["subjects"] != expected.subjects {
			t.Fatalf("group %d mismatch: %v", i, row)
		}
		share, ok := row["share"].(float64)
		if !ok || share != expected.share {
			t.Fatalf("group %d share=%v want %v", i, row["share"], expected.share)
		}
	}
	// Largest group first; equal-size groups fall back to lexicographic label,
	// where '_' (the missing label prefix) sorts before lowercase letters.
	if result[0]["segment_value"] != "pro" || result[1]["segment_value"] != segmentMissingLabel {
		t.Fatalf("group ordering wrong: %v", result)
	}
}

func TestEvaluateSegmentScalarRenderingAndEarliestSelection(t *testing.T) {
	request := segmentRequest()
	request.Segment.Field.ResolvedType = "boolean"
	rows := []map[string]any{
		// u1's earliest event is false even though a later one is true.
		segmentRow("u1", 10, true, false),
		segmentRow("u1", 30, true, true),
		segmentRow("u2", 20, true, true),
	}
	result, e := EvaluateSegment(request, rows)
	if e != nil || len(result) != 2 {
		t.Fatalf("boolean segmentation failed: %v %v", result, e)
	}
	// Equal size, lexicographic order puts false before true.
	if result[0]["segment_value"] != "false" || result[0]["subjects"] != 1 {
		t.Fatalf("false group must use u1 earliest value: %v", result[0])
	}
	if result[1]["segment_value"] != "true" || result[1]["subjects"] != 1 {
		t.Fatalf("true group wrong: %v", result[1])
	}

	empty, e := EvaluateSegment(request, nil)
	if e != nil || len(empty) != 0 {
		t.Fatalf("empty evidence must produce no groups: %v %v", empty, e)
	}
}

func TestSegmentValidationClosedSet(t *testing.T) {
	validator, e := contract.New()
	if e != nil {
		t.Fatal(e)
	}
	valid := `{"kind":"segment","environment":"development","subject":"uid","time_range":{"from":"2026-11-01T00:00:00Z","to":"2026-11-05T00:00:00Z"},"timezone":"UTC","segment":{"event":{"event_key":"sign_up","schema_version":1},"field":{"kind":"property","event_key":"sign_up","schema_version":1,"key":"plan"}}}`
	if _, e := Parse([]byte(valid), validator); e != nil {
		t.Fatalf("valid segment request rejected: %v", e)
	}
	anonymous := strings.Replace(valid, `"subject":"uid"`, `"subject":"anonymous"`, 1)
	if _, e := Parse([]byte(anonymous), validator); e != nil {
		t.Fatalf("anonymous segment must be accepted: %v", e)
	}
	for _, raw := range []string{
		// Missing subject is rejected by the schema conditional.
		`{"kind":"segment","environment":"development","time_range":{"from":"2026-11-01T00:00:00Z","to":"2026-11-05T00:00:00Z"},"timezone":"UTC","segment":{"event":{"event_key":"sign_up","schema_version":1},"field":{"kind":"property","event_key":"sign_up","schema_version":1,"key":"plan"}}}`,
		// Segment block on a trend query is rejected.
		`{"kind":"trend","environment":"development","time_range":{"from":"2026-11-01T00:00:00Z","to":"2026-11-02T00:00:00Z"},"timezone":"UTC","segment":{"event":{"event_key":"sign_up","schema_version":1},"field":{"kind":"property","event_key":"sign_up","schema_version":1,"key":"plan"}}}`,
		// Top-level event keys conflict with the pinned segment event.
		`{"kind":"segment","environment":"development","subject":"uid","event_keys":["sign_up"],"time_range":{"from":"2026-11-01T00:00:00Z","to":"2026-11-05T00:00:00Z"},"timezone":"UTC","segment":{"event":{"event_key":"sign_up","schema_version":1},"field":{"kind":"property","event_key":"sign_up","schema_version":1,"key":"plan"}}}`,
	} {
		if _, e := Parse([]byte(raw), validator); e == nil {
			t.Fatalf("malformed segment request accepted: %s", raw)
		}
	}

	// Defense-in-depth for internally constructed requests.
	for _, tc := range []struct {
		name string
		edit func(*Request)
		code string
	}{
		{"nil segment", func(r *Request) { r.Segment = nil }, "INVALID_SEGMENT"},
		{"bad event key", func(r *Request) { r.Segment.Event.EventKey = "Bad-Key" }, "INVALID_SEGMENT_EVENT"},
		{"top level event keys", func(r *Request) { r.EventKeys = []string{"sign_up"} }, "INVALID_EVENT_SELECTION"},
		{"missing subject", func(r *Request) { r.Subject = "" }, "SUBJECT_REQUIRED"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := segmentRequest()
			tc.edit(&r)
			e := Validate(&r)
			var problem *fault.Error
			if e == nil || !errors.As(e, &problem) || problem.Code != tc.code {
				t.Fatalf("want %s, got %v", tc.code, e)
			}
		})
	}

	// Scalar-only field types are enforced at compile time when the catalog
	// has resolved the field type.
	for _, resolved := range []string{"array:string", "datetime"} {
		r := segmentRequest()
		r.Segment.Field.ResolvedType = resolved
		e := CheckResolved(r)
		var problem *fault.Error
		if e == nil || !errors.As(e, &problem) || problem.Code != "INVALID_SEGMENT_FIELD" {
			t.Fatalf("resolved type %s must be rejected, got %v", resolved, e)
		}
	}
	for _, resolved := range []string{"string", "boolean", "number", "integer"} {
		r := segmentRequest()
		r.Segment.Field.ResolvedType = resolved
		if e := CheckResolved(r); e != nil {
			t.Fatalf("scalar type %s must compile: %v", resolved, e)
		}
	}
}

func TestSegmentPlannerEmitsBoundedSubjectPull(t *testing.T) {
	r := segmentRequest()
	plan, e := Compile("analytics", "event_facts", Scope{ProjectID: "project-one", Environment: "development"}, r)
	if e != nil {
		t.Fatal(e)
	}
	for _, fragment := range []string{"segment_value", "segment_present", "LIMIT 100001", "isNotNull(uid)", "uid AS subject_id"} {
		if !strings.Contains(plan.SQL, fragment) {
			t.Fatalf("segment SQL missing %s: %s", fragment, plan.SQL)
		}
	}
	if strings.Contains(plan.SQL, "'sign_up'") {
		t.Fatal("event identity must be a bound parameter")
	}
	if len(plan.Columns) != 3 || plan.Columns[0].Key != "segment_value" {
		t.Fatalf("segment result contract wrong: %+v", plan.Columns)
	}

	anonymous := segmentRequest()
	anonymous.Subject = SubjectAnonymous
	anonymousPlan, e := Compile("analytics", "event_facts", Scope{ProjectID: "project-one", Environment: "development"}, anonymous)
	if e != nil {
		t.Fatal(e)
	}
	if !strings.Contains(anonymousPlan.SQL, "anonymous_id AS subject_id") ||
		strings.Contains(anonymousPlan.SQL, "uid AS subject_id") {
		t.Fatalf("anonymous segment must select anonymous_id exclusively: %s", anonymousPlan.SQL)
	}
}

func segmentTemplate() Template {
	return Template{
		Kind:    "segment",
		Subject: SubjectUID,
		Segment: &Segment{
			Event: Step{EventKey: "sign_up", SchemaVersion: 1},
			Field: Field{Kind: "property", EventKey: "sign_up", SchemaVersion: 1, Key: "plan"},
		},
	}
}

func TestSegmentSavedTemplateValidatesPinsAndBinds(t *testing.T) {
	template := segmentTemplate()
	if e := ValidateTemplate(&template); e != nil {
		t.Fatalf("segment template must be reusable: %v", e)
	}
	if template.Segment == nil || template.Segment.Field.ResolvedType != "" || len(template.EventVersions) != 0 {
		t.Fatalf("segment projection changed: %+v", template)
	}
	from := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	bound, e := template.Bind("development", TimeRange{From: from, To: from.AddDate(0, 0, 7)}, "UTC")
	if e != nil || bound.Kind != QueryKindSegment || bound.Segment == nil {
		t.Fatalf("segment bind failed: %v", e)
	}
	for _, tc := range []struct {
		name, code string
		change     func(*Template)
	}{
		{"top level event versions", "INVALID_EVENT_SELECTION", func(t *Template) {
			t.EventVersions = []EventVersion{{EventKey: "sign_up", SchemaVersion: 1}}
		}},
		{"missing segment block", "INVALID_SEGMENT", func(t *Template) { t.Segment = nil }},
		{"missing subject", "SUBJECT_REQUIRED", func(t *Template) { t.Subject = "" }},
		{"unpinned segment event", "INVALID_SEGMENT_EVENT", func(t *Template) { t.Segment.Event.SchemaVersion = 0 }},
		{"trend-only metrics", "INVALID_QUERY_OPTIONS", func(t *Template) { t.Metrics = []Metric{{Op: OpCount}} }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			invalid := segmentTemplate()
			tc.change(&invalid)
			var problem *fault.Error
			err := ValidateTemplate(&invalid)
			if err == nil || !errors.As(err, &problem) || problem.Code != tc.code {
				t.Fatalf("want %s, got %v", tc.code, err)
			}
		})
	}
}
