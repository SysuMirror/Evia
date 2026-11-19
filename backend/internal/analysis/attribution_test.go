package analysis

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/howtomakeaname/Evia/backend/internal/platform/contract"
	"github.com/howtomakeaname/Evia/backend/internal/platform/fault"
)

func attributionRequest(window int64) Request {
	from := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	return Request{
		Kind:        "attribution",
		Environment: "development",
		Subject:     SubjectUID,
		TimeRange:   TimeRange{From: from, To: from.AddDate(0, 0, 7)},
		Timezone:    "UTC",
		Attribution: &Attribution{
			Conversion:    Step{EventKey: "buy", SchemaVersion: 1},
			Touchpoints:   []Step{{EventKey: "ad_click", SchemaVersion: 1}, {EventKey: "email_open", SchemaVersion: 1}},
			WindowSeconds: window,
			Model:         AttributionModelLastTouch,
		},
	}
}

func attributionRow(subject string, second int, event string) map[string]any {
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

func markedRow(subject string, second int, event string, conversion, touchpoint bool) map[string]any {
	row := attributionRow(subject, second, event)
	if conversion {
		row["is_conversion"] = true
	}
	if touchpoint {
		row["is_touchpoint"] = true
	}
	return row
}

func TestEvaluateAttributionLastTouchAndDirectBucket(t *testing.T) {
	rows := []map[string]any{
		// u1: last touchpoint before buy is email_open.
		markedRow("u1", 20, "email_open", false, true),
		markedRow("u1", 30, "buy", true, false),
		// u2: two touchpoints, the later email_open wins.
		markedRow("u2", 15, "ad_click", false, true),
		markedRow("u2", 22, "email_open", false, true),
		markedRow("u2", 35, "buy", true, false),
		// u3: only ad_click before the conversion.
		markedRow("u3", 10, "ad_click", false, true),
		markedRow("u3", 40, "buy", true, false),
		// u5 converts without any touchpoint: fixed direct bucket.
		markedRow("u5", 50, "buy", true, false),
		// u4 never converts and must be excluded.
		markedRow("u4", 60, "ad_click", false, true),
		// Blank subjects are skipped.
		markedRow("", 70, "buy", true, false),
	}

	result, e := EvaluateAttribution(attributionRequest(86400), rows)
	if e != nil {
		t.Fatal(e)
	}
	want := []struct {
		touchpoint string
		subjects   int
		share      float64
	}{
		{"email_open", 2, 0.5},
		{"__direct__", 1, 0.25},
		{"ad_click", 1, 0.25},
	}
	if len(result) != len(want) {
		t.Fatalf("want %d credit rows, got %v", len(want), result)
	}
	for i, expected := range want {
		row := result[i]
		if row["touchpoint"] != expected.touchpoint {
			t.Fatalf("row %d touchpoint=%v want %s", i, row["touchpoint"], expected.touchpoint)
		}
		if row["subjects"] != expected.subjects {
			t.Fatalf("row %d subjects=%v want %d", i, row["subjects"], expected.subjects)
		}
		share, _ := row["share"].(float64)
		if share != expected.share {
			t.Fatalf("row %d share=%v want %v", i, share, expected.share)
		}
	}
}

func TestEvaluateAttributionRespectsLookbackWindow(t *testing.T) {
	// Touchpoint at second 10, conversion at second 100.
	rows := []map[string]any{
		markedRow("u1", 10, "ad_click", false, true),
		markedRow("u1", 100, "buy", true, false),
	}

	// A 60-second window excludes the touchpoint, so the credit is direct.
	direct, e := EvaluateAttribution(attributionRequest(60), rows)
	if e != nil || len(direct) != 1 || direct[0]["touchpoint"] != directAttributionBucket {
		t.Fatalf("outside-window touchpoint must fall back to direct: %v %v", direct, e)
	}

	// A 120-second window keeps the touchpoint eligible.
	credited, e := EvaluateAttribution(attributionRequest(120), rows)
	if e != nil || len(credited) != 1 || credited[0]["touchpoint"] != "ad_click" {
		t.Fatalf("in-window touchpoint must receive the credit: %v %v", credited, e)
	}
}

func TestEvaluateAttributionCausalOrdering(t *testing.T) {
	base := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC).UnixNano()
	rows := []map[string]any{
		{
			"subject_id": "u9", "event_key": "ad_click", "event_time_ns": base,
			"sequence": int64(1), "session_id": "s", "tab_id": "t", "source_id": "src",
			"event_id": "a", "is_touchpoint": true,
		},
		{
			"subject_id": "u9", "event_key": "buy", "event_time_ns": base,
			"sequence": int64(2), "session_id": "s", "tab_id": "t", "source_id": "src",
			"event_id": "b", "is_conversion": true,
		},
	}
	result, e := EvaluateAttribution(attributionRequest(3600), rows)
	if e != nil || len(result) != 1 || result[0]["touchpoint"] != "ad_click" {
		t.Fatalf("equal-time causal touchpoint must be credited before conversion: %v %v", result, e)
	}

	empty, e := EvaluateAttribution(attributionRequest(3600), nil)
	if e != nil || len(empty) != 0 {
		t.Fatalf("empty evidence must produce no credits: %v %v", empty, e)
	}
}

