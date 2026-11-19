//go:build integration

package postgres

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/howtomakeaname/Evia/backend/internal/analysis"
	"github.com/howtomakeaname/Evia/backend/internal/platform/canonical"
	"github.com/howtomakeaname/Evia/backend/internal/platform/ids"
	"github.com/howtomakeaname/Evia/backend/internal/reporting"
)

func savedDefinition() reporting.SavedAnalysisDefinition {
	return reporting.SavedAnalysisDefinition{Title: "Lesson count", Query: analysis.Template{Kind: analysis.QueryKindTrend, EventVersions: []analysis.EventVersion{{EventKey: "lesson_opened", SchemaVersion: 1}}}, Visualization: reporting.SavedAnalysisVisualization{Kind: "metric"}}
}
func savedFixture(t *testing.T) (catalogFixture, reporting.SavedAnalysisDraft) {
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
	if _, e := f.svc.CreateRelease(ctx, f.owner, f.project, "saved-events", releaseInput("lesson_opened", draft.Revision)); e != nil {
		t.Fatal(e)
	}
	out, e := f.s.CreateSavedAnalysis(ctx, f.owner, f.project, "saved-create", reporting.SavedAnalysisCreate{Environment: "development", Definition: savedDefinition()})
	if e != nil {
		t.Fatal(e)
	}
	return f, out
}
func savedValidate(t *testing.T, f catalogFixture, d reporting.SavedAnalysisDraft) reporting.SavedAnalysisValidation {
	t.Helper()
	snapshot, e := f.s.SavedAnalysisValidationSnapshot(context.Background(), f.viewer, f.project, d.ID, reporting.SavedAnalysisValidate{DraftRevision: d.DraftRevision, ExpectedPublishedVersion: d.PublishedVersion})
	if e != nil {
		t.Fatal(e)
	}
	out, e := reporting.BuildSavedAnalysisValidation(snapshot)
	if e != nil {
		t.Fatal(e)
	}
	return out
}
func savedPublish(d reporting.SavedAnalysisDraft, v reporting.SavedAnalysisValidation) reporting.SavedAnalysisPublish {
	return reporting.SavedAnalysisPublish{DraftRevision: d.DraftRevision, ExpectedPublishedVersion: d.PublishedVersion, ValidationHash: v.ValidationHash}
}
func TestSavedAnalysisPersistenceRevisionSnapshotAndOriginalReplay(t *testing.T) {
	f, d := savedFixture(t)
	ctx := context.Background()
	initial := d
	before := savedCounts(t, f)
	validation := savedValidate(t, f, d)
	again := savedValidate(t, f, d)
	if !reflect.DeepEqual(validation, again) || !reflect.DeepEqual(before, savedCounts(t, f)) {
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
	in := savedPublish(d, validation)
	version, e := f.s.PublishSavedAnalysis(ctx, f.owner, f.project, d.ID, "publish-one", in)
	if e != nil {
		t.Fatal(e)
	}
	if version.Version != 1 || version.DraftRevision != 1 || version.AnalysisID != d.ID || version.DefinitionHash != validation.DefinitionHash {
		t.Fatal(version)
	}
	updated := savedDefinition()
	updated.Title = "Changed current draft"
	d, e = f.s.SaveSavedAnalysisDraft(ctx, f.owner, f.project, d.ID, 1, updated)
	if e != nil {
		t.Fatal(e)
	}
	if d.DraftRevision != 2 || d.PublishedVersion != 1 || d.Title != updated.Title {
		t.Fatal(d)
	}
	replay, e := f.s.CreateSavedAnalysis(ctx, f.owner, f.project, "saved-create", reporting.SavedAnalysisCreate{Environment: "development", Definition: savedDefinition()})
	if e != nil || !savedWireEqual(initial, replay) {
		t.Fatal("create did not replay original response", replay, e)
	}
	replayVersion, e := f.s.PublishSavedAnalysis(ctx, f.owner, f.project, d.ID, "publish-one", in)
	if e != nil || !savedWireEqual(version, replayVersion) {
		t.Fatal("publish did not replay original response", replayVersion, e)
	}
	_, e = f.s.PublishSavedAnalysis(ctx, f.owner, f.project, d.ID, "publish-two", in)
	faultCode(t, e, "SAVED_ANALYSIS_REVISION_CONFLICT")
	_, e = f.s.CreateSavedAnalysis(ctx, f.owner, f.project, "saved-create", reporting.SavedAnalysisCreate{Environment: "development", Definition: updated})
	faultCode(t, e, "IDEMPOTENCY_CONFLICT")
	validation = savedValidate(t, f, d)
	wrong := savedPublish(d, validation)
	wrong.ValidationHash = strings.Repeat("0", 64)
	_, e = f.s.PublishSavedAnalysis(ctx, f.owner, f.project, d.ID, "bad-hash", wrong)
	faultCode(t, e, "VALIDATION_HASH_CONFLICT")
	v2, e := f.s.PublishSavedAnalysis(ctx, f.owner, f.project, d.ID, "publish-two", savedPublish(d, validation))
	if e != nil || v2.Version != 2 {
		t.Fatal(v2, e)
	}
	old, e := f.s.GetSavedAnalysisVersion(ctx, f.viewer, f.project, d.ID, 1)
	if e != nil || old.Definition.Title != "Lesson count" {
		t.Fatal(old, e)
	}
	list, e := f.s.ListSavedAnalysisVersions(ctx, f.viewer, f.project, d.ID, 1, 1)
	if e != nil || len(list) != 1 || list[0].Version != 2 {
		t.Fatal(list, e)
	}
	if _, e = f.s.ListSavedAnalysisVersions(ctx, f.viewer, f.project, d.ID, 0, 52); e == nil {
		t.Fatal("unbounded versions")
	}
	var witness bool
	if e = f.s.Pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM saved_analysis_dependencies d JOIN catalog_release_events re USING(project_id,environment,event_key,schema_version,release_id) WHERE d.analysis_id=$1 AND d.release_id=(SELECT r.release_id FROM catalog_release_events r WHERE r.project_id=d.project_id AND r.environment=d.environment AND r.event_key=d.event_key AND r.schema_version=d.schema_version ORDER BY r.release_id LIMIT 1))`, d.ID).Scan(&witness); e != nil || !witness {
		t.Fatal("no scoped publication witness", e)
	}
	for next := int64(3); next <= 11; next++ {
		current, e := f.s.GetSavedAnalysisDraft(ctx, f.owner, f.project, d.ID)
		if e != nil {
			t.Fatal(e)
		}
		check := savedValidate(t, f, current)
		v, e := f.s.PublishSavedAnalysis(ctx, f.owner, f.project, d.ID, "numeric-"+string(rune('a'+next)), savedPublish(current, check))
		if e != nil || v.Version != next {
			t.Fatal(v, e)
		}
	}
	numeric, e := f.s.ListSavedAnalysisVersions(ctx, f.viewer, f.project, d.ID, 9, 2)
	if e != nil || len(numeric) != 2 || numeric[0].Version != 10 || numeric[1].Version != 11 {
		t.Fatal("versions not numeric", numeric, e)
	}
	for _, sql := range []string{`UPDATE saved_analysis_versions SET definition=definition WHERE analysis_id=$1`, `DELETE FROM saved_analysis_versions WHERE analysis_id=$1`, `UPDATE saved_analysis_dependencies SET definition_hash=definition_hash WHERE analysis_id=$1`, `DELETE FROM saved_analysis_dependencies WHERE analysis_id=$1`, `UPDATE saved_analyses SET environment='production' WHERE analysis_id=$1`, `UPDATE saved_analyses SET published_version=99 WHERE analysis_id=$1`} {
		if _, e = f.s.Pool.Exec(ctx, sql, d.ID); e == nil {
			t.Fatal("immutable/scoped relation accepted mutation")
		}
	}
}
func savedCounts(t *testing.T, f catalogFixture) []int64 {
	t.Helper()
	out := []int64{}
	for _, table := range []string{"saved_analyses", "saved_analysis_versions", "saved_analysis_dependencies", "saved_analysis_idempotency", "analysis_query_jobs", "audit_entries", "catalog_versions", "catalog_releases", "catalog_environments"} {
		var n int64
		if e := f.s.Pool.QueryRow(context.Background(), "SELECT count(*) FROM "+table).Scan(&n); e != nil {
			t.Fatal(e)
		}
		out = append(out, n)
	}
	return out
}
func TestSavedAnalysisConcurrentRevisionPublicationAndReplay(t *testing.T) {
	f, d := savedFixture(t)
	ctx := context.Background()
	var wg sync.WaitGroup
	var successes atomic.Int32
	for range 8 {
		wg.Go(func() {
			v, e := f.s.CreateSavedAnalysis(ctx, f.owner, f.project, "saved-create", reporting.SavedAnalysisCreate{Environment: "development", Definition: savedDefinition()})
			if e != nil || v.ID != d.ID {
				t.Error("concurrent create replay changed identity", v.ID, e)
			}
		})
	}
	wg.Wait()
	for range 8 {
		wg.Go(func() {
			_, e := f.s.SaveSavedAnalysisDraft(ctx, f.owner, f.project, d.ID, 1, savedDefinition())
			if e == nil {
				successes.Add(1)
			} else {
				faultCode(t, e, "SAVED_ANALYSIS_REVISION_CONFLICT")
			}
		})
	}
	wg.Wait()
	if successes.Load() != 1 {
		t.Fatal("concurrent revision lost", successes.Load())
	}
	d, e := f.s.GetSavedAnalysisDraft(ctx, f.viewer, f.project, d.ID)
	if e != nil {
		t.Fatal(e)
	}
	validation := savedValidate(t, f, d)
	in := savedPublish(d, validation)
	for range 8 {
		wg.Go(func() {
			v, e := f.s.PublishSavedAnalysis(ctx, f.owner, f.project, d.ID, "parallel-publish", in)
			if e != nil || v.Version != 1 {
				t.Error(v, e)
			}
		})
	}
	wg.Wait()
	versions, e := f.s.ListSavedAnalysisVersions(ctx, f.viewer, f.project, d.ID, 0, 51)
	if e != nil || len(versions) != 1 {
		t.Fatal(versions, e)
	}
	d, e = f.s.GetSavedAnalysisDraft(ctx, f.viewer, f.project, d.ID)
	if e != nil {
		t.Fatal(e)
	}
	validation = savedValidate(t, f, d)
	in = savedPublish(d, validation)
	successes.Store(0)
	for i := range 8 {
		key := string(rune('a' + i))
		wg.Go(func() {
			_, e := f.s.PublishSavedAnalysis(ctx, f.owner, f.project, d.ID, key, in)
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
func TestSavedAnalysisAuthorizationTypesAndAtomicRollback(t *testing.T) {
	f, d := savedFixture(t)
	ctx := context.Background()
	_, e := f.s.GetSavedAnalysis(ctx, f.outsider, f.project, d.ID)
	faultCode(t, e, "PROJECT_NOT_FOUND")
	_, e = f.s.GetSavedAnalysis(ctx, f.owner, f.otherProject, d.ID)
	faultCode(t, e, "SAVED_ANALYSIS_NOT_FOUND")
	_, e = f.s.GetSavedAnalysisVersion(ctx, f.owner, f.otherProject, d.ID, 1)
	faultCode(t, e, "SAVED_ANALYSIS_VERSION_NOT_FOUND")
	_, e = f.s.CreateSavedAnalysis(ctx, f.viewer, f.project, "denied", reporting.SavedAnalysisCreate{Environment: "development", Definition: savedDefinition()})
	faultCode(t, e, "PROJECT_FORBIDDEN")
	_, e = f.s.SaveSavedAnalysisDraft(ctx, f.viewer, f.project, d.ID, 1, savedDefinition())
	faultCode(t, e, "PROJECT_FORBIDDEN")
	validation := savedValidate(t, f, d)
	_, e = f.s.PublishSavedAnalysis(ctx, f.viewer, f.project, d.ID, "denied", savedPublish(d, validation))
	faultCode(t, e, "PROJECT_FORBIDDEN")
	staging, e := f.s.CreateSavedAnalysis(ctx, f.owner, f.project, "staging", reporting.SavedAnalysisCreate{Environment: "staging", Definition: savedDefinition()})
	if e != nil {
		t.Fatal(e)
	}
	page, e := f.s.ListSavedAnalyses(ctx, f.viewer, f.project, "staging", "", 101, false, "")
	if e != nil || len(page) != 1 || page[0].ID != staging.ID {
		t.Fatal("environment summary scope", page, e)
	}
	page, e = f.s.ListSavedAnalyses(ctx, f.viewer, f.project, "development", "", 101, false, "")
	if e != nil || len(page) != 1 || page[0].ID != d.ID {
		t.Fatal("environment summary scope", page, e)
	}
	_, e = f.s.SavedAnalysisValidationSnapshot(ctx, f.viewer, f.project, staging.ID, reporting.SavedAnalysisValidate{DraftRevision: 1})
	faultCode(t, e, "QUERY_EVENT_VERSION_NOT_PUBLISHED")
	definition := savedDefinition()
	definition.Query.Filter = &analysis.Filter{Op: "eq", Field: &analysis.Field{Kind: "property", EventKey: "lesson_opened", SchemaVersion: 1, Key: "lesson_id"}, Value: 0}
	d, e = f.s.SaveSavedAnalysisDraft(ctx, f.owner, f.project, d.ID, 1, definition)
	if e != nil {
		t.Fatal(e)
	}
	validation = savedValidate(t, f, d)
	b, e := json.Marshal(validation.NormalizedDefinition)
	if e != nil || !strings.Contains(string(b), `"value":0`) || strings.Contains(string(b), "resolved_type") {
		t.Fatal("scalar or internal type changed", string(b), e)
	}
	for _, tc := range []struct {
		key   string
		value any
	}{{"enabled", false}, {"tags", []any{"first", "second"}}} {
		typed := savedDefinition()
		typed.Query.Filter = &analysis.Filter{Op: "eq", Field: &analysis.Field{Kind: "property", EventKey: "lesson_opened", SchemaVersion: 1, Key: tc.key}, Value: tc.value}
		d, e = f.s.SaveSavedAnalysisDraft(ctx, f.owner, f.project, d.ID, d.DraftRevision, typed)
		if e != nil {
			t.Fatal(e)
		}
		result := savedValidate(t, f, d)
		if !savedWireEqual(result.NormalizedDefinition.Query.Filter.Value, tc.value) {
			t.Fatal("typed value changed")
		}
	}
	definition.Query.Filter.Value = false
	d, e = f.s.SaveSavedAnalysisDraft(ctx, f.owner, f.project, d.ID, d.DraftRevision, definition)
	if e != nil {
		t.Fatal(e)
	}
	_, e = f.s.SavedAnalysisValidationSnapshot(ctx, f.viewer, f.project, d.ID, reporting.SavedAnalysisValidate{DraftRevision: d.DraftRevision})
	faultCode(t, e, "INVALID_FILTER_TYPE")
	definition.Query.Filter.Value = 0
	d, e = f.s.SaveSavedAnalysisDraft(ctx, f.owner, f.project, d.ID, d.DraftRevision, definition)
	if e != nil {
		t.Fatal(e)
	}
	validation = savedValidate(t, f, d)
	if _, e = f.s.Pool.Exec(ctx, `CREATE FUNCTION reject_saved_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action LIKE 'analysis.saved%' THEN RAISE EXCEPTION 'synthetic audit failure'; END IF; RETURN NEW; END; $$; CREATE TRIGGER reject_saved_audit BEFORE INSERT ON audit_entries FOR EACH ROW EXECUTE FUNCTION reject_saved_audit()`); e != nil {
		t.Fatal(e)
	}
	before := savedCounts(t, f)
	if _, e = f.s.PublishSavedAnalysis(ctx, f.owner, f.project, d.ID, "audit-fail", savedPublish(d, validation)); e == nil {
		t.Fatal("audit failure succeeded")
	}
	if _, e = f.s.SaveSavedAnalysisDraft(ctx, f.owner, f.project, d.ID, d.DraftRevision, savedDefinition()); e == nil {
		t.Fatal("audit failure saved")
	}
	if _, e = f.s.CreateSavedAnalysis(ctx, f.owner, f.project, "audit-fail", reporting.SavedAnalysisCreate{Environment: "development", Definition: savedDefinition()}); e == nil {
		t.Fatal("audit failure created")
	}
	if !reflect.DeepEqual(before, savedCounts(t, f)) {
		t.Fatal("audit failure left writes")
	}
	if _, e = f.s.Pool.Exec(ctx, `DROP TRIGGER reject_saved_audit ON audit_entries`); e != nil {
		t.Fatal(e)
	}
	if _, e = f.s.PublishSavedAnalysis(ctx, f.owner, f.project, d.ID, "audit-fail", savedPublish(d, validation)); e != nil {
		t.Fatal("rolled-back idempotency poisoned retry", e)
	}
	if _, e = f.s.Pool.Exec(ctx, `UPDATE project_members SET role='viewer' WHERE project_id=$1 AND user_id=$2`, f.project, f.owner); e != nil {
		t.Fatal(e)
	}
	_, e = f.s.CreateSavedAnalysis(ctx, f.owner, f.project, "saved-create", reporting.SavedAnalysisCreate{Environment: "development", Definition: savedDefinition()})
	faultCode(t, e, "PROJECT_FORBIDDEN")
	if _, e = f.s.Pool.Exec(ctx, `UPDATE accounts SET disabled=true WHERE id=$1`, f.viewer); e != nil {
		t.Fatal(e)
	}
	_, e = f.s.GetSavedAnalysisDraft(ctx, f.viewer, f.project, d.ID)
	faultCode(t, e, "PROJECT_NOT_FOUND")
}

func savedWireEqual(a, b any) bool {
	x, e := canonical.JSON(a)
	if e != nil {
		return false
	}
	y, e := canonical.JSON(b)
	return e == nil && string(x) == string(y)
}

func TestSavedAnalysisUpgradePreservesPublishedCatalog(t *testing.T) {
	legacy := populatedLegacyCatalog(t, 1)
	s := legacy.store
	ctx := context.Background()
	// Reproduce an applied 006 database with real preexisting immutable facts.
	tx, e := s.Pool.Begin(ctx)
	if e != nil {
		t.Fatal(e)
	}
	defer tx.Rollback(context.Background())
	for _, name := range []string{"003_catalog_membership_delivery.sql", "004_sources.sql", "005_queries.sql", "006_release_activation.sql"} {
		data, e := migrations.ReadFile("migrations/" + name)
		if e != nil {
			t.Fatal(e)
		}
		if _, e = tx.Exec(ctx, string(data)); e != nil {
			t.Fatal(e)
		}
		sum := sha256.Sum256(data)
		if _, e = tx.Exec(ctx, `INSERT INTO schema_migrations(name,checksum) VALUES($1,$2)`, name, hex.EncodeToString(sum[:])); e != nil {
			t.Fatal(e)
		}
	}
	if e = tx.Commit(ctx); e != nil {
		t.Fatal(e)
	}
	var before, after string
	if e = s.Pool.QueryRow(ctx, `SELECT jsonb_build_object('versions',(SELECT jsonb_agg(to_jsonb(v)) FROM catalog_versions v),'releases',(SELECT jsonb_agg(to_jsonb(r)) FROM catalog_releases r),'members',(SELECT jsonb_agg(to_jsonb(re)) FROM catalog_release_events re))::text`).Scan(&before); e != nil {
		t.Fatal(e)
	}
	if e = s.Migrate(ctx); e != nil {
		t.Fatal(e)
	}
	if e = s.Migrate(ctx); e != nil {
		t.Fatal(e)
	}
	if e = s.CheckMigrations(ctx); e != nil {
		t.Fatal(e)
	}
	if e = s.Pool.QueryRow(ctx, `SELECT jsonb_build_object('versions',(SELECT jsonb_agg(to_jsonb(v)) FROM catalog_versions v),'releases',(SELECT jsonb_agg(to_jsonb(r)) FROM catalog_releases r),'members',(SELECT jsonb_agg(to_jsonb(re)) FROM catalog_release_events re))::text`).Scan(&after); e != nil || before != after {
		t.Fatal("upgrade changed published catalog", e)
	}
	d, e := s.CreateSavedAnalysis(ctx, legacy.actor, legacy.project, "after-upgrade", reporting.SavedAnalysisCreate{Environment: "development", Definition: savedDefinition()})
	if e != nil {
		t.Fatal(e)
	}
	snapshot, e := s.SavedAnalysisValidationSnapshot(ctx, legacy.actor, legacy.project, d.ID, reporting.SavedAnalysisValidate{DraftRevision: 1})
	if e != nil {
		t.Fatal(e)
	}
	validation, e := reporting.BuildSavedAnalysisValidation(snapshot)
	if e != nil {
		t.Fatal(e)
	}
	v, e := s.PublishSavedAnalysis(ctx, legacy.actor, legacy.project, d.ID, "after-upgrade", savedPublish(d, validation))
	if e != nil || v.Version != 1 {
		t.Fatal(v, e)
	}
}

func TestSavedAnalysisDefinitionByteBoundary(t *testing.T) {
	f := catalogDB(t)
	ctx := context.Background()
	definition := savedDefinition()
	definition.Query.Filter = &analysis.Filter{Op: "or"}
	for i := 0; i < 7; i++ {
		n := 20
		if i == 6 {
			n = 5
		}
		values := make([]any, n)
		for j := range values {
			values[j] = strings.Repeat("x", 1000)
		}
		definition.Query.Filter.Args = append(definition.Query.Filter.Args, &analysis.Filter{Op: "in", Field: &analysis.Field{Kind: "property", EventKey: "lesson_opened", SchemaVersion: 1, Key: "lesson_id"}, Value: values})
	}
	definition, e := reporting.NormalizeSavedAnalysisDefinition(definition)
	if e != nil {
		t.Fatal(e)
	}
	raw, e := json.Marshal(definition)
	if e != nil {
		t.Fatal(e)
	}
	remaining := reporting.MaxSavedAnalysisDefinitionBytes - 1 - len(raw)
	if remaining < 0 || remaining > 6000 {
		t.Fatal("invalid boundary fixture", remaining)
	}
	definition.Description = strings.Repeat("界", remaining/3) + strings.Repeat("x", remaining%3)
	d, e := f.s.CreateSavedAnalysis(ctx, f.owner, f.project, "size-boundary", reporting.SavedAnalysisCreate{Environment: "development", Definition: definition})
	if e != nil {
		t.Fatal("legal bounded normalized JSON rejected", e)
	}
	var size int
	if e = f.s.Pool.QueryRow(ctx, `SELECT octet_length(definition::text) FROM saved_analyses WHERE analysis_id=$1`, d.ID).Scan(&size); e != nil || size != reporting.MaxSavedAnalysisDefinitionBytes-1 {
		t.Fatal(size, e)
	}
	definition.Description += "xx"
	_, e = f.s.CreateSavedAnalysis(ctx, f.owner, f.project, "size-overflow", reporting.SavedAnalysisCreate{Environment: "development", Definition: definition})
	faultCode(t, e, "BODY_TOO_LARGE")
	if _, e = f.s.Pool.Exec(ctx, `UPDATE saved_analyses SET definition=json_build_object('title',title,'description',repeat('x',131072),'query',json_build_object('kind',kind)) WHERE analysis_id=$1`, d.ID); e == nil {
		t.Fatal("database accepted oversized definition")
	}
}

func TestSavedAnalysisRetentionPersistsAndPinsBoundarySteps(t *testing.T) {
	f := catalogDB(t)
	ctx := context.Background()
	signUp := f.event(t, "sign_up")
	if _, e := f.svc.CreateRelease(ctx, f.owner, f.project, "retention-release-start", releaseInput("sign_up", signUp.Revision)); e != nil {
		t.Fatal(e)
	}
	login := f.event(t, "login")
	if _, e := f.svc.CreateRelease(ctx, f.owner, f.project, "retention-release-return", releaseInput("login", login.Revision)); e != nil {
		t.Fatal(e)
	}
	definition := reporting.SavedAnalysisDefinition{
		Title: "Signup retention",
		Query: analysis.Template{
			Kind:    "retention",
			Subject: analysis.SubjectUID,
			Retention: &analysis.Retention{
				StartEvent:  analysis.Step{EventKey: "sign_up", SchemaVersion: 1},
				ReturnEvent: analysis.Step{EventKey: "login", SchemaVersion: 1},
				Granularity: "day",
				Periods:     4,
			},
		},
		Visualization: reporting.SavedAnalysisVisualization{Kind: "table"},
	}
	draft, e := f.s.CreateSavedAnalysis(ctx, f.owner, f.project, "retention-create", reporting.SavedAnalysisCreate{Environment: "development", Definition: definition})
	if e != nil {
		t.Fatalf("retention saved analysis must pass the widened kind check: %v", e)
	}
	if draft.Kind != analysis.QueryKindRetention {
		t.Fatalf("stored kind = %q, want retention", draft.Kind)
	}
	validation := savedValidate(t, f, draft)
	if !validation.Valid || validation.Dependencies.SelectionMode != "retention_steps" || len(validation.Dependencies.EventVersions) != 2 {
		t.Fatalf("retention dependency manifest wrong: %+v", validation.Dependencies)
	}
	version, e := f.s.PublishSavedAnalysis(ctx, f.owner, f.project, draft.ID, "retention-publish", savedPublish(draft, validation))
	if e != nil {
		t.Fatal(e)
	}
	if version.Version != 1 || version.DefinitionHash != validation.DefinitionHash {
		t.Fatal(version)
	}
}

func TestSavedAnalysisAdvancedKindsPersistWithTypedPins(t *testing.T) {
	f := catalogDB(t)
	ctx := context.Background()
	for _, key := range []string{"sign_up", "open", "buy", "ad_click"} {
		draft := f.event(t, key)
		if _, e := f.svc.CreateRelease(ctx, f.owner, f.project, "release-"+key, releaseInput(key, draft.Revision)); e != nil {
			t.Fatal(e)
		}
	}
	cases := []struct {
		name       string
		mode       string
		pinCount   int
		definition reporting.SavedAnalysisDefinition
	}{
		{
			name:     "segment",
			mode:     "segment_steps",
			pinCount: 1,
			definition: reporting.SavedAnalysisDefinition{
				Title: "Plan segments",
				Query: analysis.Template{
					Kind: analysis.QueryKindSegment, Subject: analysis.SubjectUID,
					Segment: &analysis.Segment{
						Event: analysis.Step{EventKey: "sign_up", SchemaVersion: 1},
						Field: analysis.Field{Kind: "property", EventKey: "sign_up", SchemaVersion: 1, Key: "lesson_id"},
					},
				},
				Visualization: reporting.SavedAnalysisVisualization{Kind: "table"},
			},
		},
		{
			name:     "pinned path",
			mode:     "path_steps",
			pinCount: 1,
			definition: reporting.SavedAnalysisDefinition{
				Title: "Onboarding paths",
				Query: analysis.Template{
					Kind: analysis.QueryKindPath, Subject: analysis.SubjectUID,
					Path: &analysis.Path{Depth: 3, StartEvent: &analysis.Step{EventKey: "open", SchemaVersion: 1}},
				},
				Visualization: reporting.SavedAnalysisVisualization{Kind: "table"},
			},
		},
		{
			name:     "unpinned path",
			mode:     "all_events",
			pinCount: 0,
			definition: reporting.SavedAnalysisDefinition{
				Title: "Full paths",
				Query: analysis.Template{
					Kind: analysis.QueryKindPath, Subject: analysis.SubjectUID,
					Path: &analysis.Path{Depth: 3},
				},
				Visualization: reporting.SavedAnalysisVisualization{Kind: "table"},
			},
		},
		{
			name:     "attribution",
			mode:     "attribution_steps",
			pinCount: 2,
			definition: reporting.SavedAnalysisDefinition{
				Title: "Conversion attribution",
				Query: analysis.Template{
					Kind: analysis.QueryKindAttribution, Subject: analysis.SubjectUID,
					Attribution: &analysis.Attribution{
						Conversion:    analysis.Step{EventKey: "buy", SchemaVersion: 1},
						Touchpoints:   []analysis.Step{{EventKey: "ad_click", SchemaVersion: 1}},
						WindowSeconds: 86400,
						Model:         analysis.AttributionModelLastTouch,
					},
				},
				Visualization: reporting.SavedAnalysisVisualization{Kind: "table"},
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			draft, e := f.s.CreateSavedAnalysis(ctx, f.owner, f.project, "advanced-create-"+tc.name, reporting.SavedAnalysisCreate{Environment: "development", Definition: tc.definition})
			if e != nil {
				t.Fatalf("%s must pass the widened kind check: %v", tc.name, e)
			}
			if draft.Kind != tc.definition.Query.Kind {
				t.Fatalf("stored kind = %q", draft.Kind)
			}
			validation := savedValidate(t, f, draft)
			if !validation.Valid || validation.Dependencies.SelectionMode != tc.mode || len(validation.Dependencies.EventVersions) != tc.pinCount {
				t.Fatalf("%s manifest wrong: %+v", tc.name, validation.Dependencies)
			}
			version, e := f.s.PublishSavedAnalysis(ctx, f.owner, f.project, draft.ID, "advanced-publish-"+tc.name, savedPublish(draft, validation))
			if e != nil || version.Version != 1 {
				t.Fatalf("%s publish failed: %v", tc.name, e)
			}
		})
	}
}

func TestSavedAnalysisArchiveLifecycleAndMutationGuards(t *testing.T) {
	f, d := savedFixture(t)
	ctx := context.Background()
	validation := savedValidate(t, f, d)
	if _, e := f.s.PublishSavedAnalysis(ctx, f.owner, f.project, d.ID, "archive-publish", savedPublish(d, validation)); e != nil {
		t.Fatal(e)
	}
	keep, e := f.s.CreateSavedAnalysis(ctx, f.owner, f.project, "archive-keep", reporting.SavedAnalysisCreate{Environment: "development", Definition: savedDefinition()})
	if e != nil {
		t.Fatal(e)
	}

	// A read-only viewer cannot change lifecycle state.
	if _, e = f.s.ArchiveSavedAnalysis(ctx, f.viewer, f.project, d.ID); e == nil || faultPublicCode(e) != "PROJECT_FORBIDDEN" {
		t.Fatalf("viewer archive must be forbidden: %v", e)
	}

	archived, e := f.s.ArchiveSavedAnalysis(ctx, f.owner, f.project, d.ID)
	if e != nil {
		t.Fatal(e)
	}
	if archived.ArchivedAt == nil || archived.ArchivedBy != f.owner {
		t.Fatalf("archive marker missing: %+v", archived)
	}
	if _, e = f.s.ArchiveSavedAnalysis(ctx, f.owner, f.project, d.ID); e == nil || faultPublicCode(e) != "SAVED_ANALYSIS_ALREADY_ARCHIVED" {
		t.Fatalf("re-archive must conflict: %v", e)
	}

	// Default listing hides archived analyses; the archived stream is opt-in.
	listed, e := f.s.ListSavedAnalyses(ctx, f.owner, f.project, "development", "", 100, false, "")
	if e != nil {
		t.Fatal(e)
	}
	for _, item := range listed {
		if item.ID == d.ID {
			t.Fatal("archived analysis must leave the default listing")
		}
		if item.ArchivedAt != nil {
			t.Fatal("active analysis must not carry an archive marker")
		}
	}
	if len(listed) != 1 || listed[0].ID != keep.ID {
		t.Fatalf("listing must retain only the active analysis: %+v", listed)
	}
	archivedList, e := f.s.ListSavedAnalyses(ctx, f.owner, f.project, "development", "", 100, true, "")
	if e != nil {
		t.Fatal(e)
	}
	if len(archivedList) != 1 || archivedList[0].ID != d.ID || archivedList[0].ArchivedAt == nil {
		t.Fatalf("archived listing must return only the archived analysis with marker: %+v", archivedList)
	}

	// Reads by ID and draft stay available and expose the marker.
	got, e := f.s.GetSavedAnalysis(ctx, f.owner, f.project, d.ID)
	if e != nil || got.ArchivedAt == nil {
		t.Fatalf("archived analysis must remain readable by ID: %+v %v", got, e)
	}
	gotDraft, e := f.s.GetSavedAnalysisDraft(ctx, f.owner, f.project, d.ID)
	if e != nil || gotDraft.ArchivedAt == nil {
		t.Fatalf("archived draft must remain readable with marker: %+v %v", gotDraft, e)
	}
	// Immutable versions stay readable, keeping dashboard/report references valid.
	if _, e = f.s.GetSavedAnalysisVersion(ctx, f.owner, f.project, d.ID, 1); e != nil {
		t.Fatalf("immutable version must stay readable: %v", e)
	}

	// Every mutating path fails closed while archived.
	if _, e = f.s.SaveSavedAnalysisDraft(ctx, f.owner, f.project, d.ID, d.DraftRevision, savedDefinition()); e == nil || faultPublicCode(e) != "SAVED_ANALYSIS_ARCHIVED" {
		t.Fatalf("draft save must reject archived analysis: %v", e)
	}
	if _, e = f.s.SavedAnalysisValidationSnapshot(ctx, f.owner, f.project, d.ID, reporting.SavedAnalysisValidate{DraftRevision: d.DraftRevision}); e == nil || faultPublicCode(e) != "SAVED_ANALYSIS_ARCHIVED" {
		t.Fatalf("validation must reject archived analysis: %v", e)
	}
	if _, e = f.s.PublishSavedAnalysis(ctx, f.owner, f.project, d.ID, ids.New(), savedPublish(d, validation)); e == nil || faultPublicCode(e) != "SAVED_ANALYSIS_ARCHIVED" {
		t.Fatalf("publish must reject archived analysis: %v", e)
	}
	if _, e = f.s.EnqueueSavedAnalysisVersion(ctx, f.owner, f.project, d.ID, 1, ids.New(), reporting.SavedAnalysisExecute{TimeRange: analysis.TimeRange{From: archived.ArchivedAt.UTC(), To: archived.ArchivedAt.UTC().Add(time.Hour)}, Timezone: "UTC"}); e == nil || faultPublicCode(e) != "SAVED_ANALYSIS_ARCHIVED" {
		t.Fatalf("run enqueue must reject archived analysis: %v", e)
	}

	// Unknown id resolves to not-found, not an archive conflict.
	if _, e = f.s.ArchiveSavedAnalysis(ctx, f.owner, f.project, ids.New()); e == nil || faultPublicCode(e) != "SAVED_ANALYSIS_NOT_FOUND" {
		t.Fatalf("unknown analysis must be not-found: %v", e)
	}

	// Restoring an active analysis conflicts; restoring the archived one clears markers.
	if _, e = f.s.UnarchiveSavedAnalysis(ctx, f.owner, f.project, keep.ID); e == nil || faultPublicCode(e) != "SAVED_ANALYSIS_NOT_ARCHIVED" {
		t.Fatalf("unarchiving an active analysis must conflict: %v", e)
	}
	restored, e := f.s.UnarchiveSavedAnalysis(ctx, f.owner, f.project, d.ID)
	if e != nil {
		t.Fatal(e)
	}
	if restored.ArchivedAt != nil || restored.ArchivedBy != "" {
		t.Fatalf("restore must clear archive markers: %+v", restored)
	}
	listedAgain, e := f.s.ListSavedAnalyses(ctx, f.owner, f.project, "development", "", 100, false, "")
	if e != nil || len(listedAgain) != 2 {
		t.Fatalf("restored analysis must rejoin the listing: %d %v", len(listedAgain), e)
	}
	restoredArchive, e := f.s.ListSavedAnalyses(ctx, f.owner, f.project, "development", "", 100, true, "")
	if e != nil || len(restoredArchive) != 0 {
		t.Fatalf("restored analysis must leave the archived stream: %d %v", len(restoredArchive), e)
	}

	// Both lifecycle transitions are audited after the existing create/publish entries.
	var actions []string
	rows, e := f.s.Pool.Query(ctx, `SELECT action FROM audit_entries WHERE resource_key=$1 AND action IN ('analysis.archived','analysis.unarchived') ORDER BY id`, d.ID)
	if e != nil {
		t.Fatal(e)
	}
	defer rows.Close()
	for rows.Next() {
		var action string
		if e = rows.Scan(&action); e != nil {
			t.Fatal(e)
		}
		actions = append(actions, action)
	}
	if len(actions) != 2 || actions[0] != "analysis.archived" || actions[1] != "analysis.unarchived" {
		t.Fatalf("lifecycle audit trail wrong: %v", actions)
	}
}
