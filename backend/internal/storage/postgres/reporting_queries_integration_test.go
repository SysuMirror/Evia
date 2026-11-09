//go:build integration

package postgres

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/howtomakeaname/Evia/backend/internal/access"
	"github.com/howtomakeaname/Evia/backend/internal/analysis"
	"github.com/howtomakeaname/Evia/backend/internal/platform/ids"
	"github.com/howtomakeaname/Evia/backend/internal/reporting"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func savedRunDefinition() reporting.SavedAnalysisDefinition {
	return reporting.SavedAnalysisDefinition{
		Title: "Lesson total", Description: "Synthetic saved query",
		Query:         analysis.Template{Kind: "trend", EventVersions: []analysis.EventVersion{{EventKey: "lesson_opened", SchemaVersion: 1}}, Metrics: []analysis.Metric{{Op: "sum", Field: &analysis.Field{Kind: "property", EventKey: "lesson_opened", SchemaVersion: 1, Key: "lesson_id"}}}},
		Visualization: reporting.SavedAnalysisVisualization{Kind: "metric"},
	}
}

func savedRunPublish(t *testing.T, f catalogFixture, draft reporting.SavedAnalysisDraft) reporting.SavedAnalysisVersion {
	t.Helper()
	ctx := context.Background()
	snapshot, e := f.s.SavedAnalysisValidationSnapshot(ctx, f.owner, f.project, draft.ID, reporting.SavedAnalysisValidate{DraftRevision: draft.DraftRevision, ExpectedPublishedVersion: draft.PublishedVersion})
	if e != nil {
		t.Fatal(e)
	}
	validation, e := reporting.BuildSavedAnalysisValidation(snapshot)
	if e != nil {
		t.Fatal(e)
	}
	version, e := f.s.PublishSavedAnalysis(ctx, f.owner, f.project, draft.ID, ids.New(), reporting.SavedAnalysisPublish{DraftRevision: draft.DraftRevision, ExpectedPublishedVersion: draft.PublishedVersion, ValidationHash: validation.ValidationHash})
	if e != nil {
		t.Fatal(e)
	}
	return version
}

func savedRunFixture(t *testing.T) (catalogFixture, reporting.SavedAnalysisVersion, reporting.SavedAnalysisExecute) {
	t.Helper()
	f := catalogDB(t)
	ctx := context.Background()
	event := f.event(t, "lesson_opened")
	if _, e := f.svc.CreateRelease(ctx, f.owner, f.project, "saved-run-release", releaseInput("lesson_opened", event.Revision)); e != nil {
		t.Fatal(e)
	}
	draft, e := f.s.CreateSavedAnalysis(ctx, f.owner, f.project, "saved-run-create", reporting.SavedAnalysisCreate{Environment: "development", Definition: savedRunDefinition()})
	if e != nil {
		t.Fatal(e)
	}
	r := queryRequest(t, "trend")
	return f, savedRunPublish(t, f, draft), reporting.SavedAnalysisExecute{TimeRange: r.TimeRange, Timezone: r.Timezone}
}

func assertSavedRunCounts(t *testing.T, f catalogFixture, wanted int) {
	t.Helper()
	for name, sql := range map[string]string{
		"jobs":                         `SELECT count(*) FROM analysis_query_jobs`,
		"provenance and retry markers": `SELECT count(*) FROM saved_analysis_query_requests`,
		"query audit":                  `SELECT count(*) FROM audit_entries WHERE action='analysis.query_created'`,
	} {
		var count int
		if e := f.s.Pool.QueryRow(context.Background(), sql).Scan(&count); e != nil || count != wanted {
			t.Fatalf("%s: count=%d wanted=%d error=%v", name, count, wanted, e)
		}
	}
}

