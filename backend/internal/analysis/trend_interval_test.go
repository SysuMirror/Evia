package analysis

import (
	"strings"
	"testing"
)

// TestTrendWeekIntervalPlannerSQL proves week uses the Monday-start
// toStartOfWeek(mode 1) with the bound timezone, while minute/hour/day keep
// their existing single-argument bucket calls byte-stable.
func TestTrendWeekIntervalPlannerSQL(t *testing.T) {
	cases := []struct {
		interval string
		want     string
	}{
		{"minute", "toStartOfMinute(event_time,"},
		{"hour", "toStartOfHour(event_time,"},
		{"day", "toStartOfDay(event_time,"},
		{"week", "toStartOfWeek(event_time,1,"},
	}
	for _, tc := range cases {
		r := request(t, baseRequest)
		r.Interval = tc.interval
		r.Metrics = []Metric{{Op: "count", Accuracy: "exact"}}
		plan, e := Compile("db", "event_facts", Scope{ProjectID: "p", Environment: r.Environment}, r)
		if e != nil {
			t.Fatalf("%s compile: %v", tc.interval, e)
		}
		if !strings.Contains(plan.SQL, tc.want) {
			t.Fatalf("%s SQL missing %q: %s", tc.interval, tc.want, plan.SQL)
		}
		if !strings.Contains(plan.SQL, "AS bucket_ms") {
			t.Fatalf("%s lost bucket_ms projection: %s", tc.interval, plan.SQL)
		}
	}
}

// TestTrendIntervalValidationClosedSet exercises the structural closed set with
// the client request shape.
func TestTrendIntervalValidationClosedSet(t *testing.T) {
	if e := Validate(func() *Request {
		r := request(t, baseRequest)
		r.Interval = "week"
		return &r
	}()); e != nil {
		t.Fatalf("week interval must validate: %v", e)
	}
	for _, bad := range []string{"month", "year"} {
		r := request(t, baseRequest)
		r.Interval = bad
		if e := Validate(&r); e == nil {
			t.Fatalf("%s interval must be rejected", bad)
		}
	}
}