func TestAttributionValidationClosedSet(t *testing.T) {
	validator, e := contract.New()
	if e != nil {
		t.Fatal(e)
	}
	valid := `{"kind":"attribution","environment":"development","subject":"uid","time_range":{"from":"2026-11-01T00:00:00Z","to":"2026-11-05T00:00:00Z"},"timezone":"UTC","attribution":{"conversion":{"event_key":"buy","schema_version":1},"touchpoints":[{"event_key":"ad_click","schema_version":1}],"window_seconds":86400,"model":"last_touch"}}`
	if _, e := Parse([]byte(valid), validator); e != nil {
		t.Fatalf("valid attribution request rejected: %v", e)
	}
	for _, raw := range []string{
		// A model outside the explicit enum is rejected by the schema.
		`{"kind":"attribution","environment":"development","subject":"uid","time_range":{"from":"2026-11-01T00:00:00Z","to":"2026-11-05T00:00:00Z"},"timezone":"UTC","attribution":{"conversion":{"event_key":"buy","schema_version":1},"touchpoints":[{"event_key":"ad_click","schema_version":1}],"window_seconds":86400,"model":"data_driven"}}`,
		// Window must be within 1..604800.
		`{"kind":"attribution","environment":"development","subject":"uid","time_range":{"from":"2026-11-01T00:00:00Z","to":"2026-11-05T00:00:00Z"},"timezone":"UTC","attribution":{"conversion":{"event_key":"buy","schema_version":1},"touchpoints":[{"event_key":"ad_click","schema_version":1}],"window_seconds":0,"model":"last_touch"}}`,
		// At least one touchpoint is required.
		`{"kind":"attribution","environment":"development","subject":"uid","time_range":{"from":"2026-11-01T00:00:00Z","to":"2026-11-05T00:00:00Z"},"timezone":"UTC","attribution":{"conversion":{"event_key":"buy","schema_version":1},"touchpoints":[],"window_seconds":86400,"model":"last_touch"}}`,
		// Subject is mandatory.
		`{"kind":"attribution","environment":"development","time_range":{"from":"2026-11-01T00:00:00Z","to":"2026-11-05T00:00:00Z"},"timezone":"UTC","attribution":{"conversion":{"event_key":"buy","schema_version":1},"touchpoints":[{"event_key":"ad_click","schema_version":1}],"window_seconds":86400,"model":"last_touch"}}`,
	} {
		if _, e := Parse([]byte(raw), validator); e == nil {
			t.Fatalf("malformed attribution request accepted: %s", raw)
		}
	}

	// Defense-in-depth for internally constructed requests.
	for _, tc := range []struct {
		name string
		edit func(*Request)
		code string
	}{
		{"nil attribution", func(r *Request) { r.Attribution = nil }, "INVALID_ATTRIBUTION"},
		{"missing subject", func(r *Request) { r.Subject = "" }, "SUBJECT_REQUIRED"},
		{"unknown model", func(r *Request) { r.Attribution.Model = "data_driven" }, "INVALID_ATTRIBUTION_MODEL"},
		{"zero window", func(r *Request) { r.Attribution.WindowSeconds = 0 }, "INVALID_ATTRIBUTION_WINDOW"},
		{"oversized window", func(r *Request) { r.Attribution.WindowSeconds = 604801 }, "INVALID_ATTRIBUTION_WINDOW"},
		{"conversion overlap", func(r *Request) {
			r.Attribution.Touchpoints[0] = Step{EventKey: "buy", SchemaVersion: 1}
		}, "INVALID_ATTRIBUTION_TOUCHPOINT"},
		{"duplicate touchpoint", func(r *Request) {
			r.Attribution.Touchpoints = []Step{
				{EventKey: "ad_click", SchemaVersion: 1},
				{EventKey: "ad_click", SchemaVersion: 1},
			}
		}, "INVALID_ATTRIBUTION_TOUCHPOINT"},
		{"too many touchpoints", func(r *Request) {
			steps := make([]Step, 9)
			for i := range steps {
				steps[i] = Step{EventKey: "event_" + string(rune('a'+i)), SchemaVersion: 1}
			}
			r.Attribution.Touchpoints = steps
		}, "INVALID_ATTRIBUTION_TOUCHPOINTS"},
		{"top level event keys", func(r *Request) { r.EventKeys = []string{"buy"} }, "INVALID_EVENT_SELECTION"},
		{"top level event versions", func(r *Request) { r.EventVersions = []EventVersion{{EventKey: "buy", SchemaVersion: 1}} }, "INVALID_EVENT_SELECTION"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := attributionRequest(86400)
			tc.edit(&r)
			e := Validate(&r)
			var problem *fault.Error
			if e == nil || !errors.As(e, &problem) || problem.Code != tc.code {
				t.Fatalf("want %s, got %v", tc.code, e)
			}
		})
	}
}

