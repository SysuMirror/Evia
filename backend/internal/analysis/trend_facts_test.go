package analysis

import (
	"strings"
	"testing"
)

func TestTrendWinnerProjectsOnlyRequiredColumns(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(*Request)
		want []string
	}{
		{"count", func(*Request) {}, nil},
		{"event keys", func(r *Request) { r.EventKeys = []string{"opened"} }, []string{"event_key"}},
		{"versions", func(r *Request) { r.EventVersions = []EventVersion{{EventKey: "opened", SchemaVersion: 2}} }, []string{"event_key", "schema_version"}},
		{"UID subject", func(r *Request) { r.Subject = SubjectUID; r.Metrics = []Metric{{Op: OpUnique}} }, []string{"uid"}},
		{"anonymous subject", func(r *Request) { r.Subject = SubjectAnonymous; r.Metrics = []Metric{{Op: OpUnique}} }, []string{"anonymous_id"}},
		{"identity filter", func(r *Request) { uid := "synthetic"; r.IdentityFilter = &IdentityFilter{UID: &uid} }, []string{"uid"}},
		{"anonymous identity filter", func(r *Request) { id := "synthetic"; r.IdentityFilter = &IdentityFilter{AnonymousID: &id} }, []string{"anonymous_id"}},
		{"property metric", func(r *Request) {
			r.Metrics = []Metric{{Op: OpSum, Field: &Field{Kind: "property", EventKey: "opened", SchemaVersion: 1, Key: "score", ResolvedType: "number"}}}
		}, []string{"event_key", "schema_version", "properties_json"}},
		{"nested filters", func(r *Request) {
			r.Filter = &Filter{Op: "or", Args: []*Filter{{Op: "eq", Field: &Field{Kind: "system", Key: "uid", ResolvedType: "string"}, Value: "one"}, {Op: "is_missing", Field: &Field{Kind: "property", EventKey: "opened", SchemaVersion: 1, Key: "score", ResolvedType: "number"}}}}
		}, []string{"event_key", "schema_version", "properties_json", "uid"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := request(t, baseRequest)
			tc.edit(&r)
			plan, e := Compile("testdb", "event_facts", Scope{ProjectID: "project-one", Environment: r.Environment}, r)
			if e != nil {
				t.Fatal(e)
			}
			want := map[string]bool{"event_time": true, "received_at": true}
			for _, name := range tc.want {
				want[name] = true
			}
			for _, name := range []string{"event_key", "schema_version", "event_time", "received_at", "uid", "anonymous_id", "session_id", "page_view_id", "operation_id", "properties_json"} {
				if strings.Contains(plan.SQL, " AS "+name+",") || strings.Contains(plan.SQL, " AS "+name+" FROM") {
					if !want[name] {
						t.Fatalf("unused winner payload %s: %s", name, plan.SQL)
					}
				} else if want[name] {
					t.Fatalf("required winner payload %s missing: %s", name, plan.SQL)
				}
			}
			if strings.Count(plan.SQL, "argMin(") != 1 || !strings.Contains(plan.SQL, "),"+trendWinnerWeight+") AS winner") {
				t.Fatal("winner tuple or precision ordering changed", plan.SQL)
			}
			for _, unused := range []string{"context_json", "source_kind", "sdk_name", "sdk_version", "release_revision", "route_name"} {
				if strings.Contains(plan.SQL, unused) {
					t.Fatal("unneeded raw payload was loaded", unused)
				}
			}
			for _, original := range []string{"occurred_at_precise", "event_time_precise", "toDateTime64(", "unhex("} {
				if strings.Contains(plan.SQL, original) {
					t.Fatal("compact winner reader recomputes original payload", original)
				}
			}
		})
	}
}

