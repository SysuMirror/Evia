//go:build integration

package postgres

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"

	"github.com/howtomakeaname/Evia/backend/internal/access"
	"github.com/howtomakeaname/Evia/backend/internal/catalog"
	"github.com/howtomakeaname/Evia/backend/internal/platform/ids"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// legacyCatalogDatabase uses the shared isolated database helper, then creates a
// second isolated schema containing exactly the applied 001+002 migration chain.
// No global application tables are changed by an upgrade regression test.
func legacyCatalogDatabase(t *testing.T) *Store {
	t.Helper()
	parent := database(t)
	ctx := context.Background()
	schema := "evia_catalog_upgrade_" + strings.ReplaceAll(ids.New(), "-", "")
	if _, e := parent.Pool.Exec(ctx, "CREATE SCHEMA "+pgx.Identifier{schema}.Sanitize()); e != nil {
		t.Fatal(e)
	}
	cfg := parent.Pool.Config().Copy()
	cfg.ConnConfig.RuntimeParams["search_path"] = schema
	pool, e := pgxpool.NewWithConfig(ctx, cfg)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() {
		pool.Close()
		if _, e := parent.Pool.Exec(context.Background(), "DROP SCHEMA "+pgx.Identifier{schema}.Sanitize()+" CASCADE"); e != nil {
			t.Error(e)
		}
	})
	store := &Store{Pool: pool}
	tx, e := pool.Begin(ctx)
	if e != nil {
		t.Fatal(e)
	}
	defer tx.Rollback(context.Background())
	if _, e = tx.Exec(ctx, `CREATE TABLE schema_migrations(name text PRIMARY KEY,checksum text NOT NULL,applied_at timestamptz NOT NULL DEFAULT now())`); e != nil {
		t.Fatal(e)
	}
	for _, name := range []string{"001_access.sql", "002_catalog.sql"} {
		data, e := migrations.ReadFile("migrations/" + name)
		if e != nil {
			t.Fatal(e)
		}
		if _, e = tx.Exec(ctx, string(data)); e != nil {
			t.Fatal(name, e)
		}
		checksum := sha256.Sum256(data)
		if _, e = tx.Exec(ctx, `INSERT INTO schema_migrations(name,checksum) VALUES($1,$2)`, name, hex.EncodeToString(checksum[:])); e != nil {
			t.Fatal(e)
		}
	}
	if e = tx.Commit(ctx); e != nil {
		t.Fatal(e)
	}
	return store
}

type legacyCatalog struct {
	store                                                *Store
	actor, project, releaseID, manifestHash, requestHash string
	input                                                catalog.ReleaseInput
}