func TestAttributionPlannerEmitsMarkersAndSubjectPull(t *testing.T) {
	r := attributionRequest(86400)
	plan, e := Compile("analytics", "event_facts", Scope{ProjectID: "project-one", Environment: "development"}, r)
	if e != nil {
		t.Fatal(e)
	}
	for _, fragment := range []string{"is_conversion", "is_touchpoint", "LIMIT 100001", "isNotNull(uid)", "uid AS subject_id"} {
		if !strings.Contains(plan.SQL, fragment) {
			t.Fatalf("attribution SQL missing %s: %s", fragment, plan.SQL)
		}
	}
	if len(plan.Columns) != 3 || plan.Columns[0].Key != "touchpoint" {
		t.Fatalf("attribution result contract wrong: %+v", plan.Columns)
	}

	anonymous := attributionRequest(86400)
	anonymous.Subject = SubjectAnonymous
	anonymousPlan, e := Compile("analytics", "event_facts", Scope{ProjectID: "project-one", Environment: "development"}, anonymous)
	if e != nil {
		t.Fatal(e)
	}
	if !strings.Contains(anonymousPlan.SQL, "anonymous_id AS subject_id") || strings.Contains(anonymousPlan.SQL, "uid AS subject_id") {
		t.Fatalf("anonymous attribution must select anonymous_id exclusively: %s", anonymousPlan.SQL)
	}
}

func firstTouchRequest(window int64) Request {
	r := attributionRequest(window)
	r.Attribution.Model = AttributionModelFirstTouch
	return r
}

func TestEvaluateFirstTouchVsLastTouchDiverge(t *testing.T) {
	// ad_click then email_open before the conversion: first and last touch
	// must credit different touchpoints for the same evidence.
	rows := []map[string]any{
		markedRow("u1", 10, "ad_click", false, true),
		markedRow("u1", 20, "email_open", false, true),
		markedRow("u1", 30, "buy", true, false),
	}

	last, e := EvaluateAttribution(attributionRequest(86400), rows)
	if e != nil || len(last) != 1 || last[0]["touchpoint"] != "email_open" {
		t.Fatalf("last-touch must credit email_open: %v %v", last, e)
	}

	first, e := EvaluateAttribution(firstTouchRequest(86400), rows)
	if e != nil || len(first) != 1 || first[0]["touchpoint"] != "ad_click" {
		t.Fatalf("first-touch must credit ad_click: %v %v", first, e)
	}
}

func TestEvaluateFirstTouchRespectsLookbackWindow(t *testing.T) {
	// Two touchpoints; only the later one is inside the tight window.
	rows := []map[string]any{
		markedRow("u1", 10, "ad_click", false, true),
		markedRow("u1", 95, "email_open", false, true),
		markedRow("u1", 100, "buy", true, false),
	}

	// A 30-second window excludes ad_click, so the earliest eligible
	// touchpoint is email_open.
	first, e := EvaluateAttribution(firstTouchRequest(30), rows)
	if e != nil || len(first) != 1 || first[0]["touchpoint"] != "email_open" {
		t.Fatalf("windowed first-touch must skip out-of-window ad_click: %v %v", first, e)
	}

	// A 120-second window makes ad_click the earliest eligible touchpoint.
	wide, e := EvaluateAttribution(firstTouchRequest(120), rows)
	if e != nil || len(wide) != 1 || wide[0]["touchpoint"] != "ad_click" {
		t.Fatalf("wide-window first-touch must credit ad_click: %v %v", wide, e)
	}

	// With no in-window touchpoint both models fall back to the direct bucket.
	direct, e := EvaluateAttribution(firstTouchRequest(1), rows)
	if e != nil || len(direct) != 1 || direct[0]["touchpoint"] != directAttributionBucket {
		t.Fatalf("first-touch without an in-window touchpoint must be direct: %v %v", direct, e)
	}
}