func TestSavedAnalysisQueryConcurrentReplayAndCurrentAuthorization(t *testing.T) {
	f, v, in := savedRunFixture(t)
	ctx := context.Background()
	var wg sync.WaitGroup
	results := make(chan analysis.Job, 12)
	for i := 0; i < 12; i++ {
		wg.Go(func() {
			job, e := f.s.EnqueueSavedAnalysisVersion(ctx, f.viewer, f.project, v.AnalysisID, v.Version, "retry", in)
			if e != nil {
				t.Error(e)
				return
			}
			results <- job
		})
	}
	wg.Wait()
	close(results)
	var jobID string
	for job := range results {
		if jobID != "" && jobID != job.ID {
			t.Fatal("concurrent retry created another job")
		}
		jobID = job.ID
	}
	if jobID == "" {
		t.Fatal("no execution accepted")
	}
	assertSavedRunCounts(t, f, 1)
	var fullRetryWindow bool
	if e := f.s.Pool.QueryRow(ctx, `SELECT expires_at-created_at=interval '7 days' FROM saved_analysis_query_requests WHERE query_id=$1`, jobID).Scan(&fullRetryWindow); e != nil || !fullRetryWindow {
		t.Fatal("retry window is not seven days", fullRetryWindow, e)
	}
	// Canonical execution context uses instants, independent of wire offsets.
	offset := in
	offset.TimeRange.From = in.TimeRange.From.In(time.FixedZone("fixture", 8*60*60))
	offset.TimeRange.To = in.TimeRange.To.In(time.FixedZone("fixture", 8*60*60))
	if job, e := f.s.EnqueueSavedAnalysisVersion(ctx, f.viewer, f.project, v.AnalysisID, v.Version, "retry", offset); e != nil || job.ID != jobID {
		t.Fatal("equivalent time range failed retry", job, e)
	}
	changed := in
	changed.Timezone = "Asia/Shanghai"
	_, e := f.s.EnqueueSavedAnalysisVersion(ctx, f.viewer, f.project, v.AnalysisID, v.Version, "retry", changed)
	faultCode(t, e, "IDEMPOTENCY_CONFLICT")
	if _, e = f.s.CancelQuery(ctx, f.viewer, f.project, jobID); e != nil {
		t.Fatal(e)
	}
	if job, e := f.s.EnqueueSavedAnalysisVersion(ctx, f.viewer, f.project, v.AnalysisID, v.Version, "retry", in); e != nil || job.ID != jobID || job.State != "canceled" {
		t.Fatal("replay did not return current job", job, e)
	}
	assertSavedRunCounts(t, f, 1)
	if job, e := f.s.EnqueueSavedAnalysisVersion(ctx, f.viewer, f.project, v.AnalysisID, v.Version, "another", in); e != nil || job.ID == jobID {
		t.Fatal("new key did not create a job", job, e)
	}
	assertSavedRunCounts(t, f, 2)
	claim, e := f.s.ClaimQuery(ctx, "saved-run-worker", time.Minute)
	if e != nil || claim == nil {
		t.Fatal(claim, e)
	}
	if e = f.s.CompleteQuery(ctx, *claim, queryOutcome(claim.JobID, claim.Request)); e != nil {
		t.Fatal(e)
	}
	if job, e := f.s.EnqueueSavedAnalysisVersion(ctx, f.viewer, f.project, v.AnalysisID, v.Version, "another", in); e != nil || job.ID != claim.JobID || job.State != "succeeded" || job.ResultURL == "" {
		t.Fatal("replay did not expose the existing result", job, e)
	}
	assertSavedRunCounts(t, f, 2)
	_, e = f.s.EnqueueSavedAnalysisVersion(ctx, f.outsider, f.project, v.AnalysisID, v.Version, "retry", in)
	faultCode(t, e, "PROJECT_NOT_FOUND")
	if _, e = f.s.Pool.Exec(ctx, `DELETE FROM project_members WHERE project_id=$1 AND user_id=$2`, f.project, f.viewer); e != nil {
		t.Fatal(e)
	}
	_, e = f.s.EnqueueSavedAnalysisVersion(ctx, f.viewer, f.project, v.AnalysisID, v.Version, "retry", in)
	faultCode(t, e, "PROJECT_NOT_FOUND")
	assertSavedRunCounts(t, f, 2)
}

