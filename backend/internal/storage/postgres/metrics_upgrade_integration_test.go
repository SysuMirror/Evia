//go:build integration

package postgres

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"
	"time"

	"github.com/howtomakeaname/Evia/backend/internal/access"
	"github.com/howtomakeaname/Evia/backend/internal/catalog"
	"github.com/howtomakeaname/Evia/backend/internal/platform/ids"
	"github.com/howtomakeaname/Evia/backend/internal/reporting"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestMetricUpgradePreservesExistingImmutableFactsJobsAndGrants(t *testing.T) {
	parent := database(t)
	ctx := context.Background()
	schema := "evia_metric_upgrade_" + strings.ReplaceAll(ids.New(), "-", "")
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
	s := &Store{Pool: pool}
	tx, e := pool.Begin(ctx)
	if e != nil {
		t.Fatal(e)
	}
	defer tx.Rollback(ctx)
	if _, e = tx.Exec(ctx, `CREATE TABLE schema_migrations(name text PRIMARY KEY,checksum text NOT NULL,applied_at timestamptz NOT NULL DEFAULT now())`); e != nil {
		t.Fatal(e)
	}
	entries, e := migrations.ReadDir("migrations")
	if e != nil {
		t.Fatal(e)
	}
	for _, entry := range entries {
		// The baseline is the pre-metric (012) schema. Current saved-analysis
		// read code selects the 030 archive marker columns, which depend only on
		// saved_analyses/accounts already present here, so include that additive
		// migration in the baseline rather than waiting for the upgrade pass.
		if entry.Name() >= "012_metrics.sql" && entry.Name() != "030_saved_analysis_archive.sql" {
			continue
		}
		data, e := migrations.ReadFile("migrations/" + entry.Name())
		if e != nil {
			t.Fatal(e)
		}
		if _, e = tx.Exec(ctx, string(data)); e != nil {
			t.Fatal(entry.Name(), e)
		}
		sum := sha256.Sum256(data)
		if _, e = tx.Exec(ctx, `INSERT INTO schema_migrations(name,checksum) VALUES($1,$2)`, entry.Name(), hex.EncodeToString(sum[:])); e != nil {
			t.Fatal(e)
		}
	}
	if e = tx.Commit(ctx); e != nil {
		t.Fatal(e)
	}
	f := catalogFixture{s: s, svc: catalog.New(s), owner: ids.New(), project: ids.New()}
	if _, e = s.Pool.Exec(ctx, `INSERT INTO accounts(id,username,display_name,password_hash) VALUES($1,'metric_upgrade','Synthetic','unused-test-hash')`, f.owner); e != nil {
		t.Fatal(e)
	}
	if e = s.CreateProject(ctx, f.owner, access.Project{ID: f.project, Name: "Metric upgrade", Timezone: "UTC"}); e != nil {
		t.Fatal(e)
	}
	event := f.event(t, "lesson_opened")
	if _, e = f.svc.CreateRelease(ctx, f.owner, f.project, "upgrade-release", releaseInput("lesson_opened", event.Revision)); e != nil {
		t.Fatal(e)
	}
	d, e := s.CreateSavedAnalysis(ctx, f.owner, f.project, "upgrade-analysis", reporting.SavedAnalysisCreate{Environment: "development", Definition: savedDefinition()})
	if e != nil {
		t.Fatal(e)
	}
	savedRunPublish(t, f, d)
	m, e := access.NewMachines(s).CreatePrincipal(ctx, f.owner, f.project, "old-grants", access.MachineCreate{Name: "Existing grants", Environment: "development", Permissions: []string{"queries.run", "queries.read"}, ExpiresAt: time.Now().UTC().Add(time.Hour)})
	if e != nil {
		t.Fatal(e)
	}
	job, e := s.EnqueueQuery(ctx, f.owner, f.project, queryRequest(t, "trend"))
	if e != nil {
		t.Fatal(e)
	}
	const facts = `SELECT jsonb_build_object('catalog_versions',(SELECT jsonb_agg(to_jsonb(v) ORDER BY project_id,event_key,schema_version) FROM catalog_versions v),'releases',(SELECT jsonb_agg(to_jsonb(r) ORDER BY id) FROM catalog_releases r),'members',(SELECT jsonb_agg(to_jsonb(r) ORDER BY release_id,event_key,schema_version) FROM catalog_release_events r),'saved',(SELECT jsonb_agg(to_jsonb(v) ORDER BY analysis_id,version) FROM saved_analysis_versions v),'jobs',(SELECT jsonb_agg(to_jsonb(j) ORDER BY id) FROM analysis_query_jobs j),'machines',(SELECT jsonb_agg(to_jsonb(m) ORDER BY id) FROM machine_principals m),'credentials',(SELECT jsonb_agg(to_jsonb(c) ORDER BY id) FROM machine_credentials c))::text`
	var before, after string
	if e = pool.QueryRow(ctx, facts).Scan(&before); e != nil {
		t.Fatal(e)
	}
	if e = s.Migrate(ctx); e != nil {
		t.Fatal("012 upgrade", e)
	}
	if e = s.Migrate(ctx); e != nil {
		t.Fatal("012 replay", e)
	}
	if e = s.CheckMigrations(ctx); e != nil {
		t.Fatal(e)
	}
	if e = pool.QueryRow(ctx, facts).Scan(&after); e != nil || after != before {
		t.Fatal("012 changed existing facts, jobs or grants", e)
	}
	if _, e = s.GetQuery(ctx, f.owner, f.project, job.ID); e != nil {
		t.Fatal(e)
	}
	identity, e := access.NewMachines(s).AuthenticateMachine(ctx, m.SecretOnce)
	if e != nil {
		t.Fatal(e)
	}
	_, e = s.ListMetrics(access.WithIdentity(ctx, identity), m.Principal.ID, f.project, "development", "", 10)
	machineForbidden(t, e)
	metric, e := s.CreateMetric(ctx, f.owner, f.project, "after-upgrade", reporting.MetricCreate{Environment: "development", Definition: metricDefinition()})
	if e != nil {
		t.Fatal(e)
	}
	if v := metricRunPublish(t, f, metric); v.Version != 1 {
		t.Fatal(v)
	}
}