func TestFirstTouchValidationAcceptsModelAndRejectsUnknown(t *testing.T) {
	validator, e := contract.New()
	if e != nil {
		t.Fatal(e)
	}
	valid := `{"kind":"attribution","environment":"development","subject":"uid","time_range":{"from":"2026-11-01T00:00:00Z","to":"2026-11-05T00:00:00Z"},"timezone":"UTC","attribution":{"conversion":{"event_key":"buy","schema_version":1},"touchpoints":[{"event_key":"ad_click","schema_version":1}],"window_seconds":86400,"model":"first_touch"}}`
	parsed, e := Parse([]byte(valid), validator)
	if e != nil {
		t.Fatalf("first_touch must be accepted: %v", e)
	}
	if parsed.Attribution.Model != AttributionModelFirstTouch {
		t.Fatalf("model must deserialize to first_touch, got %q", parsed.Attribution.Model)
	}

	// Internally constructed requests also accept first_touch and reject unknown models.
	good := firstTouchRequest(86400)
	if e := Validate(&good); e != nil {
		t.Fatalf("first_touch request must pass Go validation: %v", e)
	}
	bad := firstTouchRequest(86400)
	bad.Attribution.Model = "data_driven"
	var problem *fault.Error
	if e := Validate(&bad); e == nil || !errors.As(e, &problem) || problem.Code != "INVALID_ATTRIBUTION_MODEL" {
		t.Fatalf("unknown model must fail closed: %v", e)
	}
}

func linearRequest(window int64) Request {
	r := attributionRequest(window)
	r.Attribution.Model = AttributionModelLinear
	return r
}

func TestEvaluateLinearEqualWeightAndDirectBucket(t *testing.T) {
	rows := []map[string]any{
		// u1 touches two distinct events, so each receives weight 1/2.
		markedRow("u1", 10, "ad_click", false, true),
		markedRow("u1", 20, "email_open", false, true),
		markedRow("u1", 30, "buy", true, false),
		// u2 touches only ad_click, giving it full weight.
		markedRow("u2", 15, "ad_click", false, true),
		markedRow("u2", 25, "buy", true, false),
		// u3 converts without any touchpoint: direct keeps full weight.
		markedRow("u3", 40, "buy", true, false),
	}

	result, e := EvaluateAttribution(linearRequest(86400), rows)
	if e != nil {
		t.Fatal(e)
	}
	want := map[string]struct {
		subjects int
		credit   float64
	}{
		"ad_click":              {2, 1.5},
		directAttributionBucket: {1, 1.0},
		"email_open":            {1, 0.5},
	}
	if len(result) != len(want) {
		t.Fatalf("linear wants %d rows, got %v", len(want), result)
	}
	shareSum := 0.0
	for _, row := range result {
		key, _ := row["touchpoint"].(string)
		expected, ok := want[key]
		if !ok {
			t.Fatalf("unexpected touchpoint %q: %v", key, row)
		}
		if row["subjects"] != expected.subjects {
			t.Fatalf("%s subjects=%v want %d", key, row["subjects"], expected.subjects)
		}
		credit, _ := row["credit"].(float64)
		if credit != expected.credit {
			t.Fatalf("%s credit=%v want %v", key, credit, expected.credit)
		}
		share, _ := row["share"].(float64)
		shareSum += share
	}
	// Credits total the three converters, so shares must sum to one within
	// floating-point accumulation tolerance.
	if diff := shareSum - 1.0; diff > 1e-12 || diff < -1e-12 {
		t.Fatalf("linear shares must sum to 1, got %v", shareSum)
	}
}

func TestEvaluateLinearDedupesRepeatedTouchpoint(t *testing.T) {
	// The same touchpoint key repeats; linear counts distinct keys once, so
	// it is one node receiving full weight rather than two 1/2 nodes.
	rows := []map[string]any{
		markedRow("u1", 10, "ad_click", false, true),
		markedRow("u1", 15, "ad_click", false, true),
		markedRow("u1", 20, "buy", true, false),
	}
	result, e := EvaluateAttribution(linearRequest(86400), rows)
	if e != nil || len(result) != 1 {
		t.Fatalf("deduped linear path wants one row: %v %v", result, e)
	}
	row := result[0]
	if row["touchpoint"] != "ad_click" || row["subjects"] != 1 {
		t.Fatalf("linear dedupe mismatch: %v", row)
	}
	credit, _ := row["credit"].(float64)
	if credit != 1.0 {
		t.Fatalf("deduped single touchpoint credit must be 1, got %v", credit)
	}
}