func TestTrendWinnerOnlyPushesScopeBeforeDeduplication(t *testing.T) {
	r := request(t, baseRequest)
	r.EventVersions = []EventVersion{{EventKey: "opened", SchemaVersion: 2}}
	r.Filter = &Filter{Op: "eq", Field: &Field{Kind: "property", EventKey: "opened", SchemaVersion: 2, Key: "label", ResolvedType: "string"}, Value: "' OR 1=1 --"}
	plan, e := Compile("testdb", "event_facts", Scope{ProjectID: "private-project", Environment: r.Environment}, r)
	if e != nil {
		t.Fatal(e)
	}
	start := strings.Index(plan.SQL, "FROM `testdb`.`event_facts_raw` AS raw_facts WHERE ")
	if start < 0 {
		t.Fatal("deduplicated raw source missing", plan.SQL)
	}
	end := strings.Index(plan.SQL[start:], " GROUP BY source_id,event_id")
	if end < 0 {
		t.Fatal("deduplication scope or identity missing", plan.SQL)
	}
	inner := plan.SQL[start : start+end]
	if strings.Count(inner, "=") != 2 || !strings.Contains(inner, "WHERE raw_facts.project_id=") || !strings.Contains(inner, " AND raw_facts.environment=") {
		t.Fatal("payload predicate moved ahead of deduplication", inner)
	}
	for _, value := range []string{"private-project", "opened", "label", "' OR 1=1 --"} {
		if strings.Contains(plan.SQL, value) {
			t.Fatal("request value interpolated into SQL", value)
		}
	}
}

func TestTrendWinnerScopeConstantsStayOutsideAggregation(t *testing.T) {
	r := request(t, baseRequest)
	plan, err := Compile("db", "event_facts", Scope{ProjectID: "private-project", Environment: r.Environment}, r)
	if err != nil {
		t.Fatal(err)
	}
	scopeBinding := func(value, alias string) string {
		t.Helper()
		for name, bound := range plan.Parameters {
			token := "{" + name + ":String}"
			if bound == value && strings.Contains(plan.SQL, token+" AS "+alias) {
				return token
			}
		}
		t.Fatal("outer scope alias does not bind the authorized value", alias)
		return ""
	}
	project := scopeBinding("private-project", "project_id")
	environment := scopeBinding(r.Environment, "environment")
	if !strings.Contains(plan.SQL, "SELECT "+project+" AS project_id,"+environment+" AS environment,source_id,event_id,") ||
		!strings.Contains(plan.SQL, "FROM (SELECT source_id,event_id,argMin(") ||
		!strings.Contains(plan.SQL, "WHERE raw_facts.project_id="+project+" AND raw_facts.environment="+environment) ||
		!strings.Contains(plan.SQL, "GROUP BY source_id,event_id") {
		t.Fatal("constant scope must be reconstructed outside the winner aggregate with qualified raw authorization", plan.SQL)
	}
	if strings.Contains(plan.SQL, "GROUP BY project_id") || strings.Contains(plan.SQL, "SELECT project_id,environment,source_id,event_id,argMin(") {
		t.Fatal("constant scope remains in winner grouping or output", plan.SQL)
	}
}

func TestTrendWinnerWhitelistsSystemFieldsAndLeavesOtherKindsOnView(t *testing.T) {
	for key, kind := range systemTypes {
		r := request(t, baseRequest)
		r.GroupBy = []Field{{Kind: "system", Key: key, ResolvedType: kind}}
		plan, e := Compile("db", "event_facts", Scope{ProjectID: "project-one", Environment: r.Environment}, r)
		if e != nil || !strings.Contains(plan.SQL, " AS "+key) {
			t.Fatal("supported system field lost", key, e, plan.SQL)
		}
	}
	for _, key := range []string{"source_kind", "context_json", "uid) OR 1=1 --"} {
		r := request(t, baseRequest)
		r.GroupBy = []Field{{Kind: "system", Key: key, ResolvedType: "string"}}
		if _, e := Compile("db", "event_facts", Scope{ProjectID: "project-one", Environment: r.Environment}, r); e == nil {
			t.Fatal("unsupported system field selected a physical column", key)
		}
	}
	for _, kind := range []string{"events", "journey"} {
		r := request(t, baseRequest)
		r.Kind, r.Metrics = kind, nil
		plan, e := Compile("db", "event_facts", Scope{ProjectID: "project-one", Environment: r.Environment}, r)
		if e != nil || !strings.Contains(plan.SQL, "FROM `db`.`event_facts` WHERE") || strings.Contains(plan.SQL, "argMin(") {
			t.Fatal("detail query source changed", kind, e, plan.SQL)
		}
	}
}
