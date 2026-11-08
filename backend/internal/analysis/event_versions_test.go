package analysis

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"testing"

	"github.com/howtomakeaname/Evia/backend/internal/platform/contract"
)

func TestEventVersionSelectionContractAndPolicy(t *testing.T) {
	validator, e := contract.New()
	if e != nil {
		t.Fatal(e)
	}
	with := func(options string) string {
		return strings.TrimSuffix(baseRequest, "}") + "," + options + "}"
	}
	for _, tc := range []struct{ name, options string }{
		{"empty", `"event_versions":[]`},
		{"null", `"event_versions":null`},
		{"missing key", `"event_versions":[{"schema_version":1}]`},
		{"missing version", `"event_versions":[{"event_key":"opened"}]`},
		{"unknown field", `"event_versions":[{"event_key":"opened","schema_version":1,"sql":"1=1"}]`},
		{"duplicate JSON key", `"event_versions":[{"event_key":"opened","event_key":"closed","schema_version":1}]`},
		{"event injection", `"event_versions":[{"event_key":"opened') OR 1=1 --","schema_version":1}]`},
		{"version injection", `"event_versions":[{"event_key":"opened","schema_version":"1 OR 1=1"}]`},
		{"zero", `"event_versions":[{"event_key":"opened","schema_version":0}]`},
		{"negative", `"event_versions":[{"event_key":"opened","schema_version":-1}]`},
		{"fractional", `"event_versions":[{"event_key":"opened","schema_version":1.5}]`},
		{"unsafe integer", `"event_versions":[{"event_key":"opened","schema_version":9007199254740992}]`},
		{"duplicate pair", `"event_versions":[{"event_key":"opened","schema_version":1},{"event_key":"opened","schema_version":1}]`},
		{"multiple versions", `"event_versions":[{"event_key":"opened","schema_version":1},{"event_key":"opened","schema_version":2}]`},
		{"both selectors", `"event_keys":["opened"],"event_versions":[{"event_key":"opened","schema_version":1}]`},
		{"both selectors with empty keys", `"event_keys":[],"event_versions":[{"event_key":"opened","schema_version":1}]`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, e := Parse([]byte(with(tc.options)), validator); e == nil {
				t.Fatal("invalid selection accepted")
			}
		})
	}
	versions := make([]EventVersion, 21)
	for i := range versions {
		versions[i] = EventVersion{EventKey: fmt.Sprintf("event_%d", i), SchemaVersion: 1}
	}
	for _, count := range []int{1, 20, 21} {
		raw, e := json.Marshal(versions[:count])
		if e != nil {
			t.Fatal(e)
		}
		r, e := Parse([]byte(with(`"event_versions":`+string(raw))), validator)
		if count == 21 {
			if e == nil {
				t.Fatal("unbounded event versions accepted")
			}
			continue
		}
		if e != nil || len(r.EventVersions) != count || r.EventVersions[count-1] != versions[count-1] {
			t.Fatal("bounded selection lost in normalization", r, e)
		}
		encoded, e := json.Marshal(r)
		if e != nil {
			t.Fatal(e)
		}
		if _, e = Parse(encoded, validator); e != nil {
			t.Fatal("normalized request no longer satisfies contract", e)
		}
	}
	// These direct selectors retain their existing cross-version behavior.
	for _, options := range []string{`"event_keys":[]`, `"event_keys":["opened"]`} {
		if _, e := Parse([]byte(with(options)), validator); e != nil {
			t.Fatal("existing selector rejected", e)
		}
	}
	raw := strings.Replace(with(`"subject":"uid","event_versions":[{"event_key":"opened","schema_version":1}],"funnel":{"mode":"ordered_with_interleaving","window_seconds":60,"steps":[{"event_key":"opened","schema_version":1},{"event_key":"closed","schema_version":2}]}`), `"kind":"trend"`, `"kind":"funnel"`, 1)
	if _, e := Parse([]byte(raw), validator); e == nil {
		t.Fatal("funnel accepted a competing version selector")
	}
}

func TestEventVersionSelectionBindsExactPairsInFactPredicate(t *testing.T) {
	r := request(t, baseRequest)
	r.EventVersions = []EventVersion{{EventKey: "opened", SchemaVersion: 2}, {EventKey: "closed", SchemaVersion: MaxSafeInteger}}
	plan, e := Compile("db", "event_facts", Scope{ProjectID: "p", Environment: r.Environment}, r)
	if e != nil {
		t.Fatal(e)
	}
	pairs := regexp.MustCompile(`AND \(\(event_key=\{(p[0-9]+):String\} AND schema_version=\{(p[0-9]+):UInt64\}\) OR \(event_key=\{(p[0-9]+):String\} AND schema_version=\{(p[0-9]+):UInt64\}\)\)`).FindStringSubmatch(plan.SQL)
	if len(pairs) != 5 || strings.Contains(plan.SQL, "opened") || strings.Contains(plan.SQL, "closed") || strings.Contains(plan.SQL, "9007199254740991") {
		t.Fatal("exact version pairs missing or values interpolated", plan.SQL)
	}
	for index, value := range []string{"opened", "2", "closed", "9007199254740991"} {
		key := pairs[index+1]
		if plan.Parameters[key] != value {
			t.Fatal("missing bound selector", key, plan.Parameters)
		}
	}
	for _, versions := range [][]EventVersion{{}, {{EventKey: "opened", SchemaVersion: 0}}, {{EventKey: "opened", SchemaVersion: 1}, {EventKey: "opened", SchemaVersion: 2}}, {{EventKey: "opened' OR 1=1", SchemaVersion: 1}}} {
		r.EventVersions = versions
		if _, e := Compile("db", "event_facts", Scope{ProjectID: "p", Environment: r.Environment}, r); e == nil {
			t.Fatal("direct compilation bypassed selector validation", versions)
		}
	}
	r.EventVersions = nil
	r.EventKeys = []string{"opened"}
	plan, e = Compile("db", "event_facts", Scope{ProjectID: "p", Environment: r.Environment}, r)
	if e != nil || !strings.Contains(plan.SQL, "event_key IN (") || strings.Contains(plan.SQL, "schema_version=") {
		t.Fatal("direct event keys lost cross-version semantics", plan, e)
	}
}

func TestPinnedEventVersionFieldsMustBelongToSelection(t *testing.T) {
	for _, placement := range []string{"filter", "group", "metric"} {
		for _, selected := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/selected=%t", placement, selected), func(t *testing.T) {
				r := request(t, baseRequest)
				r.EventVersions = []EventVersion{{EventKey: "opened", SchemaVersion: 1}}
				field := Field{Kind: "property", EventKey: "opened", SchemaVersion: 2, Key: "score", ResolvedType: "integer"}
				if selected {
					field.SchemaVersion = 1
				}
				switch placement {
				case "filter":
					r.Filter = &Filter{Op: "eq", Field: &field, Value: json.Number("1")}
				case "group":
					r.GroupBy = []Field{field}
				case "metric":
					r.Metrics = []Metric{{Op: "sum", Field: &field, Accuracy: "exact"}}
				}
				_, e := Compile("db", "event_facts", Scope{ProjectID: "p", Environment: r.Environment}, r)
				if (e == nil) != selected {
					t.Fatal("selected property version policy", selected, e)
				}
			})
		}
	}
}