func TestEvaluateLinearRespectsLookbackWindow(t *testing.T) {
	// ad_click at 10s is outside a 30s window ending at the 100s conversion.
	rows := []map[string]any{
		markedRow("u1", 10, "ad_click", false, true),
		markedRow("u1", 95, "email_open", false, true),
		markedRow("u1", 100, "buy", true, false),
	}
	result, e := EvaluateAttribution(linearRequest(30), rows)
	if e != nil || len(result) != 1 {
		t.Fatalf("windowed linear wants one row: %v %v", result, e)
	}
	if result[0]["touchpoint"] != "email_open" {
		t.Fatalf("out-of-window touchpoint must be excluded: %v", result[0])
	}
	credit, _ := result[0]["credit"].(float64)
	if credit != 1.0 {
		t.Fatalf("only eligible touchpoint must carry full weight: %v", credit)
	}

	// A one-second window removes every touchpoint, collapsing to direct.
	direct, e := EvaluateAttribution(linearRequest(1), rows)
	if e != nil || len(direct) != 1 || direct[0]["touchpoint"] != directAttributionBucket {
		t.Fatalf("linear without in-window touchpoint must be direct: %v %v", direct, e)
	}
}

func TestLinearValidationAndPlannerContract(t *testing.T) {
	validator, e := contract.New()
	if e != nil {
		t.Fatal(e)
	}
	valid := `{"kind":"attribution","environment":"development","subject":"uid","time_range":{"from":"2026-11-01T00:00:00Z","to":"2026-11-05T00:00:00Z"},"timezone":"UTC","attribution":{"conversion":{"event_key":"buy","schema_version":1},"touchpoints":[{"event_key":"ad_click","schema_version":1}],"window_seconds":86400,"model":"linear"}}`
	parsed, e := Parse([]byte(valid), validator)
	if e != nil {
		t.Fatalf("linear must be accepted: %v", e)
	}
	if parsed.Attribution.Model != AttributionModelLinear {
		t.Fatalf("model must deserialize to linear, got %q", parsed.Attribution.Model)
	}

	r := linearRequest(86400)
	if e := Validate(&r); e != nil {
		t.Fatalf("linear request must pass Go validation: %v", e)
	}
	plan, e := Compile("analytics", "event_facts", Scope{ProjectID: "project-one", Environment: "development"}, r)
	if e != nil {
		t.Fatal(e)
	}
	keys := map[string]bool{}
	for _, column := range plan.Columns {
		keys[column.Key] = true
	}
	if !keys["credit"] {
		t.Fatalf("linear plan must expose the credit column: %+v", plan.Columns)
	}

	// Single-touch plans keep their original three columns byte-stable.
	single := attributionRequest(86400)
	singlePlan, e := Compile("analytics", "event_facts", Scope{ProjectID: "project-one", Environment: "development"}, single)
	if e != nil {
		t.Fatal(e)
	}
	for _, column := range singlePlan.Columns {
		if column.Key == "credit" {
			t.Fatalf("single-touch plans must not emit credit: %+v", singlePlan.Columns)
		}
	}
}

func decayRequest(window, halfLife int64) Request {
	r := attributionRequest(window)
	r.Attribution.Model = AttributionModelTimeDecay
	r.Attribution.DecayHalfLifeSeconds = halfLife
	return r
}

func approxEqual(got, want, tol float64) bool {
	diff := got - want
	return diff <= tol && diff >= -tol
}

func TestEvaluateTimeDecayFavorsCloserTouchpointAndNormalizes(t *testing.T) {
	// ad_click at 10s, email_open at 20s, conversion at 30s. With a 10s
	// half-life the raw weights are 0.25 and 0.5, normalizing to 1/3 and 2/3.
	rows := []map[string]any{
		markedRow("u1", 10, "ad_click", false, true),
		markedRow("u1", 20, "email_open", false, true),
		markedRow("u1", 30, "buy", true, false),
	}
	result, e := EvaluateAttribution(decayRequest(86400, 10), rows)
	if e != nil {
		t.Fatal(e)
	}
	credits := map[string]float64{}
	shareSum := 0.0
	for _, row := range result {
		key, _ := row["touchpoint"].(string)
		credit, _ := row["credit"].(float64)
		share, _ := row["share"].(float64)
		credits[key] = credit
		shareSum += share
	}
	if !approxEqual(credits["ad_click"], 1.0/3.0, 1e-12) || !approxEqual(credits["email_open"], 2.0/3.0, 1e-12) {
		t.Fatalf("decay weights must normalize to 1/3 and 2/3: %v", credits)
	}
	if !approxEqual(shareSum, 1.0, 1e-12) {
		t.Fatalf("decay shares must sum to 1: %v", shareSum)
	}
	// The closer touchpoint must always outrank the older one.
	if credits["email_open"] <= credits["ad_click"] {
		t.Fatalf("closer touchpoint must receive more weight: %v", credits)
	}
}

