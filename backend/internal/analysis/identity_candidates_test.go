package analysis

import (
	"strings"
	"testing"
)

func identityEquality(key, value string) *Filter {
	return &Filter{Op: "eq", Field: &Field{Kind: "system", Key: key, ResolvedType: "string"}, Value: value}
}

func TestNecessaryIdentityEquality(t *testing.T) {
	uid, anonymous := "explicit-user", "explicit-anonymous"
	for _, tc := range []struct {
		name    string
		request Request
		key     string
		value   string
	}{
		{"none", Request{}, "", ""},
		{"UID filter", Request{Filter: identityEquality("uid", "user")}, "uid", "user"},
		{"empty equality", Request{Filter: identityEquality("uid", "")}, "uid", ""},
		{"anonymous filter", Request{Filter: identityEquality("anonymous_id", "anon")}, "anonymous_id", "anon"},
		{"explicit UID", Request{IdentityFilter: &IdentityFilter{UID: &uid}, Filter: identityEquality("anonymous_id", "anon")}, "uid", uid},
		{"explicit anonymous", Request{IdentityFilter: &IdentityFilter{AnonymousID: &anonymous}}, "anonymous_id", anonymous},
		{"nested AND", Request{Filter: &Filter{Op: "and", Args: []*Filter{identityEquality("event_key", "opened"), {Op: "and", Args: []*Filter{identityEquality("uid", "nested"), identityEquality("anonymous_id", "anon")}}}}}, "uid", "nested"},
		{"OR", Request{Filter: &Filter{Op: "or", Args: []*Filter{identityEquality("uid", "first"), identityEquality("uid", "second")}}}, "", ""},
		{"AND skips OR", Request{Filter: &Filter{Op: "and", Args: []*Filter{{Op: "or", Args: []*Filter{identityEquality("uid", "optional"), identityEquality("event_key", "opened")}}, identityEquality("anonymous_id", "required")}}}, "anonymous_id", "required"},
		{"property", Request{Filter: &Filter{Op: "eq", Field: &Field{Kind: "property", Key: "uid"}, Value: "user"}}, "", ""},
		{"non string", Request{Filter: &Filter{Op: "eq", Field: &Field{Kind: "system", Key: "uid"}, Value: 4}}, "", ""},
		{"inequality", Request{Filter: &Filter{Op: "neq", Field: &Field{Kind: "system", Key: "uid"}, Value: "user"}}, "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			key, value := necessaryIdentityEquality(tc.request)
			if key != tc.key || value != tc.value {
				t.Fatalf("got %q=%q, want %q=%q", key, value, tc.key, tc.value)
			}
		})
	}
}

func TestIdentityCandidateSourceKeepsWinnerPredicatesOutside(t *testing.T) {
	r := request(t, baseRequest)
	r.Kind = QueryKindEvents
	r.Filter = identityEquality("uid", "O'Hare; --")
	plan, err := Compile("evia", "event_facts", Scope{ProjectID: "project-one", Environment: r.Environment}, r)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(plan.SQL, "(source_id,event_id) IN (SELECT candidate_facts.source_id,candidate_facts.event_id FROM `evia`.`event_facts_raw` AS candidate_facts WHERE candidate_facts.project_id=") || strings.Contains(plan.SQL, "O'Hare") {
		t.Fatal("candidate lookup must use scoped bound source/event pairs", plan.SQL)
	}
	if strings.Count(plan.SQL, "uid=") != 2 || strings.Count(plan.SQL, "event_time>=") != 1 {
		t.Fatal("keep the complete outer winner filter and time range", plan.SQL)
	}
	reference, err := Compile("evia", "event_facts_reference", Scope{ProjectID: "project-one", Environment: r.Environment}, r)
	if err != nil || strings.Contains(reference.SQL, "event_facts_raw") {
		t.Fatal("custom view must retain its configured source", reference, err)
	}
}

func TestIdentityCandidateColumnsBelongToTheirOwnRawScan(t *testing.T) {
	for _, key := range []string{"uid", "anonymous_id"} {
		t.Run(key, func(t *testing.T) {
			p := planner{params: map[string]string{}}
			predicate := p.identityCandidatePredicate("evia", Scope{ProjectID: "project-one", Environment: "development"}, Request{Filter: identityEquality(key, "selected")})
			for _, column := range []string{"source_id", "event_id", "project_id", "environment", key} {
				if !strings.Contains(predicate, "candidate_facts."+column) {
					t.Fatal("candidate lookup could resolve a column against surrounding winner aliases", column, predicate)
				}
			}
		})
	}
}
