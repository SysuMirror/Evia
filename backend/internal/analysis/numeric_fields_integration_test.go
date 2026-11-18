//go:build integration

package analysis

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

func TestClickHouseNumericFieldsIncludeJSONDoubleWithoutCoercion(t *testing.T) {
	client := clickHouseDatabase(t)
	chExec(t, client, "CREATE VIEW event_facts_reference AS SELECT * FROM event_facts")
	properties := []string{`{"value":1.5}`, `{"value":-2.25}`, `{"value":1e2}`, `{"value":1.00}`, `{"value":0}`, `{"value":-3}`, `{"value":2.5e-1}`, `{"value":null}`, `{}`, `{"value":"100"}`, `{"value":true}`, `{"value":[]}`, `{"value":{}}`}
	var rows []map[string]any
	for index, document := range properties {
		row := fact(fmt.Sprintf("numeric-%d", index), "opened", "user", "device", "source-one", index, nil)
		row["properties_json"] = document
		rows = append(rows, row)
	}
	loser := cloneTrendFact(rows[0])
	loser["received_at"], loser["properties_json"], loser["event_key"], loser["schema_version"] = "2026-11-07 10:00:59.000000000", `{"value":999}`, "closed", 2
	otherSource := cloneTrendFact(rows[0])
	otherSource["source_id"], otherSource["properties_json"] = "source-two", `{"value":4.5}`
	otherVersion := cloneTrendFact(rows[0])
	otherVersion["event_id"], otherVersion["schema_version"], otherVersion["properties_json"] = "other-version", 2, `{"value":200}`
	otherEvent := cloneTrendFact(rows[0])
	otherEvent["event_id"], otherEvent["event_key"], otherEvent["properties_json"] = "other-event", "closed", `{"value":300}`
	otherProject := cloneTrendFact(rows[0])
	otherProject["project_id"], otherProject["properties_json"] = "other-project", `{"value":400}`
	otherEnvironment := cloneTrendFact(rows[0])
	otherEnvironment["environment"], otherEnvironment["properties_json"] = "production", `{"value":500}`
	rows = append(rows, loser, otherSource, otherVersion, otherEvent, otherProject, otherEnvironment)
	seedRows(t, client, rows)
	for _, table := range []string{"event_facts_reference", "event_facts"} {
		for _, kind := range []string{"number", "integer"} {
			t.Run(table+"/"+kind, func(t *testing.T) {
				field := Field{Kind: "property", EventKey: "opened", SchemaVersion: 1, Key: "value", ResolvedType: kind}
				base := request(t, baseRequest)
				base.EventVersions = []EventVersion{{EventKey: "opened", SchemaVersion: 1}}
				r := base
				r.Metrics = []Metric{{Op: "count"}, {Op: "sum", Field: &field}, {Op: "avg", Field: &field}, {Op: "min", Field: &field}, {Op: "max", Field: &field}, {Op: "p95", Field: &field}}
				got := trendPlanRows(t, client, r, table)
				if len(got) != 1 {
					t.Fatal("expected one aggregate", got)
				}
				for index, want := range []float64{14, 102, 12.75, -3, 100, 100} {
					number, ok := got[0][fmt.Sprintf("metric_%d", index)].(json.Number)
					if !ok {
						t.Fatal("numeric aggregate is missing", index, got)
					}
					value, err := number.Float64()
					if err != nil || value != want {
						t.Fatal("wrong numeric aggregate", index, value, want, err)
					}
				}
				for _, filter := range []struct{ op, value, count string }{{"gte", "0", "6"}, {"lt", "0", "2"}, {"eq", "100", "1"}, {"neq", "100", "7"}} {
					r = base
					r.Filter = &Filter{Op: filter.op, Field: &field, Value: json.Number(filter.value)}
					got = trendPlanRows(t, client, r, table)
					if len(got) != 1 || got[0]["metric_0"] != json.Number(filter.count) {
						t.Fatal("wrong numeric filter cohort", filter, got)
					}
				}
				r = base
				r.GroupBy = []Field{field}
				got = trendPlanRows(t, client, r, table)
				groups := map[string]string{}
				for _, row := range got {
					groups[fmt.Sprint(row["group_0"])+"/"+fmt.Sprint(row["group_0_present"])] = fmt.Sprint(row["metric_0"])
				}
				want := map[string]string{"-3/1": "1", "-2.25/1": "1", "0/1": "1", "0.25/1": "1", "1/1": "1", "1.5/1": "1", "4.5/1": "1", "100/1": "1", "<nil>/0": "1", "<nil>/1": "5"}
				if !reflect.DeepEqual(groups, want) {
					t.Fatal("numeric grouping coerced or omitted values", groups, want)
				}
			})
		}
	}
}

