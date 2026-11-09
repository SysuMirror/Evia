//go:build integration

package postgres

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/howtomakeaname/Evia/backend/internal/analysis"
	"github.com/howtomakeaname/Evia/backend/internal/reporting"
)

func metricDefinition() reporting.MetricDefinition {
	return reporting.MetricDefinition{Title: "Lesson count", EventVersions: []analysis.EventVersion{{EventKey: "lesson_opened", SchemaVersion: 1}}, Subject: "uid", Aggregation: analysis.Metric{Op: "count"}, Unit: "event"}
}
func metricFixture(t *testing.T) (catalogFixture, reporting.MetricDraft) {
	t.Helper()
	f := catalogDB(t)
	ctx := context.Background()
	draft := f.event(t, "lesson_opened")
	catalogDefinition := publishable()
	catalogDefinition.Schema["properties"].(map[string]any)["enabled"] = map[string]any{"type": "boolean"}
	catalogDefinition.Schema["properties"].(map[string]any)["tags"] = map[string]any{"type": "array", "items": map[string]any{"type": "string"}}
	draft, e := f.svc.SaveDraft(ctx, f.owner, f.project, "lesson_opened", draft.Revision, catalogDefinition)
	if e != nil {
		t.Fatal(e)
	}
	if _, e := f.svc.CreateRelease(ctx, f.owner, f.project, "metric-events", releaseInput("lesson_opened", draft.Revision)); e != nil {
		t.Fatal(e)
	}
	out, e := f.s.CreateMetric(ctx, f.owner, f.project, "metric-create", reporting.MetricCreate{Environment: "development", Definition: metricDefinition()})
	if e != nil {
		t.Fatal(e)
	}
	return f, out
}
func metricValidate(t *testing.T, f catalogFixture, d reporting.MetricDraft) reporting.MetricValidation {
	t.Helper()
	snapshot, e := f.s.MetricValidationSnapshot(context.Background(), f.owner, f.project, d.ID, reporting.MetricValidate{DraftRevision: d.DraftRevision, ExpectedPublishedVersion: d.PublishedVersion})
	if e != nil {
		t.Fatal(e)
	}
	out, e := reporting.BuildMetricValidation(snapshot)
	if e != nil {
		t.Fatal(e)
	}
	return out
}
func metricPublish(d reporting.MetricDraft, v reporting.MetricValidation) reporting.MetricPublish {
	return reporting.MetricPublish{DraftRevision: d.DraftRevision, ExpectedPublishedVersion: d.PublishedVersion, ValidationHash: v.ValidationHash}
}
func TestMetricPersistenceRevisionSnapshotAndOriginalReplay(t *testing.T) {
	f, d := metricFixture(t)
	ctx := context.Background()
	initial := d
	before := metricCounts(t, f)
	validation := metricValidate(t, f, d)
	again := metricValidate(t, f, d)
	if !reflect.DeepEqual(validation, again) || !reflect.DeepEqual(before, metricCounts(t, f)) {
		t.Fatal("validation was not stable and read-only")
	}
	if !validation.Valid || validation.ExecutionPerformed || len(validation.Dependencies.EventVersions) != 1 {
		t.Fatal(validation)
	}
	// Publishing another catalog release containing the same immutable version
	// cannot invalidate a semantic validation acknowledgement.
	catalogDraft, e := f.s.GetDraft(ctx, f.owner, f.project, "lesson_opened")
	if e != nil {
		t.Fatal(e)
	}
	if _, e = f.svc.CreateRelease(ctx, f.owner, f.project, "same-version-witness", releaseInput("lesson_opened", catalogDraft.Revision)); e != nil {
		t.Fatal(e)
	}
	in := metricPublish(d, validation)
	version, e := f.s.PublishMetric(ctx, f.owner, f.project, d.ID, "publish-one", in)
	if e != nil {
		t.Fatal(e)
	}
	if version.Version != 1 || version.DraftRevision != 1 || version.MetricID != d.ID || version.DefinitionHash != validation.DefinitionHash {
		t.Fatal(version)
	}
	updated := metricDefinition()
	updated.Title = "Changed current draft"
	d, e = f.s.SaveMetricDraft(ctx, f.owner, f.project, d.ID, 1, updated)
	if e != nil {
		t.Fatal(e)
	}
	if d.DraftRevision != 2 || d.PublishedVersion != 1 || d.Title != updated.Title {
		t.Fatal(d)
	}
	replay, e := f.s.CreateMetric(ctx, f.owner, f.project, "metric-create", reporting.MetricCreate{Environment: "development", Definition: metricDefinition()})
	if e != nil || !savedWireEqual(initial, replay) {
		t.Fatal("create did not replay original response", replay, e)
	}
	replayVersion, e := f.s.PublishMetric(ctx, f.owner, f.project, d.ID, "publish-one", in)
	if e != nil || !savedWireEqual(version, replayVersion) {
		t.Fatal("publish did not replay original response", replayVersion, e)
	}
	_, e = f.s.PublishMetric(ctx, f.owner, f.project, d.ID, "publish-two", in)
	faultCode(t, e, "METRIC_REVISION_CONFLICT")
	conflicting := in
	conflicting.ValidationHash = strings.Repeat("0", 64)
	_, e = f.s.PublishMetric(ctx, f.owner, f.project, d.ID, "publish-one", conflicting)
	faultCode(t, e, "IDEMPOTENCY_CONFLICT")
	_, e = f.s.CreateMetric(ctx, f.owner, f.project, "metric-create", reporting.MetricCreate{Environment: "development", Definition: updated})
	faultCode(t, e, "IDEMPOTENCY_CONFLICT")
	validation = metricValidate(t, f, d)
	wrong := metricPublish(d, validation)
	wrong.ValidationHash = strings.Repeat("0", 64)
	_, e = f.s.PublishMetric(ctx, f.owner, f.project, d.ID, "bad-hash", wrong)
	faultCode(t, e, "VALIDATION_HASH_CONFLICT")
	v2, e := f.s.PublishMetric(ctx, f.owner, f.project, d.ID, "publish-two", metricPublish(d, validation))
	if e != nil || v2.Version != 2 {
		t.Fatal(v2, e)
	}
	old, e := f.s.GetMetricVersion(ctx, f.viewer, f.project, d.ID, 1)
	if e != nil || old.Definition.Title != "Lesson count" {
		t.Fatal(old, e)
	}
	list, e := f.s.ListMetricVersions(ctx, f.viewer, f.project, d.ID, 1, 1)
	if e != nil || len(list) != 1 || list[0].Version != 2 {
		t.Fatal(list, e)
	}
	if _, e = f.s.ListMetricVersions(ctx, f.viewer, f.project, d.ID, 0, 52); e == nil {
		t.Fatal("unbounded versions")
	}
	var witness bool
	if e = f.s.Pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM metric_dependencies d JOIN catalog_release_events re USING(project_id,environment,event_key,schema_version,release_id) WHERE d.metric_id=$1 AND d.release_id=(SELECT r.release_id FROM catalog_release_events r WHERE r.project_id=d.project_id AND r.environment=d.environment AND r.event_key=d.event_key AND r.schema_version=d.schema_version ORDER BY r.release_id LIMIT 1))`, d.ID).Scan(&witness); e != nil || !witness {
		t.Fatal("no scoped publication witness", e)
	}
	for next := int64(3); next <= 11; next++ {
		current, e := f.s.GetMetricDraft(ctx, f.owner, f.project, d.ID)
		if e != nil {
			t.Fatal(e)
		}
		check := metricValidate(t, f, current)
		v, e := f.s.PublishMetric(ctx, f.owner, f.project, d.ID, "numeric-"+string(rune('a'+next)), metricPublish(current, check))
		if e != nil || v.Version != next {
			t.Fatal(v, e)
		}
	}
	numeric, e := f.s.ListMetricVersions(ctx, f.viewer, f.project, d.ID, 9, 2)
	if e != nil || len(numeric) != 2 || numeric[0].Version != 10 || numeric[1].Version != 11 {
		t.Fatal("versions not numeric", numeric, e)
	}
	for _, sql := range []string{`UPDATE metric_versions SET definition=definition WHERE metric_id=$1`, `DELETE FROM metric_versions WHERE metric_id=$1`, `UPDATE metric_dependencies SET definition_hash=definition_hash WHERE metric_id=$1`, `DELETE FROM metric_dependencies WHERE metric_id=$1`, `UPDATE metric_idempotency SET response=response WHERE response->>'metric_id'=$1`, `DELETE FROM metric_idempotency WHERE response->>'metric_id'=$1`, `UPDATE metrics SET environment='production' WHERE metric_id=$1`, `UPDATE metrics SET published_version=99 WHERE metric_id=$1`} {
		if _, e = f.s.Pool.Exec(ctx, sql, d.ID); e == nil {
			t.Fatal("immutable/scoped relation accepted mutation")
		}
	}
}
func metricCounts(t *testing.T, f catalogFixture) []int64 {
	t.Helper()
	out := []int64{}
	for _, table := range []string{"metrics", "metric_versions", "metric_dependencies", "metric_idempotency", "analysis_query_jobs", "audit_entries", "catalog_versions", "catalog_releases", "catalog_environments"} {
		var n int64
		if e := f.s.Pool.QueryRow(context.Background(), "SELECT count(*) FROM "+table).Scan(&n); e != nil {
			t.Fatal(e)
		}
		out = append(out, n)
	}
	return out
}
func TestMetricConcurrentRevisionPublicationAndReplay(t *testing.T) {
	f, d := metricFixture(t)
	ctx := context.Background()
	var wg sync.WaitGroup
	var successes atomic.Int32
	for range 8 {
		wg.Go(func() {
			v, e := f.s.CreateMetric(ctx, f.owner, f.project, "metric-create", reporting.MetricCreate{Environment: "development", Definition: metricDefinition()})
			if e != nil || v.ID != d.ID {
				t.Error("concurrent create replay changed identity", v.ID, e)
			}
		})
	}
	wg.Wait()
	for range 8 {
		wg.Go(func() {
			_, e := f.s.SaveMetricDraft(ctx, f.owner, f.project, d.ID, 1, metricDefinition())
			if e == nil {
				successes.Add(1)
			} else {
				faultCode(t, e, "METRIC_REVISION_CONFLICT")
			}
		})
	}
	wg.Wait()
	if successes.Load() != 1 {
		t.Fatal("concurrent revision lost", successes.Load())
	}
	d, e := f.s.GetMetricDraft(ctx, f.viewer, f.project, d.ID)
	if e != nil {
		t.Fatal(e)
	}
	validation := metricValidate(t, f, d)
	in := metricPublish(d, validation)
	for range 8 {
		wg.Go(func() {
			v, e := f.s.PublishMetric(ctx, f.owner, f.project, d.ID, "parallel-publish", in)
			if e != nil || v.Version != 1 {
				t.Error(v, e)
			}
		})
	}
	wg.Wait()
	versions, e := f.s.ListMetricVersions(ctx, f.viewer, f.project, d.ID, 0, 51)
	if e != nil || len(versions) != 1 {
		t.Fatal(versions, e)
	}
	d, e = f.s.GetMetricDraft(ctx, f.viewer, f.project, d.ID)
	if e != nil {
		t.Fatal(e)
	}
	validation = metricValidate(t, f, d)
	in = metricPublish(d, validation)
	successes.Store(0)
	for i := range 8 {
		key := string(rune('a' + i))
		wg.Go(func() {
			_, e := f.s.PublishMetric(ctx, f.owner, f.project, d.ID, key, in)
			if e == nil {
				successes.Add(1)
			} else {
				faultCode(t, e, "PUBLISHED_VERSION_CONFLICT")
			}
		})
	}
	wg.Wait()
	if successes.Load() != 1 {
		t.Fatal("concurrent publication lost", successes.Load())
	}
}
func TestMetricAuthorizationTypesAndAtomicRollback(t *testing.T) {
	f, d := metricFixture(t)
	ctx := context.Background()
	_, e := f.s.GetMetric(ctx, f.outsider, f.project, d.ID)
	faultCode(t, e, "PROJECT_NOT_FOUND")
	_, e = f.s.GetMetric(ctx, f.owner, f.otherProject, d.ID)
	faultCode(t, e, "METRIC_NOT_FOUND")
	_, e = f.s.GetMetricVersion(ctx, f.owner, f.otherProject, d.ID, 1)
	faultCode(t, e, "METRIC_VERSION_NOT_FOUND")
	_, e = f.s.CreateMetric(ctx, f.viewer, f.project, "denied", reporting.MetricCreate{Environment: "development", Definition: metricDefinition()})
	faultCode(t, e, "PROJECT_FORBIDDEN")
	_, e = f.s.SaveMetricDraft(ctx, f.viewer, f.project, d.ID, 1, metricDefinition())
	faultCode(t, e, "PROJECT_FORBIDDEN")
	validation := metricValidate(t, f, d)
	_, e = f.s.MetricValidationSnapshot(ctx, f.viewer, f.project, d.ID, reporting.MetricValidate{DraftRevision: 1})
	faultCode(t, e, "PROJECT_FORBIDDEN")
	_, e = f.s.PublishMetric(ctx, f.viewer, f.project, d.ID, "denied", metricPublish(d, validation))
	faultCode(t, e, "PROJECT_FORBIDDEN")
	staging, e := f.s.CreateMetric(ctx, f.owner, f.project, "staging", reporting.MetricCreate{Environment: "staging", Definition: metricDefinition()})
	if e != nil {
		t.Fatal(e)
	}
	page, e := f.s.ListMetrics(ctx, f.viewer, f.project, "staging", "", 101)
	if e != nil || len(page) != 1 || page[0].ID != staging.ID {
		t.Fatal("environment summary scope", page, e)
	}
	page, e = f.s.ListMetrics(ctx, f.viewer, f.project, "development", "", 101)
	if e != nil || len(page) != 1 || page[0].ID != d.ID {
		t.Fatal("environment summary scope", page, e)
	}
	_, e = f.s.MetricValidationSnapshot(ctx, f.owner, f.project, staging.ID, reporting.MetricValidate{DraftRevision: 1})
	faultCode(t, e, "QUERY_EVENT_VERSION_NOT_PUBLISHED")
	definition := metricDefinition()
	definition.Filter = &analysis.Filter{Op: "eq", Field: &analysis.Field{Kind: "property", EventKey: "lesson_opened", SchemaVersion: 1, Key: "lesson_id"}, Value: 0}
	d, e = f.s.SaveMetricDraft(ctx, f.owner, f.project, d.ID, 1, definition)
	if e != nil {
		t.Fatal(e)
	}
	validation = metricValidate(t, f, d)
	b, e := json.Marshal(validation.NormalizedDefinition)
	if e != nil || !strings.Contains(string(b), `"value":0`) || strings.Contains(string(b), "resolved_type") {
		t.Fatal("scalar or internal type changed", string(b), e)
	}
	for _, tc := range []struct {
		key   string
		value any
	}{{"enabled", false}, {"tags", []any{"first", "second"}}} {
		typed := metricDefinition()
		typed.Filter = &analysis.Filter{Op: "eq", Field: &analysis.Field{Kind: "property", EventKey: "lesson_opened", SchemaVersion: 1, Key: tc.key}, Value: tc.value}
		d, e = f.s.SaveMetricDraft(ctx, f.owner, f.project, d.ID, d.DraftRevision, typed)
		if e != nil {
			t.Fatal(e)
		}
		result := metricValidate(t, f, d)
		if !savedWireEqual(result.NormalizedDefinition.Filter.Value, tc.value) {
			t.Fatal("typed value changed")
		}
	}
	definition.Filter.Value = false
	d, e = f.s.SaveMetricDraft(ctx, f.owner, f.project, d.ID, d.DraftRevision, definition)
	if e != nil {
		t.Fatal(e)
	}
	_, e = f.s.MetricValidationSnapshot(ctx, f.owner, f.project, d.ID, reporting.MetricValidate{DraftRevision: d.DraftRevision})
	faultCode(t, e, "INVALID_FILTER_TYPE")
	definition.Filter.Value = 0
	d, e = f.s.SaveMetricDraft(ctx, f.owner, f.project, d.ID, d.DraftRevision, definition)
	if e != nil {
		t.Fatal(e)
	}
	validation = metricValidate(t, f, d)
	if _, e = f.s.Pool.Exec(ctx, `CREATE FUNCTION reject_metric_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action LIKE 'metric.%' THEN RAISE EXCEPTION 'synthetic audit failure'; END IF; RETURN NEW; END; $$; CREATE TRIGGER reject_metric_audit BEFORE INSERT ON audit_entries FOR EACH ROW EXECUTE FUNCTION reject_metric_audit()`); e != nil {
		t.Fatal(e)
	}
	before := metricCounts(t, f)
	if _, e = f.s.PublishMetric(ctx, f.owner, f.project, d.ID, "audit-fail", metricPublish(d, validation)); e == nil {
		t.Fatal("audit failure succeeded")
	}
	if _, e = f.s.SaveMetricDraft(ctx, f.owner, f.project, d.ID, d.DraftRevision, metricDefinition()); e == nil {
		t.Fatal("audit failure saved")
	}
	if _, e = f.s.CreateMetric(ctx, f.owner, f.project, "audit-fail", reporting.MetricCreate{Environment: "development", Definition: metricDefinition()}); e == nil {
		t.Fatal("audit failure created")
	}
	if !reflect.DeepEqual(before, metricCounts(t, f)) {
		t.Fatal("audit failure left writes")
	}
	if _, e = f.s.Pool.Exec(ctx, `DROP TRIGGER reject_metric_audit ON audit_entries`); e != nil {
		t.Fatal(e)
	}
	if _, e = f.s.PublishMetric(ctx, f.owner, f.project, d.ID, "audit-fail", metricPublish(d, validation)); e != nil {
		t.Fatal("rolled-back idempotency poisoned retry", e)
	}
	if _, e = f.s.Pool.Exec(ctx, `UPDATE project_members SET role='viewer' WHERE project_id=$1 AND user_id=$2`, f.project, f.owner); e != nil {
		t.Fatal(e)
	}
	_, e = f.s.CreateMetric(ctx, f.owner, f.project, "metric-create", reporting.MetricCreate{Environment: "development", Definition: metricDefinition()})
	faultCode(t, e, "PROJECT_FORBIDDEN")
	if _, e = f.s.Pool.Exec(ctx, `UPDATE accounts SET disabled=true WHERE id=$1`, f.viewer); e != nil {
		t.Fatal(e)
	}
	_, e = f.s.GetMetricDraft(ctx, f.viewer, f.project, d.ID)
	faultCode(t, e, "PROJECT_NOT_FOUND")
}

func TestMetricDefinitionByteBoundary(t *testing.T) {
	f := catalogDB(t)
	ctx := context.Background()
	definition := metricDefinition()
	definition.Filter = &analysis.Filter{Op: "or"}
	for i := 0; i < 7; i++ {
		n := 20
		if i == 6 {
			n = 5
		}
		values := make([]any, n)
		for j := range values {
			values[j] = strings.Repeat("x", 1000)
		}
		definition.Filter.Args = append(definition.Filter.Args, &analysis.Filter{Op: "in", Field: &analysis.Field{Kind: "property", EventKey: "lesson_opened", SchemaVersion: 1, Key: "lesson_id"}, Value: values})
	}
	definition, e := reporting.NormalizeMetricDefinition(definition)
	if e != nil {
		t.Fatal(e)
	}
	raw, e := json.Marshal(definition)
	if e != nil {
		t.Fatal(e)
	}
	remaining := reporting.MaxMetricDefinitionBytes - 1 - len(raw)
	if remaining < 0 || remaining > 6000 {
		t.Fatal("invalid boundary fixture", remaining)
	}
	definition.Description = strings.Repeat("界", remaining/3) + strings.Repeat("x", remaining%3)
	d, e := f.s.CreateMetric(ctx, f.owner, f.project, "size-boundary", reporting.MetricCreate{Environment: "development", Definition: definition})
	if e != nil {
		t.Fatal("legal bounded normalized JSON rejected", e)
	}
	var size int
	if e = f.s.Pool.QueryRow(ctx, `SELECT octet_length(definition::text) FROM metrics WHERE metric_id=$1`, d.ID).Scan(&size); e != nil || size != reporting.MaxMetricDefinitionBytes-1 {
		t.Fatal(size, e)
	}
	definition.Description += "xx"
	_, e = f.s.CreateMetric(ctx, f.owner, f.project, "size-overflow", reporting.MetricCreate{Environment: "development", Definition: definition})
	faultCode(t, e, "BODY_TOO_LARGE")
	if _, e = f.s.Pool.Exec(ctx, `UPDATE metrics SET definition=json_build_object('title',title,'description',repeat('x',131072),'event_versions',definition->'event_versions','aggregation',definition->'aggregation') WHERE metric_id=$1`, d.ID); e == nil {
		t.Fatal("database accepted oversized definition")
	}
}
