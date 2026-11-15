package analysis

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/howtomakeaname/Evia/backend/internal/platform/contract"
	"github.com/howtomakeaname/Evia/backend/internal/platform/fault"
)

func pathRequest(depth int) Request {
	from := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	return Request{
		Kind:        "path",
		Environment: "development",
		Subject:     "uid",
		TimeRange:   TimeRange{From: from, To: from.AddDate(0, 0, 7)},
		Timezone:    "UTC",
		Path:        &Path{Depth: depth},
	}
}

func pathRow(subject string, second int, event string) map[string]any {
	return map[string]any{
		"subject_id":    subject,
		"event_id":      subject + "-" + event + "-" + time.Unix(int64(second), 0).Format("150405"),
		"event_key":     event,
		"event_time_ns": time.Date(2026, 11, 1, 0, 0, second, 0, time.UTC).UnixNano(),
		"sequence":      int64(second),
		"session_id":    "s",
		"tab_id":        "t",
		"source_id":     "src",
	}
}

func TestEvaluatePathAggregatesOrderedSequences(t *testing.T) {
	rows := []map[string]any{
		// u1: open -> view -> buy
		pathRow("u1", 10, "open"),
		pathRow("u1", 20, "view"),
		pathRow("u1", 30, "buy"),
		// u2 follows the same three-event path.
		pathRow("u2", 15, "open"),
		pathRow("u2", 25, "view"),
		pathRow("u2", 35, "buy"),
		// u3: open -> view -> share
		pathRow("u3", 12, "open"),
		pathRow("u3", 22, "view"),
		pathRow("u3", 32, "share"),
		// u4 only has two events and cannot fill a depth-3 path.
		pathRow("u4", 40, "open"),
		pathRow("u4", 50, "view"),
		// Blank subjects are skipped.
		pathRow("", 60, "open"),
	}
	result, e := EvaluatePath(pathRequest(3), rows)
	if e != nil {
		t.Fatal(e)
	}
	if len(result) != 2 {
		t.Fatalf("want two distinct paths, got %v", result)
	}
	first := result[0]
	gotPath, ok := first["path"].([]string)
	if !ok || len(gotPath) != 3 {
		t.Fatalf("path column shape wrong: %#v", first["path"])
	}
	want := []struct {
		path     []string
		subjects int
	}{
		{[]string{"open", "view", "buy"}, 2},
		{[]string{"open", "view", "share"}, 1},
	}
	for i, expected := range want {
		row := result[i]
		steps, ok := row["path"].([]string)
		if !ok || strings.Join(steps, ",") != strings.Join(expected.path, ",") {
			t.Fatalf("path %d mismatch: %v", i, row)
		}
		if row["subjects"] != expected.subjects {
			t.Fatalf("path %d subjects=%v want %d", i, row["subjects"], expected.subjects)
		}
	}
}

func TestEvaluatePathPinnedStartAndCausalOrdering(t *testing.T) {
	request := pathRequest(2)
	request.Path.StartEvent = &Step{EventKey: "view", SchemaVersion: 1}
	rows := []map[string]any{
		// Events listed out of order; the start is the middle event for u1.
		pathRow("u1", 30, "buy"),
		pathRow("u1", 20, "view"),
		pathRow("u1", 10, "open"),
		// u2 never reaches the pinned start.
		pathRow("u2", 10, "open"),
	}
	rows[1]["is_start"] = true
	result, e := EvaluatePath(request, rows)
	if e != nil {
		t.Fatal(e)
	}
	if len(result) != 1 {
		t.Fatalf("only u1 reaches the start event, got %v", result)
	}
	steps, ok := result[0]["path"].([]string)
	if !ok || len(steps) != 2 || steps[0] != "view" || steps[1] != "buy" {
		t.Fatalf("pinned path must start at view and continue to buy: %v", result[0])
	}
	if result[0]["subjects"] != 1 {
		t.Fatalf("one subject expected: %v", result[0])
	}

	// Equal-time events use the session causal sequence rather than arrival.
	equalBase := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC).UnixNano()
	ordered := []map[string]any{
		{
			"subject_id": "u9", "event_key": "first", "event_time_ns": equalBase,
			"sequence": int64(1), "session_id": "s", "tab_id": "t", "source_id": "src", "event_id": "a",
		},
		{
			"subject_id": "u9", "event_key": "second", "event_time_ns": equalBase,
			"sequence": int64(2), "session_id": "s", "tab_id": "t", "source_id": "src", "event_id": "b",
		},
	}
	orderedResult, e := EvaluatePath(pathRequest(2), ordered)
	if e != nil || len(orderedResult) != 1 {
		t.Fatalf("equal-time path failed: %v %v", orderedResult, e)
	}
	equalSteps, _ := orderedResult[0]["path"].([]string)
	if equalSteps[0] != "first" || equalSteps[1] != "second" {
		t.Fatalf("causal sequence not honored for equal timestamps: %v", equalSteps)
	}
}

