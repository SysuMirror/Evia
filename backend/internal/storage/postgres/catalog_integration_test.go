//go:build integration

package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/howtomakeaname/Evia/backend/internal/access"
	"github.com/howtomakeaname/Evia/backend/internal/catalog"
	"github.com/howtomakeaname/Evia/backend/internal/ingestion"
	"github.com/howtomakeaname/Evia/backend/internal/platform/fault"
	"github.com/howtomakeaname/Evia/backend/internal/platform/ids"
)

type catalogFixture struct {
	s                                              *Store
	svc                                            *catalog.Service
	owner, viewer, outsider, project, otherProject string
}

func catalogDB(t *testing.T) catalogFixture {
	t.Helper()
	s := database(t)
	f := catalogFixture{s: s, svc: catalog.New(s), owner: ids.New(), viewer: ids.New(), outsider: ids.New(), project: ids.New(), otherProject: ids.New()}
	ctx := context.Background()
	for i, id := range []string{f.owner, f.viewer, f.outsider} {
		if _, e := s.Pool.Exec(ctx, `INSERT INTO accounts(id,username,display_name,password_hash) VALUES($1,$2,'Synthetic','unused-test-hash')`, id, fmt.Sprintf("account%d", i)); e != nil {
			t.Fatal(e)
		}
	}
	for _, p := range []access.Project{{ID: f.project, Name: "Catalog", Timezone: "UTC"}, {ID: f.otherProject, Name: "Other", Timezone: "UTC"}} {
		if e := s.CreateProject(ctx, f.owner, p); e != nil {
			t.Fatal(e)
		}
	}
	if _, e := s.Pool.Exec(ctx, `INSERT INTO project_members(project_id,user_id,role) VALUES($1,$2,'viewer')`, f.project, f.viewer); e != nil {
		t.Fatal(e)
	}
	return f
}
func publishable() catalog.DraftInput {
	return catalog.DraftInput{DisplayName: catalogString("Lesson opened"), Schema: map[string]any{"type": "object", "additionalProperties": false, "properties": map[string]any{"lesson_id": map[string]any{"type": "integer", "minimum": 1}}, "required": []any{"lesson_id"}}, Trigger: catalogString("When the lesson opens"), AllowedSourceKinds: []string{ingestion.SourceKindBrowser}, FactKind: catalogString(catalog.FactKindUserIntent), ExampleProperties: map[string]any{"lesson_id": 1}}
}
func (f catalogFixture) event(t *testing.T, key string) catalog.Draft {
	t.Helper()
	ctx := context.Background()
	if _, e := f.svc.CreateEvent(ctx, f.owner, f.project, catalog.EventInput{Key: key, DisplayName: key}); e != nil {
		t.Fatal(e)
	}
	d, e := f.svc.SaveDraft(ctx, f.owner, f.project, key, 1, publishable())
	if e != nil {
		t.Fatal(e)
	}
	return d
}
func faultCode(t *testing.T, e error, code string) {
	t.Helper()
	var f *fault.Error
	if !errors.As(e, &f) || f.Code != code {
		t.Fatalf("wanted %s, got %v", code, e)
	}
}
func releaseInput(key string, revision int64) catalog.ReleaseInput {
	return catalog.ReleaseInput{Environment: "development", Events: []catalog.ReleaseEvent{{Key: key, DraftRevision: revision}}}
}
func TestCatalogAuthorizationRevisionAndPagination(t *testing.T) {
	f := catalogDB(t)
	ctx := context.Background()
	d := f.event(t, "lesson_opened")
	_, e := f.svc.GetDraft(ctx, f.outsider, f.project, "lesson_opened")
	faultCode(t, e, "PROJECT_NOT_FOUND")
	_, e = f.svc.GetDraft(ctx, f.viewer, f.otherProject, "lesson_opened")
	faultCode(t, e, "PROJECT_NOT_FOUND")
	_, e = f.svc.GetDraft(ctx, f.owner, f.otherProject, "lesson_opened")
	faultCode(t, e, "EVENT_NOT_FOUND")
	if _, e = f.svc.GetDraft(ctx, f.viewer, f.project, "lesson_opened"); e != nil {
		t.Fatal(e)
	}
	_, e = f.svc.CreateEvent(ctx, f.viewer, f.project, catalog.EventInput{Key: "denied", DisplayName: "Denied"})
	faultCode(t, e, "PROJECT_FORBIDDEN")
	_, e = f.svc.SaveDraft(ctx, f.viewer, f.project, "lesson_opened", d.Revision, publishable())
	faultCode(t, e, "PROJECT_FORBIDDEN")
	_, e = f.svc.CreateRelease(ctx, f.viewer, f.project, "denied", releaseInput("lesson_opened", d.Revision))
	faultCode(t, e, "PROJECT_FORBIDDEN")
	_, e = f.svc.CreateEvent(ctx, f.owner, f.project, catalog.EventInput{Key: "lesson_opened", DisplayName: "Duplicate"})
	faultCode(t, e, "EVENT_KEY_EXISTS")
	_, e = f.svc.SaveDraft(ctx, f.owner, f.project, "lesson_opened", 1, publishable())
	faultCode(t, e, "DRAFT_REVISION_CONFLICT")
	_, e = f.svc.ValidateSample(ctx, f.viewer, f.project, "lesson_opened", 1, map[string]any{})
	faultCode(t, e, "DRAFT_REVISION_CONFLICT")
	result, e := f.svc.ValidateSample(ctx, f.viewer, f.project, "lesson_opened", d.Revision, map[string]any{"lesson_id": "wrong"})
	if e != nil || result.Valid || len(result.Issues) != 1 || result.Issues[0].Path != "/lesson_id" || result.Compatibility != catalog.CompatibilityInitial {
		t.Fatal(result, e)
	}
	f.event(t, "alpha")
	f.event(t, "zulu")
	list, e := f.svc.ListEvents(ctx, f.owner, f.project, "development", "", 1)
	if e != nil || len(list) != 2 || list[0].Key != "alpha" || list[1].Key != "lesson_opened" {
		t.Fatal(list, e)
	}
	list, e = f.svc.ListEvents(ctx, f.viewer, f.project, "development", list[0].Key, 1)
	if e != nil || len(list) != 2 || list[0].Key != "lesson_opened" {
		t.Fatal(list, e)
	}
	var won atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 12; i++ {
		wg.Go(func() {
			_, e := f.svc.SaveDraft(ctx, f.owner, f.project, "lesson_opened", d.Revision, publishable())
			if e == nil {
				won.Add(1)
			} else {
				faultCode(t, e, "DRAFT_REVISION_CONFLICT")
			}
		})
	}
	wg.Wait()
	if won.Load() != 1 {
		t.Fatal("concurrent stale writes succeeded", won.Load())
	}
	if _, e = f.s.Pool.Exec(ctx, `UPDATE accounts SET disabled=true WHERE id=$1`, f.viewer); e != nil {
		t.Fatal(e)
	}
	_, e = f.svc.GetDraft(ctx, f.viewer, f.project, "lesson_opened")
	faultCode(t, e, "PROJECT_NOT_FOUND")
}
func TestCatalogImmutablePublicationIdempotencyAndScope(t *testing.T) {
	f := catalogDB(t)
	ctx := context.Background()
	d := f.event(t, "lesson_opened")
	in := releaseInput("lesson_opened", d.Revision)
	r, e := f.svc.CreateRelease(ctx, f.owner, f.project, "build-1", in)
	if e != nil {
		t.Fatal(e)
	}
	if r.Revision != 1 || r.State != "published" || r.IngestionReady || r.AccelerationState != "not_requested" || len(r.ManifestHash) != 64 {
		t.Fatal(r)
	}
	repeated, e := f.svc.CreateRelease(ctx, f.owner, f.project, "build-1", in)
	if e != nil || repeated != r {
		t.Fatal(repeated, e)
	}
	other := in
	other.ExpectedActiveRevision = 1
	_, e = f.svc.CreateRelease(ctx, f.owner, f.project, "build-1", other)
	faultCode(t, e, "IDEMPOTENCY_CONFLICT")
	_, e = f.svc.CreateRelease(ctx, f.owner, f.project, "new-build", other)
	faultCode(t, e, "ACTIVE_REVISION_CONFLICT")
	_, e = f.svc.GetRelease(ctx, f.outsider, f.project, r.ID)
	faultCode(t, e, "PROJECT_NOT_FOUND")
	_, e = f.svc.GetRelease(ctx, f.owner, f.otherProject, r.ID)
	faultCode(t, e, "RELEASE_NOT_FOUND")
	if got, e := f.svc.GetRelease(ctx, f.viewer, f.project, r.ID); e != nil || got != r {
		t.Fatal(got, e)
	}
	for _, table := range []string{"catalog_releases", "catalog_versions", "catalog_release_events", "catalog_release_delivery", "catalog_idempotency", "catalog_outbox"} {
		var n int
		if e = f.s.Pool.QueryRow(ctx, "SELECT count(*) FROM "+table).Scan(&n); e != nil || n != 1 {
			t.Fatal(table, n, e)
		}
	}
	var data []byte
	if e = f.s.Pool.QueryRow(ctx, `SELECT manifest FROM catalog_releases WHERE id=$1`, r.ID).Scan(&data); e != nil {
		t.Fatal(e)
	}
	var manifest map[string]any
	if e = json.Unmarshal(data, &manifest); e != nil {
		t.Fatal(e)
	}
	hash, e := catalog.Hash(manifest)
	if e != nil || hash != r.ManifestHash {
		t.Fatal(hash, r.ManifestHash, e)
	}
	if _, e = f.s.Pool.Exec(ctx, `UPDATE catalog_releases SET manifest_hash='changed' WHERE id=$1`, r.ID); e == nil {
		t.Fatal("release mutation accepted")
	}
	if _, e = f.s.Pool.Exec(ctx, `DELETE FROM catalog_versions WHERE project_id=$1`, f.project); e == nil {
		t.Fatal("version deletion accepted")
	}
	edited := publishable()
	edited.Trigger = catalogString("A changed trigger")
	newDraft, e := f.svc.SaveDraft(ctx, f.owner, f.project, "lesson_opened", d.Revision, edited)
	if e != nil {
		t.Fatal(e)
	}
	// A replay still returns the immutable original after its source draft changes.
	if got, e := f.svc.CreateRelease(ctx, f.owner, f.project, "build-1", in); e != nil || got != r {
		t.Fatal(got, e)
	}
	_, e = f.svc.CreateRelease(ctx, f.owner, f.project, "stale", in)
	faultCode(t, e, "DRAFT_REVISION_CONFLICT")
	newRelease, e := f.svc.CreateRelease(ctx, f.owner, f.project, "build-2", releaseInput("lesson_opened", newDraft.Revision))
	if e != nil || newRelease.Revision != 2 || newRelease.IngestionReady {
		t.Fatal(newRelease, e)
	}
	result, e := f.svc.ValidateSample(ctx, f.viewer, f.project, "lesson_opened", newDraft.Revision, map[string]any{"lesson_id": 1})
	if e != nil || result.Compatibility != catalog.CompatibilityCompatible || !result.Valid {
		t.Fatal(result, e)
	}
	var active, metadata int64
	if e = f.s.Pool.QueryRow(ctx, `SELECT active_revision,metadata_revision FROM catalog_environments WHERE project_id=$1 AND environment='development'`, f.project).Scan(&active, &metadata); e != nil || active != 0 || metadata != 2 {
		t.Fatal(active, metadata, e)
	}
	prod := releaseInput("lesson_opened", newDraft.Revision)
	prod.Environment = "production"
	if got, e := f.svc.CreateRelease(ctx, f.owner, f.project, "build-2", prod); e != nil || got.Revision != 1 {
		t.Fatal(got, e)
	}
	list, e := f.svc.ListReleases(ctx, f.viewer, f.project, "development", "", 1)
	if e != nil || len(list) != 2 {
		t.Fatal(list, e)
	}
	next, e := f.svc.ListReleases(ctx, f.viewer, f.project, "development", list[0].ID, 1)
	if e != nil || len(next) != 1 || next[0].ID != list[1].ID {
		t.Fatal(next, e)
	}
	events, e := f.svc.ListEvents(ctx, f.viewer, f.project, "production", "", 10)
	if e != nil || len(events) != 1 || len(events[0].PublishedVersions) != 1 || events[0].PublishedVersions[0] != 2 {
		t.Fatal(events, e)
	}
}
func TestConcurrentCatalogBuildsAndAtomicIdempotency(t *testing.T) {
	f := catalogDB(t)
	ctx := context.Background()
	d := f.event(t, "lesson_opened")
	in := releaseInput("lesson_opened", d.Revision)
	var wg sync.WaitGroup
	results := make(chan catalog.Release, 12)
	for i := 0; i < 12; i++ {
		wg.Go(func() {
			r, e := f.svc.CreateRelease(ctx, f.owner, f.project, "same-key", in)
			if e != nil {
				t.Error(e)
			} else {
				results <- r
			}
		})
	}
	wg.Wait()
	close(results)
	var same string
	for r := range results {
		if same != "" && same != r.ID {
			t.Fatal("idempotency raced")
		}
		same = r.ID
	}
	revisions := make(chan int64, 8)
	for i := 0; i < 8; i++ {
		wg.Go(func() {
			r, e := f.svc.CreateRelease(ctx, f.owner, f.project, ids.New(), in)
			if e != nil {
				t.Error(e)
			} else {
				revisions <- r.Revision
			}
		})
	}
	wg.Wait()
	close(revisions)
	var got []int
	for r := range revisions {
		got = append(got, int(r))
	}
	sort.Ints(got)
	if len(got) != 8 {
		t.Fatal(got)
	}
	for i, r := range got {
		if r != i+2 {
			t.Fatal("non-monotonic revisions", got)
		}
	}
	var n int
	if e := f.s.Pool.QueryRow(ctx, `SELECT count(*) FROM catalog_versions`).Scan(&n); e != nil || n != 1 {
		t.Fatal("duplicated event versions", n, e)
	}
	// Conflicting bodies for one scoped key serialize; the winner's response is
	// persisted and no request can replace it with a different release.
	secondDraft := f.event(t, "lesson_completed")
	second := releaseInput("lesson_completed", secondDraft.Revision)
	var conflicts atomic.Int32
	var successes atomic.Int32
	for _, request := range []catalog.ReleaseInput{in, second} {
		wg.Go(func() {
			_, e := f.svc.CreateRelease(ctx, f.owner, f.project, "conflict-key", request)
			if e == nil {
				successes.Add(1)
			} else {
				faultCode(t, e, "IDEMPOTENCY_CONFLICT")
				conflicts.Add(1)
			}
		})
	}
	wg.Wait()
	if successes.Load() != 1 || conflicts.Load() != 1 {
		t.Fatal(successes.Load(), conflicts.Load())
	}
	changed := in
	changed.ExpectedActiveRevision = 1
	_, e := f.svc.CreateRelease(ctx, f.owner, f.project, "conflict-key", changed)
	faultCode(t, e, "IDEMPOTENCY_CONFLICT")
}
func TestCatalogPublicationRollback(t *testing.T) {
	f := catalogDB(t)
	ctx := context.Background()
	a := f.event(t, "alpha")
	b := f.event(t, "beta")
	stale := catalog.ReleaseInput{Environment: "development", Events: []catalog.ReleaseEvent{{Key: "alpha", DraftRevision: a.Revision}, {Key: "beta", DraftRevision: b.Revision + 1}}}
	_, e := f.svc.CreateRelease(ctx, f.owner, f.project, "failed", stale)
	faultCode(t, e, "DRAFT_REVISION_CONFLICT")
	assertEmpty := func() {
		t.Helper()
		for _, table := range []string{"catalog_releases", "catalog_versions", "catalog_release_events", "catalog_release_delivery", "catalog_idempotency", "catalog_outbox", "catalog_environments"} {
			var n int
			if e = f.s.Pool.QueryRow(ctx, "SELECT count(*) FROM "+table).Scan(&n); e != nil || n != 0 {
				t.Fatal("partial publication", table, n, e)
			}
		}
	}
	assertEmpty()
	// Inject a genuine database failure after release/version/idempotency/audit
	// insertion, proving the complete transaction rolls back at its final write.
	if _, e = f.s.Pool.Exec(ctx, `CREATE FUNCTION fail_catalog_outbox() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'synthetic dependency failure'; END $$; CREATE TRIGGER fail_outbox BEFORE INSERT ON catalog_outbox FOR EACH ROW EXECUTE FUNCTION fail_catalog_outbox()`); e != nil {
		t.Fatal(e)
	}
	in := releaseInput("alpha", a.Revision)
	_, e = f.svc.CreateRelease(ctx, f.owner, f.project, "failed", in)
	if e == nil {
		t.Fatal("outbox failure hidden")
	}
	assertEmpty()
	var audit int
	if e = f.s.Pool.QueryRow(ctx, `SELECT count(*) FROM audit_entries WHERE action='catalog.release_built'`).Scan(&audit); e != nil || audit != 0 {
		t.Fatal(audit, e)
	}
	if _, e = f.s.Pool.Exec(ctx, `DROP TRIGGER fail_outbox ON catalog_outbox`); e != nil {
		t.Fatal(e)
	}
	if r, e := f.svc.CreateRelease(ctx, f.owner, f.project, "failed", in); e != nil || r.Revision != 1 {
		t.Fatal("failed key poisoned replay", r, e)
	}
}

