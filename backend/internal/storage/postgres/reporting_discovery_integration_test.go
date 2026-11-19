//go:build integration

package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/howtomakeaname/Evia/backend/internal/analysis"
	"github.com/howtomakeaname/Evia/backend/internal/platform/contract"
	"github.com/howtomakeaname/Evia/backend/internal/platform/fault"
	"github.com/howtomakeaname/Evia/backend/internal/reporting"
)

func discoveryService(t *testing.T, f catalogFixture) *reporting.Discovery {
	t.Helper()
	v, e := contract.New()
	if e != nil {
		t.Fatal(e)
	}
	return reporting.NewDiscovery(f.s, v, false)
}
func reportingRequest(t *testing.T, request analysis.Request) json.RawMessage {
	t.Helper()
	b, e := json.Marshal(request)
	if e != nil {
		t.Fatal(e)
	}
	return b
}
func TestReportingDiscoveryPublishedScopeStableOrderAndImmutableMetadata(t *testing.T) {
	f := catalogDB(t)
	ctx := context.Background()
	svc := discoveryService(t, f)
	draft := f.event(t, "page_opened")
	f.event(t, "draft_only")
	if _, e := f.svc.CreateRelease(ctx, f.owner, f.project, "first", releaseInput("page_opened", draft.Revision)); e != nil {
		t.Fatal(e)
	}
	changed := publishable()
	changed.DisplayName = catalogString("Changed immutable title")
	changed.Schema["properties"].(map[string]any)["enabled"] = map[string]any{"type": "boolean"}
	draft, e := f.svc.SaveDraft(ctx, f.owner, f.project, "page_opened", draft.Revision, changed)
	if e != nil {
		t.Fatal(e)
	}
	secondRelease, e := f.svc.CreateRelease(ctx, f.owner, f.project, "second", releaseInput("page_opened", draft.Revision))
	if e != nil {
		t.Fatal(e)
	}
	if _, e = f.svc.SaveDraft(ctx, f.owner, f.project, "page_opened", draft.Revision, publishable()); e != nil {
		t.Fatal(e)
	}
	page, e := svc.Catalog(ctx, f.viewer, f.project, "development", "", 0, 1)
	if e != nil {
		t.Fatal(e)
	}
	if page.Revision != 2 || len(page.Items) != 2 || page.Items[0].Event.SchemaVersion != 1 || page.Items[1].Event.SchemaVersion != 2 {
		t.Fatal("published order or lookahead changed", page)
	}
	first := page.Items[0].Event
	second := page.Items[1].Event
	if first.DisplayName != "Lesson opened" || second.DisplayName != "Changed immutable title" || first.DefinitionHash == second.DefinitionHash || len(first.Fields) != 1 || len(second.Fields) != 2 {
		t.Fatal("draft metadata contaminated immutable projection", first, second)
	}
	if !first.Published || first.Active || first.IngestionReady || !second.Published || second.Active || second.IngestionReady {
		t.Fatal("publication impersonated activation/readiness", first, second)
	}
	next, e := svc.Catalog(ctx, f.viewer, f.project, "development", first.EventKey, first.SchemaVersion, 1)
	if e != nil || len(next.Items) != 1 || next.Items[0].Event.SchemaVersion != 2 {
		t.Fatal(next, e)
	}
	for _, environment := range []string{"staging", "production"} {
		empty, e := svc.Catalog(ctx, f.viewer, f.project, environment, "", 0, 10)
		if e != nil || empty.Revision != 0 || len(empty.Items) != 0 {
			t.Fatal("cross-environment metadata exposed", empty, e)
		}
	}
	_, e = svc.Catalog(ctx, f.outsider, f.project, "development", "", 0, 10)
	faultCode(t, e, "PROJECT_NOT_FOUND")
	_, e = svc.Catalog(ctx, f.viewer, f.otherProject, "development", "", 0, 10)
	faultCode(t, e, "PROJECT_NOT_FOUND")
	if _, e = svc.Catalog(ctx, f.viewer, f.project, "invalid", "", 0, 10); e == nil {
		t.Fatal("unsupported environment accepted")
	}
	// The existing probe fixture provides synthetic adapter proof in this isolated
	// schema. This verifies catalog projection, not broker or execution capacity.
	deliverySvc := deliveryService(t, f, &policyProbe{})
	if _, e = deliverySvc.Activate(ctx, f.owner, f.project, "discovery-activation", activationInput(secondRelease, 0)); e != nil {
		t.Fatal(e)
	}
	active, e := svc.Catalog(ctx, f.viewer, f.project, "development", "", 0, 10)
	if e != nil || len(active.Items) != 2 || active.Items[0].Event.Active || !active.Items[1].Event.Active || !active.Items[1].Event.IngestionReady {
		t.Fatal("current release state not derived from activation and proof", active, e)
	}
	if _, e = f.s.Pool.Exec(ctx, `UPDATE catalog_release_readiness SET confirmed_at=clock_timestamp()-interval '2 minutes',ready_until=clock_timestamp()-interval '1 minute' WHERE release_id=$1`, secondRelease.ID); e != nil {
		t.Fatal(e)
	}
	active, e = svc.Catalog(ctx, f.viewer, f.project, "development", "", 0, 10)
	if e != nil || !active.Items[1].Event.Active || active.Items[1].Event.IngestionReady {
		t.Fatal("expired proof changed activation or retained readiness", active, e)
	}
}
func TestReportingDryValidationTypedScalarsDependenciesAndNoWrites(t *testing.T) {
	f := catalogDB(t)
	ctx := context.Background()
	svc := discoveryService(t, f)
	draft := f.event(t, "page_opened")
	definition := publishable()
	definition.Schema["properties"].(map[string]any)["enabled"] = map[string]any{"type": "boolean"}
	definition.Schema["properties"].(map[string]any)["tags"] = map[string]any{"type": "array", "items": map[string]any{"type": "string"}}
	draft, e := f.svc.SaveDraft(ctx, f.owner, f.project, "page_opened", draft.Revision, definition)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = f.svc.CreateRelease(ctx, f.owner, f.project, "published", releaseInput("page_opened", draft.Revision)); e != nil {
		t.Fatal(e)
	}
	request := queryRequest(t, "trend")
	request.EventVersions = []analysis.EventVersion{{EventKey: "page_opened", SchemaVersion: 1}}
	counts := func() []int {
		var out []int
		for _, table := range []string{"analysis_query_jobs", "audit_entries", "catalog_versions", "catalog_releases", "catalog_outbox", "catalog_environments"} {
			var count int
			if e := f.s.Pool.QueryRow(ctx, "SELECT count(*) FROM "+table).Scan(&count); e != nil {
				t.Fatal(e)
			}
			out = append(out, count)
		}
		return out
	}
	before := counts()
	for _, tc := range []struct {
		key   string
		value any
	}{{"enabled", false}, {"lesson_id", 0}} {
		request.Filter = &analysis.Filter{Op: "eq", Field: &analysis.Field{Kind: "property", EventKey: "page_opened", SchemaVersion: 1, Key: tc.key}, Value: tc.value}
		out, e := svc.ValidateQuery(ctx, f.viewer, f.project, reportingRequest(t, request))
		if e != nil {
			t.Fatal(e)
		}
		if !out.Valid || out.ExecutionPerformed || out.ExecutionAvailable || out.CatalogRevision != 1 || len(out.Dependencies.EventVersions) != 1 || out.Dependencies.SelectionMode != "event_versions" || len(out.Dependencies.UnfixedEventKeys) != 0 {
			t.Fatal(out)
		}
		if strings.Contains(string(out.NormalizedRequest), "resolved_type") {
			t.Fatal("server field types escaped into public request")
		}
		var normalized struct {
			Filter struct {
				Value any `json:"value"`
			} `json:"filter"`
		}
		if e = json.Unmarshal(out.NormalizedRequest, &normalized); e != nil {
			t.Fatal(e)
		}
		if tc.key == "enabled" && normalized.Filter.Value != false || tc.key == "lesson_id" && normalized.Filter.Value != float64(0) {
			t.Fatal("scalar changed", normalized)
		}
	}
	for _, tc := range []struct {
		name   string
		mutate func(*analysis.Request)
		code   string
	}{
		{"null", func(r *analysis.Request) { r.Filter.Value = nil }, "INVALID_QUERY"},
		{"wrong scalar", func(r *analysis.Request) { r.Filter.Value = false }, "INVALID_FILTER_TYPE"},
		{"invalid datetime scalar", func(r *analysis.Request) {
			r.Filter = &analysis.Filter{Op: "eq", Field: &analysis.Field{Kind: "system", Key: "event_time"}, Value: "not-a-date"}
		}, "INVALID_FILTER_TYPE"},
		{"invalid datetime in", func(r *analysis.Request) {
			r.Filter = &analysis.Filter{Op: "in", Field: &analysis.Field{Kind: "system", Key: "event_time"}, Value: []any{"2026-11-07T10:00:00Z", "not-a-date"}}
		}, "INVALID_FILTER_TYPE"},
		{"invalid datetime nested funnel step", func(r *analysis.Request) {
			r.Kind, r.Metrics, r.Subject, r.EventVersions, r.Filter = "funnel", nil, analysis.SubjectUID, nil, nil
			r.Funnel = &analysis.Funnel{Mode: "ordered_with_interleaving", WindowSeconds: 60, Steps: []analysis.Step{{EventKey: "page_opened", SchemaVersion: 1, Filter: &analysis.Filter{Op: "and", Args: []*analysis.Filter{{Op: "or", Args: []*analysis.Filter{{Op: "eq", Field: &analysis.Field{Kind: "system", Key: "event_time"}, Value: "not-a-date"}}}}}}, {EventKey: "page_opened", SchemaVersion: 1}}}
		}, "INVALID_FILTER_TYPE"},
		{"unpublished exact count", func(r *analysis.Request) { r.Filter = nil; r.EventVersions[0].SchemaVersion = 99 }, "QUERY_EVENT_VERSION_NOT_PUBLISHED"},
		{"wrong environment", func(r *analysis.Request) { r.Filter = nil; r.Environment = "production" }, "QUERY_EVENT_VERSION_NOT_PUBLISHED"},
		{"undefined field", func(r *analysis.Request) { r.Filter.Field.Key = "missing_field" }, "QUERY_PROPERTY_NOT_DEFINED"},
		{"field outside selection", func(r *analysis.Request) { r.Filter.Field.SchemaVersion = 2 }, "QUERY_FIELD_VERSION_NOT_SELECTED"},
		{"array grouping", func(r *analysis.Request) {
			r.GroupBy = []analysis.Field{{Kind: "property", EventKey: "page_opened", SchemaVersion: 1, Key: "tags"}}
		}, "UNSUPPORTED_GROUP_TYPE"},
		{"boolean numeric aggregation", func(r *analysis.Request) {
			r.Metrics = []analysis.Metric{{Op: analysis.OpSum, Field: &analysis.Field{Kind: "property", EventKey: "page_opened", SchemaVersion: 1, Key: "enabled"}}}
		}, "INVALID_METRIC_TYPE"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			copy := request
			copy.EventVersions = append([]analysis.EventVersion{}, request.EventVersions...)
			filter := *request.Filter
			field := *request.Filter.Field
			filter.Field = &field
			copy.Filter = &filter
			tc.mutate(&copy)
			_, e := svc.ValidateQuery(ctx, f.viewer, f.project, reportingRequest(t, copy))
			faultCode(t, e, tc.code)
			if tc.code == "UNSUPPORTED_GROUP_TYPE" || tc.code == "INVALID_METRIC_TYPE" || tc.code == "INVALID_FILTER_TYPE" || tc.code == "QUERY_PROPERTY_NOT_DEFINED" {
				var problem *fault.Error
				if !errors.As(e, &problem) || len(problem.Fields) == 0 {
					t.Fatal("typed validation error has no safe field paths", e)
				}
			}
		})
	}
	for _, value := range []string{"2026-11-07T10:00:00Z", "2026-11-07T18:00:00+08:00", "2026-11-07T18:00:00.123456789+08:00"} {
		copy := request
		copy.Filter = &analysis.Filter{Op: "eq", Field: &analysis.Field{Kind: "system", Key: "event_time"}, Value: value}
		out, e := svc.ValidateQuery(ctx, f.viewer, f.project, reportingRequest(t, copy))
		if e != nil || !out.Valid || out.ExecutionPerformed {
			t.Fatal("valid RFC3339 datetime dry validation rejected or executed", out, e)
		}
	}
	request.Filter = nil
	request.EventVersions = nil
	request.EventKeys = []string{"page_opened"}
	unfixed, e := svc.ValidateQuery(ctx, f.viewer, f.project, reportingRequest(t, request))
	if e != nil || unfixed.Dependencies.SelectionMode != "event_keys" || len(unfixed.Dependencies.EventVersions) != 0 || len(unfixed.Dependencies.UnfixedEventKeys) != 1 {
		t.Fatal("cross-version scope silently pinned", unfixed, e)
	}
	for i, after := range counts() {
		if after != before[i] {
			t.Fatalf("read-only validation changed table %d", i)
		}
	}
	_, e = svc.ValidateQuery(ctx, f.outsider, f.project, reportingRequest(t, request))
	faultCode(t, e, "PROJECT_NOT_FOUND")
	request.Kind = analysis.QueryKindEvents
	request.Metrics = nil
	_, e = svc.ValidateQuery(ctx, f.viewer, f.project, reportingRequest(t, request))
	faultCode(t, e, "PROJECT_FORBIDDEN")
	if _, e = svc.ValidateQuery(ctx, f.owner, f.project, reportingRequest(t, request)); e != nil {
		t.Fatal(e)
	}
	if _, e = f.s.Pool.Exec(ctx, `DELETE FROM project_members WHERE project_id=$1 AND user_id=$2`, f.project, f.viewer); e != nil {
		t.Fatal(e)
	}
	_, e = svc.Capabilities(ctx, f.viewer, f.project)
	faultCode(t, e, "PROJECT_NOT_FOUND")
	request.Kind = analysis.QueryKindTrend
	_, e = svc.ValidateQuery(ctx, f.viewer, f.project, reportingRequest(t, request))
	faultCode(t, e, "PROJECT_NOT_FOUND")
}