func TestEvaluatePathEmptyAndShortSubjects(t *testing.T) {
	empty, e := EvaluatePath(pathRequest(2), nil)
	if e != nil || len(empty) != 0 {
		t.Fatalf("empty evidence must produce no paths: %v %v", empty, e)
	}
	// A single event cannot satisfy a depth-2 path.
	short, e := EvaluatePath(pathRequest(2), []map[string]any{pathRow("u1", 10, "open")})
	if e != nil || len(short) != 0 {
		t.Fatalf("short subjects must be excluded: %v %v", short, e)
	}
}

func TestPathValidationClosedSet(t *testing.T) {
	validator, e := contract.New()
	if e != nil {
		t.Fatal(e)
	}
	valid := `{"kind":"path","environment":"development","subject":"uid","time_range":{"from":"2026-11-01T00:00:00Z","to":"2026-11-05T00:00:00Z"},"timezone":"UTC","path":{"depth":3}}`
	if _, e := Parse([]byte(valid), validator); e != nil {
		t.Fatalf("valid path request rejected: %v", e)
	}
	withStart := `{"kind":"path","environment":"development","subject":"anonymous","time_range":{"from":"2026-11-01T00:00:00Z","to":"2026-11-05T00:00:00Z"},"timezone":"UTC","path":{"depth":2,"start_event":{"event_key":"open","schema_version":1}}}`
	if _, e := Parse([]byte(withStart), validator); e != nil {
		t.Fatalf("pinned-start path must be accepted: %v", e)
	}
	// Schema rejects out-of-range depth and missing subject.
	for _, raw := range []string{
		`{"kind":"path","environment":"development","subject":"uid","time_range":{"from":"2026-11-01T00:00:00Z","to":"2026-11-05T00:00:00Z"},"timezone":"UTC","path":{"depth":9}}`,
		`{"kind":"path","environment":"development","time_range":{"from":"2026-11-01T00:00:00Z","to":"2026-11-05T00:00:00Z"},"timezone":"UTC","path":{"depth":3}}`,
		`{"kind":"trend","environment":"development","time_range":{"from":"2026-11-01T00:00:00Z","to":"2026-11-02T00:00:00Z"},"timezone":"UTC","path":{"depth":3}}`,
	} {
		if _, e := Parse([]byte(raw), validator); e == nil {
			t.Fatalf("malformed path request accepted: %s", raw)
		}
	}

	// Defense-in-depth for internally constructed requests.
	for _, tc := range []struct {
		name string
		edit func(*Request)
		code string
	}{
		{"nil path", func(r *Request) { r.Path = nil }, "INVALID_PATH"},
		{"depth too small", func(r *Request) { r.Path.Depth = 1 }, "INVALID_PATH_DEPTH"},
		{"depth too large", func(r *Request) { r.Path.Depth = 9 }, "INVALID_PATH_DEPTH"},
		{"missing subject", func(r *Request) { r.Subject = "" }, "SUBJECT_REQUIRED"},
		{"bad start event", func(r *Request) { r.Path.StartEvent = &Step{EventKey: "Bad-Key", SchemaVersion: 1} }, "INVALID_PATH_START_EVENT"},
		{"top level event keys", func(r *Request) { r.EventKeys = []string{"open"} }, "INVALID_EVENT_SELECTION"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := pathRequest(3)
			tc.edit(&r)
			e := Validate(&r)
			var problem *fault.Error
			if e == nil || !errors.As(e, &problem) || problem.Code != tc.code {
				t.Fatalf("want %s, got %v", tc.code, e)
			}
		})
	}
}