func TestCatalogReleaseMembershipAndDeliverySeparation(t *testing.T) {
	f := catalogDB(t)
	ctx := context.Background()
	draft := f.event(t, "lesson_opened")
	r, e := f.svc.CreateRelease(ctx, f.owner, f.project, "first", releaseInput("lesson_opened", draft.Revision))
	if e != nil {
		t.Fatal(e)
	}
	// Metadata and normalized event membership cannot be rewritten or removed.
	for _, query := range []string{
		`UPDATE catalog_release_events SET schema_version=2 WHERE release_id=$1`,
		`DELETE FROM catalog_release_events WHERE release_id=$1`,
		`DELETE FROM catalog_releases WHERE id=$1`,
		`DELETE FROM catalog_release_delivery WHERE release_id=$1`,
		`UPDATE catalog_release_delivery SET environment='production' WHERE release_id=$1`,
	} {
		if _, e = f.s.Pool.Exec(ctx, query, r.ID); e == nil {
			t.Fatal("release identity mutation accepted", query)
		}
	}
	// A valid version from this project still cannot be appended to an unrelated
	// release's immutable manifest, even by a privileged direct SQL caller.
	secondDraft := f.event(t, "lesson_completed")
	if _, e = f.svc.CreateRelease(ctx, f.owner, f.project, "second", releaseInput("lesson_completed", secondDraft.Revision)); e != nil {
		t.Fatal(e)
	}
	if _, e = f.s.Pool.Exec(ctx, `INSERT INTO catalog_release_events(project_id,environment,event_key,schema_version,release_id) VALUES($1,'development','lesson_completed',1,$2)`, f.project, r.ID); e == nil {
		t.Fatal("non-manifest event appended")
	}
	if _, e = f.s.Pool.Exec(ctx, `INSERT INTO catalog_release_events(project_id,environment,event_key,schema_version,release_id) VALUES($1,'production','lesson_completed',1,$2)`, f.project, r.ID); e == nil {
		t.Fatal("cross-environment relation accepted")
	}
	if _, e = f.s.Pool.Exec(ctx, `INSERT INTO catalog_release_events(project_id,environment,event_key,schema_version,release_id) VALUES($1,'development','lesson_completed',1,$2)`, f.otherProject, r.ID); e == nil {
		t.Fatal("cross-project relation accepted")
	}
	// This synthetic operational status update proves future delivery work can
	// change status without modifying immutable metadata. It acknowledges no
	// collector and leaves ingestion_ready false.
	if _, e = f.s.Pool.Exec(ctx, `UPDATE catalog_release_delivery SET acceleration_state='building',updated_at=now() WHERE release_id=$1`, r.ID); e != nil {
		t.Fatal(e)
	}
	status, e := f.svc.GetRelease(ctx, f.viewer, f.project, r.ID)
	if e != nil || status.IngestionReady || status.AccelerationState != "building" || status.ManifestHash != r.ManifestHash || status.State != "published" {
		t.Fatal(status, e)
	}
	list, e := f.svc.ListReleases(ctx, f.viewer, f.project, "development", "", 10)
	if e != nil {
		t.Fatal(e)
	}
	found := false
	for _, item := range list {
		if item.ID == r.ID {
			found = true
			if item != status {
				t.Fatal("list did not read delivery status", item, status)
			}
		}
	}
	if !found {
		t.Fatal("release disappeared from list")
	}
	// Replaying the build cannot reset collector-owned operational status.
	replayed, e := f.svc.CreateRelease(ctx, f.owner, f.project, "first", releaseInput("lesson_opened", draft.Revision))
	if e != nil || replayed != status {
		t.Fatal(replayed, e)
	}
	var manifest []byte
	if e = f.s.Pool.QueryRow(ctx, `SELECT manifest FROM catalog_releases WHERE id=$1`, r.ID).Scan(&manifest); e != nil {
		t.Fatal(e)
	}
	var parsed map[string]any
	if e = json.Unmarshal(manifest, &parsed); e != nil {
		t.Fatal(e)
	}
	hash, e := catalog.Hash(parsed)
	if e != nil || hash != r.ManifestHash {
		t.Fatal("status update changed immutable manifest", hash, e)
	}
}

func catalogString(s string) *string { return &s }