func TestEvaluateTimeDecayDedupesToNearestInstance(t *testing.T) {
	// Two ad_click occurrences; the nearest (15s, delta 5s) wins and a single
	// touchpoint normalizes to full weight.
	rows := []map[string]any{
		markedRow("u1", 10, "ad_click", false, true),
		markedRow("u1", 15, "ad_click", false, true),
		markedRow("u1", 20, "buy", true, false),
	}
	result, e := EvaluateAttribution(decayRequest(86400, 10), rows)
	if e != nil || len(result) != 1 {
		t.Fatalf("deduped decay path wants one row: %v %v", result, e)
	}
	credit, _ := result[0]["credit"].(float64)
	if !approxEqual(credit, 1.0, 1e-12) {
		t.Fatalf("single distinct touchpoint must normalize to 1: %v", credit)
	}
}

func TestEvaluateTimeDecayWindowAndDirectBucket(t *testing.T) {
	// ad_click at 10s is outside a 30s window ending at the 100s conversion.
	rows := []map[string]any{
		markedRow("u1", 10, "ad_click", false, true),
		markedRow("u1", 95, "email_open", false, true),
		markedRow("u1", 100, "buy", true, false),
	}
	windowed, e := EvaluateAttribution(decayRequest(30, 10), rows)
	if e != nil || len(windowed) != 1 || windowed[0]["touchpoint"] != "email_open" {
		t.Fatalf("out-of-window touchpoint must be excluded: %v %v", windowed, e)
	}

	direct, e := EvaluateAttribution(decayRequest(1, 10), rows)
	if e != nil || len(direct) != 1 || direct[0]["touchpoint"] != directAttributionBucket {
		t.Fatalf("decay without in-window touchpoint must be direct: %v %v", direct, e)
	}
	credit, _ := direct[0]["credit"].(float64)
	if !approxEqual(credit, 1.0, 1e-12) {
		t.Fatalf("direct bucket must carry full weight: %v", credit)
	}
}

func TestTimeDecayRequiresBoundedHalfLife(t *testing.T) {
	validator, e := contract.New()
	if e != nil {
		t.Fatal(e)
	}
	valid := `{"kind":"attribution","environment":"development","subject":"uid","time_range":{"from":"2026-11-01T00:00:00Z","to":"2026-11-05T00:00:00Z"},"timezone":"UTC","attribution":{"conversion":{"event_key":"buy","schema_version":1},"touchpoints":[{"event_key":"ad_click","schema_version":1}],"window_seconds":86400,"model":"time_decay","decay_half_life_seconds":600}}`
	parsed, e := Parse([]byte(valid), validator)
	if e != nil {
		t.Fatalf("time_decay with half-life must be accepted: %v", e)
	}
	if parsed.Attribution.DecayHalfLifeSeconds != 600 {
		t.Fatalf("half-life must deserialize, got %d", parsed.Attribution.DecayHalfLifeSeconds)
	}

	// Go defense-in-depth: missing or out-of-range half-life fails closed.
	missing := decayRequest(86400, 10)
	missing.Attribution.DecayHalfLifeSeconds = 0
	var problem *fault.Error
	if e := Validate(&missing); e == nil || !errors.As(e, &problem) || problem.Code != "INVALID_ATTRIBUTION_HALF_LIFE" {
		t.Fatalf("missing half-life must fail closed: %v", e)
	}
	oversized := decayRequest(86400, 10)
	oversized.Attribution.DecayHalfLifeSeconds = 604801
	if e := Validate(&oversized); e == nil || !errors.As(e, &problem) || problem.Code != "INVALID_ATTRIBUTION_HALF_LIFE" {
		t.Fatalf("oversized half-life must fail closed: %v", e)
	}

	// A non-decay model does not require the half-life and still validates.
	single := attributionRequest(86400)
	if e := Validate(&single); e != nil {
		t.Fatalf("last-touch must not require a half-life: %v", e)
	}
}

func TestTimeDecayPlannerEmitsCreditColumn(t *testing.T) {
	r := decayRequest(86400, 600)
	plan, e := Compile("analytics", "event_facts", Scope{ProjectID: "project-one", Environment: "development"}, r)
	if e != nil {
		t.Fatal(e)
	}
	keys := map[string]bool{}
	for _, column := range plan.Columns {
		keys[column.Key] = true
	}
	if !keys["credit"] {
		t.Fatalf("time_decay plan must expose credit: %+v", plan.Columns)
	}
}

func positionRequest(window int64) Request {
	r := attributionRequest(window)
	r.Attribution.Model = AttributionModelPositionBased
	return r
}