func TestSavedAnalysisQueryExactVersionBindingScopeAndSharedQueue(t *testing.T) {
	f, first, in := savedRunFixture(t)
	ctx := context.Background()
	oldJob, e := f.s.EnqueueSavedAnalysisVersion(ctx, f.viewer, f.project, first.AnalysisID, 1, "version-retry", in)
	if e != nil {
		t.Fatal(e)
	}
	draft, e := f.s.GetSavedAnalysisDraft(ctx, f.owner, f.project, first.AnalysisID)
	if e != nil {
		t.Fatal(e)
	}
	draft.Definition.Title = "Updated count"
	draft.Definition.Query.Metrics = []analysis.Metric{{Op: "count"}}
	draft, e = f.s.SaveSavedAnalysisDraft(ctx, f.owner, f.project, first.AnalysisID, draft.DraftRevision, draft.Definition)
	if e != nil {
		t.Fatal(e)
	}
	second := savedRunPublish(t, f, draft)
	_, e = f.s.EnqueueSavedAnalysisVersion(ctx, f.viewer, f.project, first.AnalysisID, second.Version, "version-retry", in)
	faultCode(t, e, "IDEMPOTENCY_CONFLICT")
	job, e := f.s.EnqueueSavedAnalysisVersion(ctx, f.viewer, f.project, first.AnalysisID, first.Version, "old-version", in)
	if e != nil {
		t.Fatal(e)
	}
	var data []byte
	var bound analysis.Request
	if e = f.s.Pool.QueryRow(ctx, `SELECT request FROM analysis_query_jobs WHERE id=$1`, job.ID).Scan(&data); e != nil {
		t.Fatal(e)
	}
	if e = json.Unmarshal(data, &bound); e != nil || bound.Metrics[0].Op != "sum" || bound.Metrics[0].Field.ResolvedType != "integer" || bound.EventVersions[0].SchemaVersion != 1 || bound.Environment != "development" || !bound.TimeRange.From.Equal(in.TimeRange.From) || bound.Timezone != in.Timezone {
		t.Fatal("old snapshot or real execution context was not bound", bound, e)
	}
	_, e = f.s.EnqueueSavedAnalysisVersion(ctx, f.owner, f.otherProject, first.AnalysisID, 1, "wrong-project", in)
	faultCode(t, e, "SAVED_ANALYSIS_VERSION_NOT_FOUND")
	_, e = f.s.EnqueueSavedAnalysisVersion(ctx, f.viewer, f.project, first.AnalysisID, 99, "wrong-version", in)
	faultCode(t, e, "SAVED_ANALYSIS_VERSION_NOT_FOUND")
	invalid := in
	invalid.TimeRange.To = invalid.TimeRange.From
	_, e = f.s.EnqueueSavedAnalysisVersion(ctx, f.viewer, f.project, first.AnalysisID, 1, "invalid-range", invalid)
	faultCode(t, e, "INVALID_TIME_RANGE")
	invalid = in
	invalid.Timezone = "Local"
	_, e = f.s.EnqueueSavedAnalysisVersion(ctx, f.viewer, f.project, first.AnalysisID, 1, "invalid-zone", invalid)
	faultCode(t, e, "INVALID_TIMEZONE")
	assertSavedRunCounts(t, f, 2)
	// Both entry points consume the same project queue of 32 active jobs.
	for i := 0; i < 29; i++ {
		if _, e = f.s.EnqueueQuery(ctx, f.viewer, f.project, queryRequest(t, "trend")); e != nil {
			t.Fatal(e)
		}
	}
	if _, e = f.s.EnqueueSavedAnalysisVersion(ctx, f.viewer, f.project, first.AnalysisID, second.Version, "last-slot", in); e != nil {
		t.Fatal(e)
	}
	_, e = f.s.EnqueueQuery(ctx, f.viewer, f.project, queryRequest(t, "trend"))
	faultCode(t, e, "QUERY_CONCURRENCY_LIMIT")
	_, e = f.s.EnqueueSavedAnalysisVersion(ctx, f.viewer, f.project, first.AnalysisID, first.Version, "queue-full", in)
	faultCode(t, e, "QUERY_CONCURRENCY_LIMIT")
	if replay, e := f.s.EnqueueSavedAnalysisVersion(ctx, f.viewer, f.project, first.AnalysisID, 1, "version-retry", in); e != nil || replay.ID != oldJob.ID {
		t.Fatal("full queue rejected a replay", replay, e)
	}
}