func TestPathPlannerEmitsBoundedSubjectPull(t *testing.T) {
	r := pathRequest(3)
	plan, e := Compile("analytics", "event_facts", Scope{ProjectID: "project-one", Environment: "development"}, r)
	if e != nil {
		t.Fatal(e)
	}
	for _, fragment := range []string{"event_key", "LIMIT 100001", "isNotNull(uid)", "uid AS subject_id", "sequence"} {
		if !strings.Contains(plan.SQL, fragment) {
			t.Fatalf("path SQL missing %s: %s", fragment, plan.SQL)
		}
	}
	if len(plan.Columns) != 2 || plan.Columns[0].Key != "path" {
		t.Fatalf("path result contract wrong: %+v", plan.Columns)
	}

	pinned := pathRequest(2)
	pinned.Path.StartEvent = &Step{EventKey: "open", SchemaVersion: 1}
	pinnedPlan, e := Compile("analytics", "event_facts", Scope{ProjectID: "project-one", Environment: "development"}, pinned)
	if e != nil {
		t.Fatal(e)
	}
	if !strings.Contains(pinnedPlan.SQL, "is_start") {
		t.Fatalf("pinned path must select an is_start marker: %s", pinnedPlan.SQL)
	}

	anonymous := pathRequest(2)
	anonymous.Subject = "anonymous"
	anonymousPlan, e := Compile("analytics", "event_facts", Scope{ProjectID: "project-one", Environment: "development"}, anonymous)
	if e != nil {
		t.Fatal(e)
	}
	if !strings.Contains(anonymousPlan.SQL, "anonymous_id AS subject_id") ||
		strings.Contains(anonymousPlan.SQL, "uid AS subject_id") {
		t.Fatalf("anonymous path must select anonymous_id exclusively: %s", anonymousPlan.SQL)
	}
}

func pathTemplate(start *Step) Template {
	return Template{
		Kind:    "path",
		Subject: "uid",
		Path:    &Path{Depth: 3, StartEvent: start},
	}
}

func TestPathSavedTemplateValidatesPinsAndBinds(t *testing.T) {
	for _, hasStart := range []bool{false, true} {
		var start *Step
		if hasStart {
			start = &Step{EventKey: "open", SchemaVersion: 1}
		}
		template := pathTemplate(start)
		if e := ValidateTemplate(&template); e != nil {
			t.Fatalf("path template (start=%t) must be reusable: %v", hasStart, e)
		}
		from := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
		bound, e := template.Bind("development", TimeRange{From: from, To: from.AddDate(0, 0, 7)}, "UTC")
		if e != nil || bound.Kind != "path" || bound.Path == nil || bound.Path.Depth != 3 {
			t.Fatalf("path bind failed (start=%t): %v", hasStart, e)
		}
	}
	for _, tc := range []struct {
		name, code string
		change     func(*Template)
	}{
		{"top level event versions", "INVALID_EVENT_SELECTION", func(t *Template) {
			t.EventVersions = []EventVersion{{EventKey: "open", SchemaVersion: 1}}
		}},
		{"missing path block", "INVALID_PATH", func(t *Template) { t.Path = nil }},
		{"missing subject", "SUBJECT_REQUIRED", func(t *Template) { t.Subject = "" }},
		{"depth out of range", "INVALID_PATH_DEPTH", func(t *Template) { t.Path.Depth = 1 }},
		{"unpinned start event", "INVALID_PATH_START_EVENT", func(t *Template) {
			t.Path.StartEvent = &Step{EventKey: "open", SchemaVersion: 0}
		}},
		{"trend-only metrics", "INVALID_QUERY_OPTIONS", func(t *Template) { t.Metrics = []Metric{{Op: "count"}} }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			invalid := pathTemplate(nil)
			tc.change(&invalid)
			var problem *fault.Error
			err := ValidateTemplate(&invalid)
			if err == nil || !errors.As(err, &problem) || problem.Code != tc.code {
				t.Fatalf("want %s, got %v", tc.code, err)
			}
		})
	}
}

func TestEvaluatePathCollapseRepeatsOptIn(t *testing.T) {
	// open, open, view: the raw depth-2 path repeats open; folding changes it.
	rows := []map[string]any{
		pathRow("u1", 10, "open"),
		pathRow("u1", 20, "open"),
		pathRow("u1", 30, "view"),
	}

	// Default behavior is byte-stable: repeats are preserved.
	raw := pathRequest(2)
	rawResult, e := EvaluatePath(raw, rows)
	if e != nil || len(rawResult) != 1 {
		t.Fatalf("default path must keep repeats: %v %v", rawResult, e)
	}
	rawSteps, _ := rawResult[0]["path"].([]string)
	if len(rawSteps) != 2 || rawSteps[0] != "open" || rawSteps[1] != "open" {
		t.Fatalf("default path must be open,open: %v", rawResult[0])
	}

	// Opt-in folding collapses the adjacent run before depth extraction.
	collapsed := pathRequest(2)
	collapsed.Path.CollapseRepeats = true
	collapsedResult, e := EvaluatePath(collapsed, rows)
	if e != nil || len(collapsedResult) != 1 {
		t.Fatalf("collapsed path failed: %v %v", collapsedResult, e)
	}
	foldedSteps, _ := collapsedResult[0]["path"].([]string)
	if len(foldedSteps) != 2 || foldedSteps[0] != "open" || foldedSteps[1] != "view" {
		t.Fatalf("folded path must be open,view: %v", collapsedResult[0])
	}
}

