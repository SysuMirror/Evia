package ingestion

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func systemEvent(eventKey string, schemaVersion int64, properties map[string]any) map[string]any {
	return map[string]any{
		"event_id":         testEvent,
		"event_key":        eventKey,
		"schema_version":   schemaVersion,
		"release_revision": int64(7),
		"occurred_at":      time.Now().UTC().Format(time.RFC3339Nano),
		"identity":         map[string]any{"anonymous_id": "44444444-4444-4444-8444-444444444444"},
		"context":          map[string]any{"tab_id": testSource, "sequence": 1, "page_view_id": "55555555-5555-4555-8555-555555555555"},
		"sdk":              map[string]any{"name": "evia-browser", "version": "1"},
		"properties":       properties,
	}
}

func scrollProperties() map[string]any {
	return map[string]any{
		"from_ratio": 0, "to_ratio": 0.6, "max_ratio": 0.6,
		"direction": "down", "duration_ms": 3000,
	}
}

func systemCollectorFixture(t *testing.T) (*Collector, *sourceStub, *planStub, *queueStub, *budgetStub) {
	t.Helper()
	c, source, plans, queue, budget := collectorFixture(t)
	catalog, err := LoadSystemCatalog()
	if err != nil {
		t.Fatal(err)
	}
	c.WithSystemCatalog(catalog)
	return c, source, plans, queue, budget
}

func TestSystemEventAcceptedWithoutProjectPlan(t *testing.T) {
	c, _, plans, queue, budget := systemCollectorFixture(t)
	out, status, err := c.Collect(context.Background(), batchOf(
		eventJSON(systemEvent("sys.page_view", 1, map[string]any{})),
		eventJSON(systemEvent("sys.page_scroll", 1, scrollProperties())),
	), browserAuth())
	if err != nil || status != 202 || len(queue.records) != 2 {
		t.Fatalf("system events must be accepted: status=%d err=%v out=%+v", status, err, out)
	}
	for i, result := range out.Results {
		if result.Status != "accepted" {
			t.Fatalf("result %d not accepted: %+v", i, result)
		}
	}
	// The embedded catalog replaces project plan resolution entirely.
	if plans.calls != 0 {
		t.Fatalf("project plan resolver must not be consulted, calls=%d", plans.calls)
	}
	first := queue.records[0]
	if first.EventKey != "sys.page_view" || first.SchemaVersion != 1 || first.ReleaseRevision != 7 {
		t.Fatalf("system record provenance lost: %+v", first)
	}
	if first.SourceKind != SourceKindBrowser || first.ProjectID != testProject || first.AnonymousID != "44444444-4444-4444-8444-444444444444" {
		t.Fatalf("system record scope lost: %+v", first)
	}
	if budget.calls != 1 || budget.count != 2 {
		t.Fatalf("batch budget policy changed: %+v", budget)
	}
}

func TestSystemEventRejections(t *testing.T) {
	cases := []struct {
		name      string
		event     map[string]any
		wantCode  string
		wantPath  string
		serverSrc bool
	}{
		{
			name:     "unknown reserved key",
			event:    systemEvent("sys.unknown_thing", 1, map[string]any{}),
			wantCode: "SCHEMA_NOT_PUBLISHED",
			wantPath: "schema_version",
		},
		{
			name:     "wrong schema version",
			event:    systemEvent("sys.page_view", 9, map[string]any{}),
			wantCode: "SCHEMA_NOT_PUBLISHED",
			wantPath: "schema_version",
		},
		{
			name: "property schema mismatch",
			event: func() map[string]any {
				bad := scrollProperties()
				bad["direction"] = "sideways"
				return systemEvent("sys.page_scroll", 1, bad)
			}(),
			wantCode: "PROPERTY_SCHEMA_MISMATCH",
			wantPath: "properties",
		},
		{
			name: "undeclared property rejected",
			event: func() map[string]any {
				withExtra := scrollProperties()
				withExtra["unexpected"] = 1
				return systemEvent("sys.page_scroll", 1, withExtra)
			}(),
			wantCode: "PROPERTY_SCHEMA_MISMATCH",
			wantPath: "properties",
		},
		{
			name:      "server source forbidden for browser-only rule",
			event:     systemEvent("sys.page_view", 1, map[string]any{}),
			wantCode:  "SOURCE_KIND_FORBIDDEN",
			serverSrc: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c, source, plans, queue, _ := systemCollectorFixture(t)
			auth := browserAuth()
			if tc.serverSrc {
				secret := strings.Repeat("s", 43)
				source.value.Source.Kind = SourceKindServer
				source.value.Credential.SecretDigest = Digest(secret)
				auth = Authentication{Bearer: secret}
			}
			out, status, err := c.Collect(context.Background(), batchOf(eventJSON(tc.event)), auth)
			if err != nil || status != 422 || len(queue.records) != 0 {
				t.Fatalf("rejected system event must return 422 with no publish: status=%d err=%v", status, err)
			}
			got := out.Results[0].Error
			if got == nil || got.Code != tc.wantCode || got.Retryable {
				t.Fatalf("want non-retryable %s, got %+v", tc.wantCode, got)
			}
			if tc.wantPath != "" && (len(got.FieldPaths) != 1 || got.FieldPaths[0] != tc.wantPath) {
				t.Fatalf("field paths = %v, want [%s]", got.FieldPaths, tc.wantPath)
			}
			if plans.calls != 0 {
				t.Fatalf("rejected system event must not consult project plans, calls=%d", plans.calls)
			}
		})
	}
}

