//go:build integration

package analysis

import (
	"context"
	"testing"
)

// expansionFixture seeds one identity graph reality directly into the request:
// person P owns uids u1,u2 and devices d1,d2; person Q owns u3,d3. The
// warehouse cannot resolve these — only the expansion mapping can.
func expansionFixture() *IdentityExpansion {
	return &IdentityExpansion{
		Revision: 7,
		Mapping: []PersonMapping{
			{PersonID: "aaaaaaaa-0000-4000-8000-00000000000p", UIDs: []string{"u1", "u2"}, AnonymousIDs: []string{"11111111-1111-4111-8111-111111111111", "22222222-2222-4222-8222-222222222222"}},
			{PersonID: "bbbbbbbb-0000-4000-8000-00000000000q", UIDs: []string{"u3"}, AnonymousIDs: []string{"33333333-3333-4333-8333-333333333333"}},
		},
	}
}

func seedIdentityRows(t *testing.T, client *ClickHouse) {
	seedRows(t, client, []map[string]any{
		fact("e1", "sys.page_view", "u1", "11111111-1111-4111-8111-111111111111", "s1", 1, nil),
		fact("e2", "sys.page_view", "u2", "22222222-2222-4222-8222-222222222222", "s1", 2, nil),
		fact("e3", "sys.page_view", "u3", "33333333-3333-4333-8333-333333333333", "s1", 3, nil),
		fact("e4", "lesson_done", "u1", "11111111-1111-4111-8111-111111111111", "s1", 4, nil),
		fact("e5", "lesson_done", "u2", "22222222-2222-4222-8222-222222222222", "s1", 5, nil),
		fact("e6", "lesson_done", "u3", "33333333-3333-4333-8333-333333333333", "s1", 6, nil),
	})
}

func TestClickHouseResolvedPersonCountsIdentityOnce(t *testing.T) {
	client := clickHouseDatabase(t)
	seedIdentityRows(t, client)

	resolved := aggregateRequest(t)
	resolved.Subject = SubjectResolvedPerson
	resolved.Metrics = []Metric{{Op: OpUnique}}
	resolved.IdentityExpansion = expansionFixture()
	result := execute(t, client, resolved)
	if got := metricNumber(t, result, "metric_0"); got != 2 {
		t.Fatalf("resolved UV must count two persons across four raw identities, got %v", got)
	}
	if result.Metadata.IdentityRevision == nil || *result.Metadata.IdentityRevision != 7 {
		t.Fatalf("metadata must report the pinned revision: %+v", result.Metadata)
	}
	for _, gap := range result.Metadata.Coverage.KnownGaps {
		if gap == "identity_resolution_not_applied" {
			t.Fatal("resolved query must not advertise identity resolution as a gap")
		}
	}

	// The raw-identity equivalent double counts the same reality.
	raw := aggregateRequest(t)
	raw.Metrics = []Metric{{Op: OpUnique}}
	if got := metricNumber(t, execute(t, client, raw), "metric_0"); got != 3 {
		t.Fatalf("uid UV baseline wrong: %v", got)
	}

	// Hand-expanded equivalent SQL must match the mapped result exactly.
	equivalent := chExec(t, client, "SELECT uniqExactIf(multiIf(uid IN ('u1','u2'),'p',anonymous_id IN ('11111111-1111-4111-8111-111111111111','22222222-2222-4222-8222-222222222222'),'p',uid='u3','q',anonymous_id='33333333-3333-4333-8333-333333333333','q',''), event_key='sys.page_view') AS uv FROM event_facts WHERE project_id='project-one' AND environment='development' FORMAT TabSeparated")
	if string(equivalent) != "2\n" {
		t.Fatalf("hand-expanded equivalent mismatch: %q", equivalent)
	}
}

func TestClickHouseResolvedPersonFunnelAndEvents(t *testing.T) {
	client := clickHouseDatabase(t)
	seedIdentityRows(t, client)

	// Funnel: person P converts through page_view -> lesson_done on two
	// devices; grouping by resolved id yields exactly one converted subject.
	funnel := aggregateRequest(t)
	funnel.Kind = QueryKindFunnel
	funnel.Subject = SubjectResolvedPerson
	funnel.Metrics = nil
	funnel.EventKeys = nil
	funnel.Funnel = &Funnel{Steps: []Step{{EventKey: "sys.page_view", SchemaVersion: 1}, {EventKey: "lesson_done", SchemaVersion: 1}}}
	funnel.IdentityExpansion = expansionFixture()
	result := execute(t, client, funnel)
	if len(result.Rows) != 2 {
		t.Fatalf("funnel steps wrong: %+v", result.Rows)
	}

	// Pinned events listing returns rows across every member identity.
	person := "aaaaaaaa-0000-4000-8000-00000000000p"
	events := aggregateRequest(t)
	events.Kind = QueryKindEvents
	events.Subject = SubjectResolvedPerson
	events.Metrics = nil
	events.IdentityFilter = &IdentityFilter{ResolvedPersonID: &person}
	events.IdentityExpansion = &IdentityExpansion{
		Revision:           7,
		PersonID:           person,
		PinnedUIDs:         []string{"u1", "u2"},
		PinnedAnonymousIDs: []string{"11111111-1111-4111-8111-111111111111", "22222222-2222-4222-8222-222222222222"},
		Mapping:            expansionFixture().Mapping,
	}
	rows := execute(t, client, events)
	// The request filters event_key to sys.page_view, so the pinned person
	// lists one row per device identity, not its lesson_done events.
	if len(rows.Rows) != 2 {
		t.Fatalf("pinned person must list both device identities' page views, got %d", len(rows.Rows))
	}
	seen := map[string]bool{}
	for _, row := range rows.Rows {
		if uid, ok := row["uid"].(string); ok {
			seen[uid] = true
		}
	}
	if !seen["u1"] || !seen["u2"] {
		t.Fatalf("pinned listing must span both member uids: %v", seen)
	}

	// Planner fails closed when the expansion attachment is missing.
	missing := aggregateRequest(t)
	missing.Subject = SubjectResolvedPerson
	missing.Metrics = []Metric{{Op: OpUnique}}
	if _, e := client.Execute(context.Background(), Scope{ProjectID: "project-one", Environment: "development"}, missing); e == nil {
		t.Fatal("resolved query without plan-time expansion must fail closed")
	}
}