func TestSavedAnalysisQueryRollbackIncludesAuditAndProvenance(t *testing.T) {
	for _, target := range []string{"saved_analysis_query_requests", "audit_entries"} {
		t.Run(target, func(t *testing.T) {
			f, v, in := savedRunFixture(t)
			ctx := context.Background()
			if _, e := f.s.Pool.Exec(ctx, `CREATE FUNCTION fail_saved_run() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'synthetic execution failure'; END $$; CREATE TRIGGER fail_saved_run BEFORE INSERT ON `+target+` FOR EACH ROW EXECUTE FUNCTION fail_saved_run()`); e != nil {
				t.Fatal(e)
			}
			if _, e := f.s.EnqueueSavedAnalysisVersion(ctx, f.viewer, f.project, v.AnalysisID, 1, "recoverable", in); e == nil {
				t.Fatal("failed final write accepted")
			}
			assertSavedRunCounts(t, f, 0)
			if _, e := f.s.Pool.Exec(ctx, `DROP TRIGGER fail_saved_run ON `+target); e != nil {
				t.Fatal(e)
			}
			if _, e := f.s.EnqueueSavedAnalysisVersion(ctx, f.viewer, f.project, v.AnalysisID, 1, "recoverable", in); e != nil {
				t.Fatal("rollback consumed key", e)
			}
			assertSavedRunCounts(t, f, 1)
		})
	}
}

