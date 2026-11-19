//go:build integration

package analysis

import "testing"

func TestClickHousePinnedEventVersionsFilterCountAndExactUV(t *testing.T) {
	client := clickHouseDatabase(t)
	makeFact := func(id, event, uid string, version int, second int) map[string]any {
		row := fact(id, event, uid, "anonymous-"+uid, "source-one", second, map[string]any{})
		row["schema_version"] = version
		return row
	}
	rows := []map[string]any{
		makeFact("open-v1-a", "opened", "one", 1, 0),
		makeFact("open-v1-b", "opened", "one", 1, 1),
		makeFact("open-v2-a", "opened", "two", 2, 2),
		makeFact("open-v2-b", "opened", "three", 2, 3),
		makeFact("close-v1", "closed", "four", 1, 4),
		makeFact("close-v2", "closed", "five", 2, 5),
	}
	foreignProject := makeFact("foreign-project", "opened", "foreign", 1, 6)
	foreignProject["project_id"] = "project-two"
	foreignEnvironment := makeFact("foreign-environment", "opened", "foreign", 1, 7)
	foreignEnvironment["environment"] = "production"
	seedRows(t, client, append(rows, foreignProject, foreignEnvironment))
	r := request(t, baseRequest)
	r.Subject = SubjectUID
	r.Metrics = []Metric{{Op: OpCount, Accuracy: AccuracyExact}, {Op: OpUnique, Accuracy: AccuracyExact}}
	for _, tc := range []struct {
		name     string
		versions []EventVersion
		count    float64
		uv       float64
	}{
		{"version one", []EventVersion{{EventKey: "opened", SchemaVersion: 1}}, 2, 1},
		{"version two", []EventVersion{{EventKey: "opened", SchemaVersion: 2}}, 2, 2},
		{"exact pairs avoid cross product", []EventVersion{{EventKey: "opened", SchemaVersion: 1}, {EventKey: "closed", SchemaVersion: 2}}, 3, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r.EventVersions = tc.versions
			result := execute(t, client, r)
			if got := metricNumber(t, result, "metric_0"); got != tc.count {
				t.Fatal("version count", got, tc.count)
			}
			if got := metricNumber(t, result, "metric_1"); got != tc.uv {
				t.Fatal("version exact UID unique", got, tc.uv)
			}
		})
	}
	r.EventVersions = nil
	r.EventKeys = []string{"opened"}
	result := execute(t, client, r)
	if metricNumber(t, result, "metric_0") != 4 || metricNumber(t, result, "metric_1") != 3 {
		t.Fatal("event keys no longer span versions", result)
	}
	r.EventKeys = nil
	r.EventVersions = []EventVersion{{EventKey: "opened", SchemaVersion: 1}}
	r.Kind = QueryKindEvents
	r.Metrics = nil
	details := execute(t, client, r)
	if len(details.Rows) != 2 {
		t.Fatal("event details did not filter version", details)
	}
	uid := "two"
	r.Kind = QueryKindJourney
	r.IdentityFilter = &IdentityFilter{UID: &uid}
	if journey := execute(t, client, r); len(journey.Rows) != 0 {
		t.Fatal("journey included an excluded version", journey)
	}
}
