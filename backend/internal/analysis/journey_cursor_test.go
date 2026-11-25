package analysis

import (
	"strings"
	"testing"
	"time"

	"github.com/howtomakeaname/Evia/backend/internal/platform/fault"
)

func sampleJourneyRequest() Request {
	uid := "user-1"
	return Request{
		Kind:           QueryKindJourney,
		Environment:    "development",
		Subject:        SubjectUID,
		IdentityFilter: &IdentityFilter{UID: &uid},
		TimeRange: TimeRange{
			From: time.Date(2026, 11, 7, 10, 0, 0, 0, time.UTC),
			To:   time.Date(2026, 11, 7, 11, 0, 0, 0, time.UTC),
		},
		Timezone: "UTC",
		Limit:    2,
	}
}

func TestJourneyCursorRoundTripAndScopeBinding(t *testing.T) {
	scope := Scope{ProjectID: "project-one", Environment: "development"}
	req := sampleJourneyRequest()
	pos := JourneyCursorPosition{
		EventTimeNs: time.Date(2026, 11, 7, 10, 15, 0, 0, time.UTC).UnixNano(),
		SessionID:   "session-a",
		TabID:       "tab-1",
		Sequence:    42,
		SourceID:    "source-one",
		EventID:     "evt-42",
	}
	token, err := EncodeJourneyCursor(scope, req, pos)
	if err != nil {
		t.Fatal(err)
	}

	next := req
	next.Cursor = token
	next.Limit = 5
	next.Timezone = "Asia/Shanghai"
	if err := Validate(&next); err != nil {
		t.Fatalf("valid continuation rejected: %v", err)
	}
	plan, err := Compile("evia_test", "event_facts", scope, next)
	if err != nil {
		t.Fatalf("compile failed: %v", err)
	}
	if !strings.Contains(plan.SQL, "toUnixTimestamp64Nano(event_time)>=") || !strings.Contains(plan.SQL, "_cursor_tab_id") {
		t.Fatalf("expected keyset predicate and cursor helper columns in SQL: %s", plan.SQL)
	}

	// Cursor encoded after resolveQueryFields populates ResolvedType must match raw incoming request.
	resolvedReq := req
	resolvedReq.Filter = &Filter{
		Op: "and",
		Args: []*Filter{{
			Op:    "eq",
			Field: &Field{Kind: "property", Key: "plan", EventKey: "signup", SchemaVersion: 1, ResolvedType: "string"},
			Value: "pro",
		}},
	}
	resolvedToken, err := EncodeJourneyCursor(scope, resolvedReq, pos)
	if err != nil {
		t.Fatal(err)
	}
	rawFollowUp := req
	rawFollowUp.Filter = &Filter{
		Op: "and",
		Args: []*Filter{{
			Op:    "eq",
			Field: &Field{Kind: "property", Key: "plan", EventKey: "signup", SchemaVersion: 1},
			Value: "pro",
		}},
	}
	rawFollowUp.Cursor = resolvedToken
	if _, err := ValidateJourneyCursorScope(scope, rawFollowUp); err != nil {
		t.Fatalf("unresolved incoming filter rejected cursor encoded after field resolution: %v", err)
	}

	// Scope or condition drift must fail closed.
	otherUID := "user-2"
	driftCases := []struct {
		name  string
		scope Scope
		mut   func(*Request)
	}{
		{"other project", Scope{ProjectID: "project-two", Environment: "development"}, func(*Request) {}},
		{"other environment", Scope{ProjectID: "project-one", Environment: "production"}, func(r *Request) { r.Environment = "production" }},
		{"other uid", scope, func(r *Request) { r.IdentityFilter = &IdentityFilter{UID: &otherUID} }},
		{"other time range", scope, func(r *Request) { r.TimeRange.To = r.TimeRange.To.Add(time.Minute) }},
		{"added event key", scope, func(r *Request) { r.EventKeys = []string{"page_view"} }},
		{"added filter", scope, func(r *Request) {
			r.Filter = &Filter{Op: "eq", Field: &Field{Kind: "system", Key: "event_key", ResolvedType: "string"}, Value: "page_view"}
		}},
	}
	for _, tc := range driftCases {
		t.Run(tc.name, func(t *testing.T) {
			mutated := next
			tc.mut(&mutated)
			_, err := ValidateJourneyCursorScope(tc.scope, mutated)
			if err == nil {
				t.Fatalf("expected mismatch error for %s", tc.name)
			}
			var fe *fault.Error
			if !errorAsFault(err, &fe) || fe.Code != "INVALID_QUERY_CURSOR" {
				t.Fatalf("expected INVALID_QUERY_CURSOR, got %v", err)
			}
		})
	}
}

func TestJourneyCursorMalformedAndNonJourneyRejected(t *testing.T) {
	for _, bad := range []string{"garbage", "jt1.not-base64!", "jt1.e30", "jt1.eyJ2IjoyLCJmcCI6IjAxMjM0NTY3ODlhYmNkZWYwMTIzNDU2Nzg5YWJjZGVmIiwidCI6MSwiciI6InMiLCJlIjoiZSJ9"} {
		if _, err := ParseJourneyCursor(bad); err == nil {
			t.Fatalf("malformed cursor accepted: %q", bad)
		}
	}

	// Out-of-range cursor timestamp fails Validate.
	scope := Scope{ProjectID: "project-one", Environment: "development"}
	req := sampleJourneyRequest()
	outOfRangePos := JourneyCursorPosition{
		EventTimeNs: req.TimeRange.To.UnixNano(),
		SourceID:    "source-one",
		EventID:     "evt-end",
	}
	token, err := EncodeJourneyCursor(scope, req, outOfRangePos)
	if err != nil {
		t.Fatal(err)
	}
	req.Cursor = token
	if err := Validate(&req); err == nil {
		t.Fatal("expected cursor at/after time_range.to to fail validation")
	}

	// Non-journey kinds must still reject cursor with UNSUPPORTED_QUERY_CURSOR.
	eventsReq := Request{
		Kind:        QueryKindEvents,
		Environment: "development",
		TimeRange:   req.TimeRange,
		Timezone:    "UTC",
		Limit:       10,
		Cursor:      token,
	}
	err = Validate(&eventsReq)
	var fe *fault.Error
	if !errorAsFault(err, &fe) || fe.Code != "UNSUPPORTED_QUERY_CURSOR" {
		t.Fatalf("expected UNSUPPORTED_QUERY_CURSOR for events, got %v", err)
	}
}

func errorAsFault(err error, target **fault.Error) bool {
	if err == nil {
		return false
	}
	fe, ok := err.(*fault.Error)
	if ok {
		*target = fe
	}
	return ok
}