func TestSavedAnalysisQueryExpiryDeletionAndMetadataCleanup(t *testing.T) {
	f, v, in := savedRunFixture(t)
	ctx := context.Background()
	job, e := f.s.EnqueueSavedAnalysisVersion(ctx, f.viewer, f.project, v.AnalysisID, 1, "expired-job", in)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = f.s.Pool.Exec(ctx, `UPDATE analysis_query_jobs SET expires_at=clock_timestamp()-interval '2 hours' WHERE id=$1`, job.ID); e != nil {
		t.Fatal(e)
	}
	_, e = f.s.EnqueueSavedAnalysisVersion(ctx, f.viewer, f.project, v.AnalysisID, 1, "expired-job", in)
	faultCode(t, e, "QUERY_REQUEST_EXPIRED")
	if claim, e := f.s.ClaimQuery(ctx, "expiry-cleanup-worker", time.Minute); e != nil || claim != nil {
		t.Fatal("provenance blocked existing worker cleanup", claim, e)
	}
	if _, e = f.s.GetQuery(ctx, f.viewer, f.project, job.ID); e != nil {
		faultCode(t, e, "QUERY_NOT_FOUND")
	} else {
		t.Fatal("worker did not delete expired job")
	}
	var project, environment string
	var missingQuery bool
	if e = f.s.Pool.QueryRow(ctx, `SELECT project_id::text,environment,query_id IS NULL FROM saved_analysis_query_requests WHERE idempotency_key='expired-job'`).Scan(&project, &environment, &missingQuery); e != nil || project != f.project || environment != "development" || !missingQuery {
		t.Fatal("job deletion lost retry scope", project, environment, missingQuery, e)
	}
	_, e = f.s.EnqueueSavedAnalysisVersion(ctx, f.viewer, f.project, v.AnalysisID, 1, "expired-job", in)
	faultCode(t, e, "QUERY_REQUEST_EXPIRED")
	deleted, e := f.s.EnqueueSavedAnalysisVersion(ctx, f.viewer, f.project, v.AnalysisID, 1, "deleted-job", in)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = f.s.Pool.Exec(ctx, `DELETE FROM analysis_query_jobs WHERE id=$1`, deleted.ID); e != nil {
		t.Fatal(e)
	}
	_, e = f.s.EnqueueSavedAnalysisVersion(ctx, f.viewer, f.project, v.AnalysisID, 1, "deleted-job", in)
	faultCode(t, e, "QUERY_REQUEST_EXPIRED")
	if _, e = f.s.Pool.Exec(ctx, `UPDATE saved_analysis_query_requests SET created_at=clock_timestamp()-interval '8 days',expires_at=clock_timestamp()-interval '1 day' WHERE idempotency_key='expired-job'`); e != nil {
		t.Fatal(e)
	}
	if replacement, e := f.s.EnqueueSavedAnalysisVersion(ctx, f.viewer, f.project, v.AnalysisID, 1, "expired-job", in); e != nil || replacement.ID == job.ID {
		t.Fatal("expired metadata prevented new admission", replacement, e)
	}
	// Synthetic expired metadata proves each admission has bounded maintenance.
	if _, e = f.s.Pool.Exec(ctx, `INSERT INTO saved_analysis_query_requests(actor_id,project_id,environment,analysis_id,version,idempotency_key,request_hash,created_at,expires_at) SELECT $1,$2,'development',$3,1,'old-'||n,repeat('a',64),clock_timestamp()-interval '8 days',clock_timestamp()-interval '1 day' FROM generate_series(1,1005) n`, f.viewer, f.project, v.AnalysisID); e != nil {
		t.Fatal(e)
	}
	if _, e = f.s.EnqueueSavedAnalysisVersion(ctx, f.viewer, f.project, v.AnalysisID, 1, "cleanup", in); e != nil {
		t.Fatal(e)
	}
	var remaining int
	if e = f.s.Pool.QueryRow(ctx, `SELECT count(*) FROM saved_analysis_query_requests WHERE idempotency_key LIKE 'old-%'`).Scan(&remaining); e != nil || remaining != 5 {
		t.Fatal("metadata cleanup was not bounded", remaining, e)
	}
}

func TestSavedAnalysisQueryProvenanceScopedForeignKeys(t *testing.T) {
	f, v, in := savedRunFixture(t)
	ctx := context.Background()
	job, e := f.s.EnqueueSavedAnalysisVersion(ctx, f.viewer, f.project, v.AnalysisID, 1, "valid", in)
	if e != nil {
		t.Fatal(e)
	}
	other, e := f.s.EnqueueQuery(ctx, f.owner, f.otherProject, queryRequest(t, "trend"))
	if e != nil {
		t.Fatal(e)
	}
	production := queryRequest(t, "trend")
	production.Environment = "production"
	prod, e := f.s.EnqueueQuery(ctx, f.owner, f.project, production)
	if e != nil {
		t.Fatal(e)
	}
	for i, queryID := range []string{other.ID, prod.ID} {
		if _, e = f.s.Pool.Exec(ctx, `INSERT INTO saved_analysis_query_requests(actor_id,project_id,environment,analysis_id,version,idempotency_key,request_hash,query_id) VALUES($1,$2,'development',$3,1,$4,repeat('a',64),$5)`, f.viewer, f.project, v.AnalysisID, fmt.Sprintf("wrong-scope-%d", i), queryID); e == nil {
			t.Fatal("cross-scope provenance accepted")
		}
	}
	if _, e = f.s.Pool.Exec(ctx, `UPDATE saved_analysis_query_requests SET version=2 WHERE query_id=$1`, job.ID); e == nil {
		t.Fatal("nonexistent version provenance accepted")
	}
}

