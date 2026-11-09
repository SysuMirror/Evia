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