func TestPositionBasedWeightsTable(t *testing.T) {
	for _, tc := range []struct {
		n      int
		first  float64
		last   float64
		middle float64
	}{
		{1, 1.0, 1.0, 0},
		{2, 0.5, 0.5, 0},
		{3, 0.4, 0.4, 0.2},
		{4, 0.4, 0.4, 0.1},
		{5, 0.4, 0.4, 0.2 / 3.0},
	} {
		weights := positionBasedWeights(tc.n)
		if len(weights) != tc.n {
			t.Fatalf("n=%d wants %d weights, got %v", tc.n, tc.n, weights)
		}
		sum := 0.0
		for _, weight := range weights {
			sum += weight
		}
		if !approxEqual(sum, 1.0, 1e-12) {
			t.Fatalf("n=%d weights must total one, got %v (%v)", tc.n, sum, weights)
		}
		if !approxEqual(weights[0], tc.first, 1e-12) || !approxEqual(weights[tc.n-1], tc.last, 1e-12) {
			t.Fatalf("n=%d edge weights wrong: %v", tc.n, weights)
		}
		for i := 1; i < tc.n-1; i++ {
			if !approxEqual(weights[i], tc.middle, 1e-12) {
				t.Fatalf("n=%d middle weight %d wrong: %v", tc.n, i, weights)
			}
		}
	}
}

func TestEvaluatePositionBasedUShapeWeightsAndDirectBucket(t *testing.T) {
	rows := []map[string]any{
		// u1 touches three distinct events -> 0.4 / 0.2 / 0.4 U-shape.
		markedRow("u1", 10, "ad_click", false, true),
		markedRow("u1", 20, "email_open", false, true),
		markedRow("u1", 30, "social_click", false, true),
		markedRow("u1", 40, "buy", true, false),
		// u2 touches two distinct events -> 0.5 / 0.5 even split.
		markedRow("u2", 10, "ad_click", false, true),
		markedRow("u2", 20, "email_open", false, true),
		markedRow("u2", 30, "buy", true, false),
		// u3 touches one event -> full weight.
		markedRow("u3", 15, "ad_click", false, true),
		markedRow("u3", 25, "buy", true, false),
		// u4 converts without any touchpoint -> direct bucket full weight.
		markedRow("u4", 50, "buy", true, false),
	}

	result, e := EvaluateAttribution(positionRequest(86400), rows)
	if e != nil {
		t.Fatal(e)
	}
	want := map[string]struct {
		subjects int
		credit   float64
	}{
		"ad_click":              {3, 1.9},
		"email_open":            {2, 0.7},
		"social_click":          {1, 0.4},
		directAttributionBucket: {1, 1.0},
	}
	if len(result) != len(want) {
		t.Fatalf("position-based wants %d rows, got %v", len(want), result)
	}
	shareSum := 0.0
	for _, row := range result {
		key, _ := row["touchpoint"].(string)
		expected, ok := want[key]
		if !ok {
			t.Fatalf("unexpected touchpoint %q: %v", key, row)
		}
		if row["subjects"] != expected.subjects {
			t.Fatalf("%s subjects=%v want %d", key, row["subjects"], expected.subjects)
		}
		credit, _ := row["credit"].(float64)
		if !approxEqual(credit, expected.credit, 1e-12) {
			t.Fatalf("%s credit=%v want %v", key, credit, expected.credit)
		}
		share, _ := row["share"].(float64)
		shareSum += share
	}
	if diff := shareSum - 1.0; diff > 1e-12 || diff < -1e-12 {
		t.Fatalf("position-based shares must sum to 1, got %v", shareSum)
	}
}

func TestEvaluatePositionBasedDedupesRepeatedTouchpoint(t *testing.T) {
	// A repeated key is one distinct node carrying full weight, not two nodes.
	rows := []map[string]any{
		markedRow("u1", 10, "ad_click", false, true),
		markedRow("u1", 15, "ad_click", false, true),
		markedRow("u1", 20, "buy", true, false),
	}
	result, e := EvaluateAttribution(positionRequest(86400), rows)
	if e != nil || len(result) != 1 {
		t.Fatalf("deduped position-based path wants one row: %v %v", result, e)
	}
	row := result[0]
	if row["touchpoint"] != "ad_click" || row["subjects"] != 1 {
		t.Fatalf("position-based dedupe mismatch: %v", row)
	}
	credit, _ := row["credit"].(float64)
	if !approxEqual(credit, 1.0, 1e-12) {
		t.Fatalf("deduped single touchpoint credit must be 1, got %v", credit)
	}
}