func TestSystemEventStillPassesEnvelopeGate(t *testing.T) {
	// The reserved branch sits behind the shared envelope validator: a context
	// with an undeclared field must be rejected as INVALID_EVENT before the
	// system catalog is consulted, so reserved events cannot bypass envelope rules.
	c, _, plans, queue, _ := systemCollectorFixture(t)
	event := systemEvent("sys.page_view", 1, map[string]any{})
	event["context"] = map[string]any{"tab_id": testSource, "unexpected_context_field": 1}
	out, status, err := c.Collect(context.Background(), batchOf(eventJSON(event)), browserAuth())
	if err != nil || status != 422 || len(queue.records) != 0 {
		t.Fatalf("envelope violation must reject: status=%d err=%v", status, err)
	}
	got := out.Results[0].Error
	if got == nil || got.Code != "INVALID_EVENT" {
		t.Fatalf("want INVALID_EVENT, got %+v", got)
	}
	if plans.calls != 0 {
		t.Fatalf("rejected envelope must not reach plan resolution, calls=%d", plans.calls)
	}
}

func TestMixedBatchKeepsProjectAndSystemPathsSeparate(t *testing.T) {
	c, _, plans, queue, _ := systemCollectorFixture(t)
	out, status, err := c.Collect(context.Background(), batchOf(
		eventJSON(validEvent()),
		eventJSON(systemEvent("sys.page_view", 1, map[string]any{})),
	), browserAuth())
	if err != nil || status != 202 || len(queue.records) != 2 {
		t.Fatalf("mixed batch must accept both: status=%d err=%v out=%+v", status, err, out)
	}
	if plans.calls != 1 {
		t.Fatalf("exactly one project plan lookup expected, calls=%d", plans.calls)
	}
	if queue.records[0].EventKey != "lesson_opened" || queue.records[1].EventKey != "sys.page_view" {
		t.Fatalf("record order/keys changed: %s, %s", queue.records[0].EventKey, queue.records[1].EventKey)
	}
}

func TestSystemEventsBypassReleaseLeaseRecheck(t *testing.T) {
	c, _, plans, queue, _ := systemCollectorFixture(t)
	// A broken project plan cannot affect system events: they carry no lease.
	plans.err = errors.New("plan store unavailable")
	out, status, err := c.Collect(context.Background(), batchOf(
		eventJSON(systemEvent("sys.identity_link", 1, map[string]any{})),
	), browserAuth())
	if err != nil || status != 202 || len(queue.records) != 1 {
		t.Fatalf("system event must not depend on plan availability: status=%d err=%v out=%+v", status, err, out)
	}
	if plans.calls != 0 {
		t.Fatalf("plan resolver must never be reached, calls=%d", plans.calls)
	}
}

func TestSystemBranchIsOptIn(t *testing.T) {
	// Without WithSystemCatalog the collector keeps the project-plan-only
	// behaviour: a sys.* key cannot exist in any project plan and is rejected
	// through the normal lookup rather than the reserved branch.
	c, _, plans, queue, _ := collectorFixture(t)
	out, status, err := c.Collect(context.Background(), batchOf(
		eventJSON(systemEvent("sys.page_view", 1, map[string]any{})),
	), browserAuth())
	if err != nil || status != 422 || len(queue.records) != 0 {
		t.Fatalf("default collector must not accept sys.*: status=%d err=%v", status, err)
	}
	got := out.Results[0].Error
	if got == nil || got.Code != "SCHEMA_NOT_PUBLISHED" {
		t.Fatalf("want project-plan rejection SCHEMA_NOT_PUBLISHED, got %+v", got)
	}
	if plans.calls != 1 {
		t.Fatalf("default path must still consult the project plan, calls=%d", plans.calls)
	}
}