func TestClickHousePercentileLevels(t *testing.T) {
	client := clickHouseDatabase(t)
	// 101 sorted values 1..101 keep every quantile level on an integer index,
	// so quantileExact has no interpolation ambiguity.
	rows := make([]map[string]any, 0, 101)
	for value := 1; value <= 101; value++ {
		row := fact(fmt.Sprintf("pct-%d", value), "opened", "user", "", "source-one", value, nil)
		row["properties_json"] = fmt.Sprintf(`{"score":%d}`, value)
		rows = append(rows, row)
	}
	seedRows(t, client, rows)

	field := Field{Kind: "property", EventKey: "opened", SchemaVersion: 1, Key: "score", ResolvedType: "number"}
	for op, level := range map[string]float64{"p50": 51, "p90": 91, "p95": 96, "p99": 100} {
		r := request(t, baseRequest)
		r.EventVersions = []EventVersion{{EventKey: "opened", SchemaVersion: 1}}
		r.Metrics = []Metric{{Op: op, Field: &field, Accuracy: "exact"}}
		got := trendPlanRows(t, client, r, "event_facts")
		if len(got) != 1 {
			t.Fatalf("%s expected one aggregate row, got %v", op, got)
		}
		number, ok := got[0]["metric_0"].(json.Number)
		if !ok {
			t.Fatalf("%s missing metric_0: %v", op, got[0])
		}
		value, err := number.Float64()
		if err != nil || value != level {
			t.Fatalf("%s exact percentile=%v want %v (%v)", op, value, level, err)
		}
		// Approximate TDigest must still resolve to a finite in-range number.
		r.Metrics[0].Accuracy = "approximate"
		approx := trendPlanRows(t, client, r, "event_facts")
		approxNumber, ok := approx[0]["metric_0"].(json.Number)
		if !ok {
			t.Fatalf("%s approximate missing metric_0: %v", op, approx[0])
		}
		approxValue, err := approxNumber.Float64()
		if err != nil || approxValue < 1 || approxValue > 101 {
			t.Fatalf("%s approximate percentile out of range: %v (%v)", op, approxValue, err)
		}
	}
}

func TestClickHouseNumericArrayExtractionRetainsExistingCoercionAndSignedZero(t *testing.T) {
	client := clickHouseDatabase(t)
	row := fact("array-coercion", "opened", "user", "device", "source", 0, nil)
	row["properties_json"] = `{"values":[-0.0,1e2,1.00,true,"2"]}`
	seedRows(t, client, []map[string]any{row})
	data := chExec(t, client, "SELECT JSONExtract(properties_json,'values','Array(Float64)') AS numbers,JSONExtract(properties_json,'values','Array(String)') AS strings FROM event_facts FORMAT JSONEachRow")
	var got struct {
		Numbers []json.Number `json:"numbers"`
		Strings []string      `json:"strings"`
	}
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.UseNumber()
	if err := decoder.Decode(&got); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got.Numbers, []json.Number{"-0", "100", "1", "1", "2"}) || !reflect.DeepEqual(got.Strings, []string{"-0", "100", "1", "true", "2"}) {
		t.Fatal("existing array extraction changed", got)
	}
}