func populatedLegacyCatalog(t *testing.T, manifestVersion int64) legacyCatalog {
	t.Helper()
	ctx := context.Background()
	store := legacyCatalogDatabase(t)
	legacy := legacyCatalog{store: store, actor: ids.New(), project: ids.New(), releaseID: ids.New(), input: releaseInput("lesson_opened", 2)}
	if _, e := store.Pool.Exec(ctx, `INSERT INTO accounts(id,username,display_name,password_hash) VALUES($1,'upgrade-owner','Synthetic','unused-test-hash')`, legacy.actor); e != nil {
		t.Fatal(e)
	}
	if e := store.CreateProject(ctx, legacy.actor, access.Project{ID: legacy.project, Name: "Upgrade", Timezone: "UTC"}); e != nil {
		t.Fatal(e)
	}
	svc := catalog.New(store)
	if _, e := svc.CreateEvent(ctx, legacy.actor, legacy.project, catalog.EventInput{Key: "lesson_opened", DisplayName: "Lesson opened"}); e != nil {
		t.Fatal(e)
	}
	definition := publishable()
	if _, e := svc.SaveDraft(ctx, legacy.actor, legacy.project, "lesson_opened", 1, definition); e != nil {
		t.Fatal(e)
	}
	definitionJSON, e := json.Marshal(definition)
	if e != nil {
		t.Fatal(e)
	}
	definitionHash, e := catalog.Hash(definition)
	if e != nil {
		t.Fatal(e)
	}
	var event map[string]any
	if e = json.Unmarshal(definitionJSON, &event); e != nil {
		t.Fatal(e)
	}
	event["event_key"] = "lesson_opened"
	event["schema_version"] = manifestVersion
	manifest := map[string]any{"format": "activity.event-plan.v1", "status": "published", "project_key": legacy.project, "environment": "development", "release_revision": 1, "events": []any{event}}
	manifestJSON, e := catalog.CanonicalJSON(manifest)
	if e != nil {
		t.Fatal(e)
	}
	legacy.manifestHash, e = catalog.Hash(manifest)
	if e != nil {
		t.Fatal(e)
	}
	legacy.requestHash, e = catalog.Hash(legacy.input)
	if e != nil {
		t.Fatal(e)
	}
	tx, e := store.Pool.Begin(ctx)
	if e != nil {
		t.Fatal(e)
	}
	defer tx.Rollback(context.Background())
	if _, e = tx.Exec(ctx, `INSERT INTO catalog_versions(project_id,event_key,schema_version,draft_revision,definition,definition_hash) VALUES($1,'lesson_opened',1,2,$2,$3)`, legacy.project, definitionJSON, definitionHash); e != nil {
		t.Fatal(e)
	}
	if _, e = tx.Exec(ctx, `INSERT INTO catalog_environments(project_id,environment,metadata_revision,active_revision) VALUES($1,'development',1,0)`, legacy.project); e != nil {
		t.Fatal(e)
	}
	if _, e = tx.Exec(ctx, `INSERT INTO catalog_releases(id,project_id,environment,revision,state,manifest_hash,manifest,created_by) VALUES($1,$2,'development',1,'published',$3,$4,$5)`, legacy.releaseID, legacy.project, legacy.manifestHash, manifestJSON, legacy.actor); e != nil {
		t.Fatal(e)
	}
	if _, e = tx.Exec(ctx, `INSERT INTO catalog_idempotency(actor_id,project_id,environment,operation,idempotency_key,request_hash,release_id) VALUES($1,$2,'development','release.create','legacy-build',$3,$4)`, legacy.actor, legacy.project, legacy.requestHash, legacy.releaseID); e != nil {
		t.Fatal(e)
	}
	if _, e = tx.Exec(ctx, `INSERT INTO catalog_outbox(project_id,environment,release_id,kind) VALUES($1,'development',$2,'catalog.release_built')`, legacy.project, legacy.releaseID); e != nil {
		t.Fatal(e)
	}
	if e = tx.Commit(ctx); e != nil {
		t.Fatal(e)
	}
	return legacy
}