func TestSavedAnalysisQueryMigrationPreservesExistingJobs(t *testing.T) {
	parent := database(t)
	ctx := context.Background()
	schema := "evia_saved_run_upgrade_" + strings.ReplaceAll(ids.New(), "-", "")
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
	defer tx.Rollback(context.Background())
	if _, e = tx.Exec(ctx, `CREATE TABLE schema_migrations(name text PRIMARY KEY,checksum text NOT NULL,applied_at timestamptz NOT NULL DEFAULT now())`); e != nil {
		t.Fatal(e)
	}
	entries, e := migrations.ReadDir("migrations")
	if e != nil {
		t.Fatal(e)
	}
	for _, entry := range entries {
		if entry.Name() >= "008_saved_analysis_queries.sql" {
			continue
		}
		data, e := migrations.ReadFile("migrations/" + entry.Name())
		if e != nil {
			t.Fatal(e)
		}
		if _, e = tx.Exec(ctx, string(data)); e != nil {
			t.Fatal(entry.Name(), e)
		}
		checksum := sha256.Sum256(data)
		if _, e = tx.Exec(ctx, `INSERT INTO schema_migrations(name,checksum) VALUES($1,$2)`, entry.Name(), hex.EncodeToString(checksum[:])); e != nil {
			t.Fatal(e)
		}
	}
	if e = tx.Commit(ctx); e != nil {
		t.Fatal(e)
	}
	actor, project := ids.New(), ids.New()
	if _, e = pool.Exec(ctx, `INSERT INTO accounts(id,username,display_name,password_hash) VALUES($1,'upgrade_actor','Synthetic','unused-test-hash')`, actor); e != nil {
		t.Fatal(e)
	}
	if e = s.CreateProject(ctx, actor, access.Project{ID: project, Name: "Upgrade", Timezone: "UTC"}); e != nil {
		t.Fatal(e)
	}
	// Seed the historical row with its original schema. Current enqueue code
	// requires later authorization columns and must not run before migration.
	requestJSON, e := json.Marshal(queryRequest(t, "trend"))
	if e != nil {
		t.Fatal(e)
	}
	legacy, e := scanQuery(pool.QueryRow(ctx, `INSERT INTO analysis_query_jobs(id,project_id,environment,actor_id,kind,request) VALUES($1,$2,'development',$3,'trend',$4) RETURNING `+queryJobColumns, ids.New(), project, actor, requestJSON), project)
	if e != nil {
		t.Fatal(e)
	}
	var oldChecksum string
	if e = pool.QueryRow(ctx, `SELECT checksum FROM schema_migrations WHERE name='005_queries.sql'`).Scan(&oldChecksum); e != nil {
		t.Fatal(e)
	}
	if e = s.Migrate(ctx); e != nil {
		t.Fatal("008 upgrade failed", e)
	}
	if e = s.Migrate(ctx); e != nil {
		t.Fatal("008 upgrade was not idempotent", e)
	}
	if e = s.CheckMigrations(ctx); e != nil {
		t.Fatal(e)
	}
	var checksum string
	if e = pool.QueryRow(ctx, `SELECT checksum FROM schema_migrations WHERE name='005_queries.sql'`).Scan(&checksum); e != nil || checksum != oldChecksum {
		t.Fatal("original migration ledger changed", e)
	}
	if job, e := s.GetQuery(ctx, actor, project, legacy.ID); e != nil || job.ID != legacy.ID || job.State != "queued" {
		t.Fatal("upgrade changed existing query", job, e)
	}
	if _, e = s.EnqueueQuery(ctx, actor, project, queryRequest(t, "trend")); e != nil {
		t.Fatal("upgraded schema rejected direct queries", e)
	}
	if _, e = pool.Exec(ctx, `DELETE FROM analysis_query_jobs WHERE id=$1`, legacy.ID); e != nil {
		t.Fatal("upgraded schema blocked old query cleanup", e)
	}
}
