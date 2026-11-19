package analysis

import (
	"encoding/json"
	"testing"
)

func TestResolvedDatetimeFiltersMatchPlannerValidation(t *testing.T) {
	for _, placement := range []string{"scalar", "in", "nested funnel step"} {
		for _, tc := range []struct {
			name, value, bound string
		}{
			{"invalid text", "not-a-date", ""},
			{"invalid calendar date", "2026-02-30T10:00:00Z", ""},
			{"missing offset", "2026-11-07T10:00:00", ""},
			{"UTC", "2026-11-07T10:00:00Z", "2026-11-07 10:00:00.000000000"},
			{"offset", "2026-11-07T18:00:00+08:00", "2026-11-07 10:00:00.000000000"},
			{"nanoseconds", "2026-11-07T10:00:00.123456789Z", "2026-11-07 10:00:00.123456789"},
			{"offset nanoseconds", "2026-11-07T18:00:00.123456789+08:00", "2026-11-07 10:00:00.123456789"},
		} {
			t.Run(placement+"/"+tc.name, func(t *testing.T) {
				r := request(t, baseRequest)
				filter := &Filter{Op: "eq", Field: &Field{Kind: "system", Key: "event_time", ResolvedType: "datetime"}, Value: tc.value}
				switch placement {
				case "scalar":
					r.Filter = filter
				case "in":
					filter.Op = "in"
					filter.Value = []any{"2026-11-07T10:00:00Z", tc.value}
					r.Filter = filter
				case "nested funnel step":
					r.Kind, r.Metrics, r.Subject = "funnel", nil, SubjectUID
					r.Funnel = &Funnel{Mode: "ordered_with_interleaving", WindowSeconds: 60, Steps: []Step{{EventKey: "opened", SchemaVersion: 1, Filter: &Filter{Op: "and", Args: []*Filter{{Op: "or", Args: []*Filter{filter}}}}}, {EventKey: "closed", SchemaVersion: 1}}}
				}
				resolvedError := CheckResolved(r)
				plan, plannerError := Compile("db", "event_facts", Scope{ProjectID: "p", Environment: r.Environment}, r)
				if tc.bound == "" {
					if resolvedError == nil || plannerError == nil {
						t.Fatalf("invalid datetime validation mismatch: resolved=%v planner=%v", resolvedError, plannerError)
					}
					return
				}
				if resolvedError != nil || plannerError != nil {
					t.Fatal("valid RFC3339 datetime rejected", resolvedError, plannerError)
				}
				found := false
				for _, value := range plan.Parameters {
					found = found || value == tc.bound
				}
				if !found {
					t.Fatal("datetime offset or nanoseconds changed", plan.Parameters)
				}
			})
		}
	}
	if e := compatibleScalar("datetime", json.Number("1")); e == nil {
		t.Fatal("numeric datetime accepted")
	}
}