func TestEvaluatePositionBasedRespectsLookbackWindow(t *testing.T) {
	// ad_click at 10s is outside a 30s window ending at the 100s conversion.
	rows := []map[string]any{
		markedRow("u1", 10, "ad_click", false, true),
		markedRow("u1", 95, "email_open", false, true),
		markedRow("u1", 100, "buy", true, false),
	}
	result, e := EvaluateAttribution(positionRequest(30), rows)
	if e != nil || len(result) != 1 {
		t.Fatalf("windowed position-based wants one row: %v %v", result, e)
	}
	if result[0]["touchpoint"] != "email_open" {
		t.Fatalf("out-of-window touchpoint must be excluded: %v", result[0])
	}
	credit, _ := result[0]["credit"].(float64)
	if !approxEqual(credit, 1.0, 1e-12) {
		t.Fatalf("only eligible touchpoint must carry full weight: %v", credit)
	}

	// A one-second window removes every touchpoint, collapsing to direct.
	direct, e := EvaluateAttribution(positionRequest(1), rows)
	if e != nil || len(direct) != 1 || direct[0]["touchpoint"] != directAttributionBucket {
		t.Fatalf("position-based without in-window touchpoint must be direct: %v %v", direct, e)
	}
}

func TestPositionBasedValidationAndPlannerContract(t *testing.T) {
	validator, e := contract.New()
	if e != nil {
		t.Fatal(e)
	}
	valid := `{"kind":"attribution","environment":"development","subject":"uid","time_range":{"from":"2026-11-01T00:00:00Z","to":"2026-11-05T00:00:00Z"},"timezone":"UTC","attribution":{"conversion":{"event_key":"buy","schema_version":1},"touchpoints":[{"event_key":"ad_click","schema_version":1}],"window_seconds":86400,"model":"position_based"}}`
	parsed, e := Parse([]byte(valid), validator)
	if e != nil {
		t.Fatalf("position_based must be accepted: %v", e)
	}
	if parsed.Attribution.Model != AttributionModelPositionBased {
		t.Fatalf("model must deserialize to position_based, got %q", parsed.Attribution.Model)
	}

	r := positionRequest(86400)
	if e := Validate(&r); e != nil {
		t.Fatalf("position_based request must pass Go validation: %v", e)
	}
	plan, e := Compile("analytics", "event_facts", Scope{ProjectID: "project-one", Environment: "development"}, r)
	if e != nil {
		t.Fatal(e)
	}
	keys := map[string]bool{}
	for _, column := range plan.Columns {
		keys[column.Key] = true
	}
	if !keys["credit"] {
		t.Fatalf("position_based plan must expose the credit column: %+v", plan.Columns)
	}
}

func attributionTemplate() Template {
	return Template{
		Kind:    "attribution",
		Subject: SubjectUID,
		Attribution: &Attribution{
			Conversion:    Step{EventKey: "buy", SchemaVersion: 1},
			Touchpoints:   []Step{{EventKey: "ad_click", SchemaVersion: 1}},
			WindowSeconds: 86400,
			Model:         AttributionModelLastTouch,
		},
	}
}

func TestAttributionSavedTemplateValidatesPinsAndBinds(t *testing.T) {
	template := attributionTemplate()
	if e := ValidateTemplate(&template); e != nil {
		t.Fatalf("attribution template must be reusable: %v", e)
	}
	if template.Attribution == nil || len(template.EventVersions) != 0 {
		t.Fatalf("attribution projection changed: %+v", template)
	}
	from := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	bound, e := template.Bind("development", TimeRange{From: from, To: from.AddDate(0, 0, 7)}, "UTC")
	if e != nil || bound.Kind != "attribution" || bound.Attribution == nil {
		t.Fatalf("attribution bind failed: %v", e)
	}
	for _, tc := range []struct {
		name, code string
		change     func(*Template)
	}{
		{"top level event versions", "INVALID_EVENT_SELECTION", func(t *Template) {
			t.EventVersions = []EventVersion{{EventKey: "buy", SchemaVersion: 1}}
		}},
		{"missing attribution block", "INVALID_ATTRIBUTION", func(t *Template) { t.Attribution = nil }},
		{"missing subject", "SUBJECT_REQUIRED", func(t *Template) { t.Subject = "" }},
		{"window out of range", "INVALID_ATTRIBUTION_WINDOW", func(t *Template) { t.Attribution.WindowSeconds = 0 }},
		{"unknown model", "INVALID_ATTRIBUTION_MODEL", func(t *Template) { t.Attribution.Model = "data_driven" }},
		{"trend-only metrics", "INVALID_QUERY_OPTIONS", func(t *Template) { t.Metrics = []Metric{{Op: "count"}} }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			invalid := attributionTemplate()
			tc.change(&invalid)
			var problem *fault.Error
			err := ValidateTemplate(&invalid)
			if err == nil || !errors.As(err, &problem) || problem.Code != tc.code {
				t.Fatalf("want %s, got %v", tc.code, err)
			}
		})
	}
}