func TestCatalogUpgradeFromPopulated002(t *testing.T) {
	legacy := populatedLegacyCatalog(t, 1)
	s := legacy.store
	ctx := context.Background()
	var previousChecksum string
	if e := s.Pool.QueryRow(ctx, `SELECT checksum FROM schema_migrations WHERE name='002_catalog.sql'`).Scan(&previousChecksum); e != nil {
		t.Fatal(e)
	}
	if e := s.Migrate(ctx); e != nil {
		t.Fatal("populated upgrade failed", e)
	}
	if e := s.Migrate(ctx); e != nil {
		t.Fatal("upgraded migration replay failed", e)
	}
	if e := s.CheckMigrations(ctx); e != nil {
		t.Fatal(e)
	}
	var checksum string
	if e := s.Pool.QueryRow(ctx, `SELECT checksum FROM schema_migrations WHERE name='002_catalog.sql'`).Scan(&checksum); e != nil || checksum != previousChecksum {
		t.Fatal("002 checksum changed", checksum, e)
	}
	for _, table := range []string{"catalog_releases", "catalog_versions", "catalog_release_events", "catalog_release_delivery", "catalog_idempotency", "catalog_outbox"} {
		var count int
		if e := s.Pool.QueryRow(ctx, "SELECT count(*) FROM "+table).Scan(&count); e != nil || count != 1 {
			t.Fatal("upgrade lost or duplicated rows", table, count, e)
		}
	}
	var oldColumns int
	if e := s.Pool.QueryRow(ctx, `SELECT count(*) FROM information_schema.columns WHERE table_schema=current_schema() AND table_name='catalog_releases' AND column_name IN ('ingestion_ready','acceleration_state')`).Scan(&oldColumns); e != nil || oldColumns != 0 {
		t.Fatal("legacy status columns retained", oldColumns, e)
	}
	svc := catalog.New(s)
	release, e := svc.GetRelease(ctx, legacy.actor, legacy.project, legacy.releaseID)
	if e != nil || release.ManifestHash != legacy.manifestHash || release.IngestionReady || release.AccelerationState != catalog.ReleaseAccelerationNotRequested || release.Revision != 1 {
		t.Fatal("backfilled release/status differs", release, e)
	}
	events, e := svc.ListEvents(ctx, legacy.actor, legacy.project, "development", "", 10)
	if e != nil || len(events) != 1 || len(events[0].PublishedVersions) != 1 || events[0].PublishedVersions[0] != 1 {
		t.Fatal("backfilled membership unreadable", events, e)
	}
	replay, e := svc.CreateRelease(ctx, legacy.actor, legacy.project, "legacy-build", legacy.input)
	if e != nil || replay != release {
		t.Fatal("legacy idempotency changed", replay, e)
	}
	if _, e = s.Pool.Exec(ctx, `UPDATE catalog_releases SET manifest_hash='changed' WHERE id=$1`, legacy.releaseID); e == nil {
		t.Fatal("upgrade removed metadata immutability")
	}
	if _, e = s.Pool.Exec(ctx, `UPDATE catalog_release_delivery SET acceleration_state='building' WHERE release_id=$1`, legacy.releaseID); e != nil {
		t.Fatal("delivery status not independently mutable", e)
	}
	built, e := svc.CreateRelease(ctx, legacy.actor, legacy.project, "after-upgrade", legacy.input)
	if e != nil || built.Revision != 2 || built.IngestionReady || built.AccelerationState != catalog.ReleaseAccelerationNotRequested {
		t.Fatal("publication after upgrade failed", built, e)
	}
	var active, metadata int64
	if e = s.Pool.QueryRow(ctx, `SELECT active_revision,metadata_revision FROM catalog_environments WHERE project_id=$1 AND environment='development'`, legacy.project).Scan(&active, &metadata); e != nil || active != 0 || metadata != 2 {
		t.Fatal("upgrade confused metadata/activation revisions", active, metadata, e)
	}
}

func TestCatalogUpgradeRollsBackInvalidLegacyMembership(t *testing.T) {
	legacy := populatedLegacyCatalog(t, 2)
	s := legacy.store
	ctx := context.Background()
	if e := s.Migrate(ctx); e == nil {
		t.Fatal("upgrade accepted a manifest referencing a missing event version")
	}
	var count int
	if e := s.Pool.QueryRow(ctx, `SELECT count(*) FROM schema_migrations WHERE name='003_catalog_membership_delivery.sql'`).Scan(&count); e != nil || count != 0 {
		t.Fatal("failed upgrade recorded in ledger", count, e)
	}
	var absent bool
	if e := s.Pool.QueryRow(ctx, `SELECT to_regclass('catalog_release_events') IS NULL AND to_regclass('catalog_release_delivery') IS NULL`).Scan(&absent); e != nil || !absent {
		t.Fatal("failed upgrade left partial tables", absent, e)
	}
	var ready bool
	var state, hash string
	if e := s.Pool.QueryRow(ctx, `SELECT ingestion_ready,acceleration_state,manifest_hash FROM catalog_releases WHERE id=$1`, legacy.releaseID).Scan(&ready, &state, &hash); e != nil || ready || state != catalog.ReleaseAccelerationNotRequested || hash != legacy.manifestHash {
		t.Fatal("failed upgrade changed legacy data", ready, state, hash, e)
	}
	if _, e := s.Pool.Exec(ctx, `DELETE FROM catalog_releases WHERE id=$1`, legacy.releaseID); e == nil {
		t.Fatal("failed upgrade lost existing immutability")
	}
}