func TestEvaluatePathCollapseAppliesDepthAndExcludesShortSubjects(t *testing.T) {
	// Six raw events fold to three distinct consecutive nodes.
	rows := []map[string]any{
		pathRow("u1", 10, "open"),
		pathRow("u1", 20, "open"),
		pathRow("u1", 30, "open"),
		pathRow("u1", 40, "view"),
		pathRow("u1", 50, "view"),
		pathRow("u1", 60, "buy"),
	}
	collapsed := pathRequest(3)
	collapsed.Path.CollapseRepeats = true
	result, e := EvaluatePath(collapsed, rows)
	if e != nil || len(result) != 1 {
		t.Fatalf("depth-3 folded path failed: %v %v", result, e)
	}
	steps, _ := result[0]["path"].([]string)
	if len(steps) != 3 || steps[0] != "open" || steps[1] != "view" || steps[2] != "buy" {
		t.Fatalf("folded depth-3 path mismatch: %v", steps)
	}

	// The same subject only has two distinct consecutive nodes, so a depth-3
	// folded path excludes it even though the raw sequence has six events.
	tooDeep := pathRequest(3)
	tooDeep.Path.CollapseRepeats = true
	excluded, e := EvaluatePath(tooDeep, []map[string]any{
		pathRow("u2", 10, "open"),
		pathRow("u2", 20, "open"),
		pathRow("u2", 30, "view"),
	})
	if e != nil || len(excluded) != 0 {
		t.Fatalf("subject too short after folding must be excluded: %v %v", excluded, e)
	}

	// Non-adjacent identical keys are distinct nodes and must not be folded.
	nonAdjacent, e := EvaluatePath(collapsed, []map[string]any{
		pathRow("u3", 10, "open"),
		pathRow("u3", 20, "view"),
		pathRow("u3", 30, "open"),
	})
	if e != nil || len(nonAdjacent) != 1 {
		t.Fatalf("non-adjacent repeats must remain distinct: %v %v", nonAdjacent, e)
	}
	naSteps, _ := nonAdjacent[0]["path"].([]string)
	if len(naSteps) != 3 || naSteps[0] != "open" || naSteps[1] != "view" || naSteps[2] != "open" {
		t.Fatalf("non-adjacent nodes must not fold: %v", naSteps)
	}
}

func TestEvaluatePathCollapseKeepsPinnedStart(t *testing.T) {
	// A run of view events carries the start marker; folding must retain it so
	// the pinned begin still resolves inside the compressed sequence.
	rows := []map[string]any{
		pathRow("u1", 10, "open"),
		pathRow("u1", 20, "view"),
		pathRow("u1", 30, "view"),
		pathRow("u1", 40, "buy"),
	}
	rows[1]["is_start"] = true
	rows[2]["is_start"] = true

	r := pathRequest(2)
	r.Path.CollapseRepeats = true
	r.Path.StartEvent = &Step{EventKey: "view", SchemaVersion: 1}
	result, e := EvaluatePath(r, rows)
	if e != nil || len(result) != 1 {
		t.Fatalf("pinned start after folding failed: %v %v", result, e)
	}
	steps, _ := result[0]["path"].([]string)
	if len(steps) != 2 || steps[0] != "view" || steps[1] != "buy" {
		t.Fatalf("pinned folded path must begin at view: %v", steps)
	}
}

func TestPathCollapseRepeatsContract(t *testing.T) {
	validator, e := contract.New()
	if e != nil {
		t.Fatal(e)
	}
	enabled := `{"kind":"path","environment":"development","subject":"uid","time_range":{"from":"2026-11-01T00:00:00Z","to":"2026-11-05T00:00:00Z"},"timezone":"UTC","path":{"depth":3,"collapse_repeats":true}}`
	parsed, e := Parse([]byte(enabled), validator)
	if e != nil {
		t.Fatalf("collapse_repeats must be accepted: %v", e)
	}
	if !parsed.Path.CollapseRepeats {
		t.Fatal("collapse_repeats must deserialize to true")
	}
	// A non-boolean value is rejected by the schema rather than coerced.
	badType := `{"kind":"path","environment":"development","subject":"uid","time_range":{"from":"2026-11-01T00:00:00Z","to":"2026-11-05T00:00:00Z"},"timezone":"UTC","path":{"depth":3,"collapse_repeats":"yes"}}`
	if _, e := Parse([]byte(badType), validator); e == nil {
		t.Fatal("non-boolean collapse_repeats must be rejected")
	}
}
