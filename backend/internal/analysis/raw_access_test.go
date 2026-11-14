package analysis

import "testing"

func TestRequiresRawAccess(t *testing.T) {
	identity := func(key string) *Field { return &Field{Kind: "system", Key: key} }
	cases := []struct {
		name    string
		request Request
		want    bool
	}{
		{"aggregate UID", Request{Kind: "trend", Subject: "uid", Metrics: []Metric{{Op: "unique"}}}, false},
		{"aggregate funnel", Request{Kind: "funnel", Subject: "uid"}, false},
		{"events", Request{Kind: "events"}, true},
		{"journey", Request{Kind: "journey"}, true},
		{"explicit identity on count", Request{Kind: "trend", IdentityFilter: &IdentityFilter{}}, true},
		{"UID dimension", Request{GroupBy: []Field{*identity("uid")}}, true},
		{"anonymous dimension", Request{GroupBy: []Field{*identity("anonymous_id")}}, true},
		{"session dimension", Request{GroupBy: []Field{*identity("session_id")}}, true},
		{"page dimension", Request{GroupBy: []Field{*identity("page_view_id")}}, true},
		{"operation dimension", Request{GroupBy: []Field{*identity("operation_id")}}, true},
		{"nested filter", Request{Filter: &Filter{Op: "or", Args: []*Filter{{Op: "and", Args: []*Filter{{Field: identity("uid")}}}}}}, true},
		{"metric", Request{Metrics: []Metric{{Op: "unique", Field: identity("uid")}}}, true},
		{"step filter", Request{Funnel: &Funnel{Steps: []Step{{Filter: &Filter{Field: identity("uid")}}}}}, true},
		{"entity field", Request{Funnel: &Funnel{SameEntity: []EntityField{{Field: *identity("uid")}}}}, true},
		{"property name is user schema", Request{GroupBy: []Field{{Kind: "property", Key: "uid"}}}, false},
		{"event dimension", Request{GroupBy: []Field{*identity("event_key")}}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := RequiresRawAccess(c.request); got != c.want {
				t.Fatalf("got %v want %v", got, c.want)
			}
		})
	}
}

// TestRequiresRawAccessInProcessEvaluators pins the authorization classification
// for the aggregate in-process evaluators. They consume a subject internally but
// only emit aggregate counts/shares (never individual uid/anonymous_id values),
// so a subject-only request must stay non-raw exactly like UV and funnel. Any
// walkable identity system field still upgrades the request to raw access.
func TestRequiresRawAccessInProcessEvaluators(t *testing.T) {
	identity := func(key string) *Field { return &Field{Kind: "system", Key: key} }
	aggregateSubjects := []struct {
		name    string
		request Request
	}{
		{"retention uid", Request{Kind: "retention", Subject: "uid"}},
		{"retention anonymous", Request{Kind: "retention", Subject: "anonymous"}},
		{"segment uid", Request{Kind: "segment", Subject: "uid"}},
		{"path uid", Request{Kind: "path", Subject: "uid"}},
		{"attribution uid", Request{Kind: "attribution", Subject: "uid"}},
	}
	for _, c := range aggregateSubjects {
		t.Run(c.name+" aggregate is non-raw", func(t *testing.T) {
			if RequiresRawAccess(c.request) {
				t.Fatalf("%s subject-only aggregate must not require raw access", c.name)
			}
		})
	}

	identityExposing := []struct {
		name    string
		request Request
	}{
		{
			"retention identity step filter",
			Request{
				Kind:    "retention",
				Subject: "uid",
				Retention: &Retention{
					StartEvent:  Step{Filter: &Filter{Field: identity("uid")}},
					ReturnEvent: Step{},
				},
			},
		},
		{
			"segment identity field",
			Request{Kind: "segment", Subject: "uid", Segment: &Segment{Field: *identity("anonymous_id")}},
		},
		{
			"path identity top-level filter",
			Request{Kind: "path", Subject: "uid", Filter: &Filter{Field: identity("session_id")}},
		},
		{
			"attribution identity top-level filter",
			Request{Kind: "attribution", Subject: "uid", Filter: &Filter{Field: identity("uid")}},
		},
	}
	for _, c := range identityExposing {
		t.Run(c.name+" is raw", func(t *testing.T) {
			if !RequiresRawAccess(c.request) {
				t.Fatalf("%s touches an identity field and must require raw access", c.name)
			}
		})
	}
}
