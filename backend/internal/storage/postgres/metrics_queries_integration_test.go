//go:build integration

package postgres

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/howtomakeaname/Evia/backend/internal/analysis"
	"github.com/howtomakeaname/Evia/backend/internal/platform/ids"
	"github.com/howtomakeaname/Evia/backend/internal/reporting"
)

func metricRunDefinition() reporting.MetricDefinition {
	return reporting.MetricDefinition{Title: "Lesson total", Description: "Synthetic metric query", EventVersions: []analysis.EventVersion{{EventKey: "lesson_opened", SchemaVersion: 1}}, Subject: analysis.SubjectUID, Aggregation: analysis.Metric{Op: "sum", Field: &analysis.Field{Kind: "property", EventKey: "lesson_opened", SchemaVersion: 1, Key: "lesson_id"}}, Unit: "number"}
}

func metricRunPublish(t *testing.T, f catalogFixture, draft reporting.MetricDraft) reporting.MetricVersion {
	t.Helper()
	ctx := context.Background()
	snapshot, e := f.s.MetricValidationSnapshot(ctx, f.owner, f.project, draft.ID, reporting.MetricValidate{DraftRevision: draft.DraftRevision, ExpectedPublishedVersion: draft.PublishedVersion})
	if e != nil {
		t.Fatal(e)
	}
	validation, e := reporting.BuildMetricValidation(snapshot)
	if e != nil {
		t.Fatal(e)
	}
	version, e := f.s.PublishMetric(ctx, f.owner, f.project, draft.ID, ids.New(), reporting.MetricPublish{DraftRevision: draft.DraftRevision, ExpectedPublishedVersion: draft.PublishedVersion, ValidationHash: validation.ValidationHash})
	if e != nil {
		t.Fatal(e)
	}
	return version
}

func metricRunFixture(t *testing.T) (catalogFixture, reporting.MetricVersion, reporting.MetricExecute) {
	t.Helper()
	f := catalogDB(t)
	ctx := context.Background()
	event := f.event(t, "lesson_opened")
	if _, e := f.svc.CreateRelease(ctx, f.owner, f.project, "metric-run-release", releaseInput("lesson_opened", event.Revision)); e != nil {
		t.Fatal(e)
	}
	draft, e := f.s.CreateMetric(ctx, f.owner, f.project, "metric-run-create", reporting.MetricCreate{Environment: "development", Definition: metricRunDefinition()})
	if e != nil {
		t.Fatal(e)
	}
	r := queryRequest(t, "trend")
	return f, metricRunPublish(t, f, draft), reporting.MetricExecute{TimeRange: r.TimeRange, Timezone: r.Timezone, Interval: analysis.IntervalHour}
}

func assertMetricRunCounts(t *testing.T, f catalogFixture, wanted int) {
	t.Helper()
	for name, sql := range map[string]string{
		"jobs":                         `SELECT count(*) FROM analysis_query_jobs`,
		"provenance and retry markers": `SELECT count(*) FROM metric_query_requests`,
		"query audit":                  `SELECT count(*) FROM audit_entries WHERE action='analysis.query_created'`,
	} {
		var count int
		if e := f.s.Pool.QueryRow(context.Background(), sql).Scan(&count); e != nil || count != wanted {
			t.Fatalf("%s: count=%d wanted=%d error=%v", name, count, wanted, e)
		}
	}
}

func TestMetricQueryConcurrentReplayAndCurrentAuthorization(t *testing.T) {
	f, v, in := metricRunFixture(t)
	ctx := context.Background()
	var wg sync.WaitGroup
	results := make(chan analysis.Job, 12)
	for i := 0; i < 12; i++ {
		wg.Go(func() {
			job, e := f.s.EnqueueMetricVersion(ctx, f.viewer, f.project, v.MetricID, v.Version, "retry", in)
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
	assertMetricRunCounts(t, f, 1)
	var fullRetryWindow bool
	if e := f.s.Pool.QueryRow(ctx, `SELECT expires_at-created_at=interval '7 days' FROM metric_query_requests WHERE query_id=$1`, jobID).Scan(&fullRetryWindow); e != nil || !fullRetryWindow {
		t.Fatal("retry window is not seven days", fullRetryWindow, e)
	}
	// Canonical execution context uses instants, independent of wire offsets.
	offset := in
	offset.TimeRange.From = in.TimeRange.From.In(time.FixedZone("fixture", 8*60*60))
	offset.TimeRange.To = in.TimeRange.To.In(time.FixedZone("fixture", 8*60*60))
	if job, e := f.s.EnqueueMetricVersion(ctx, f.viewer, f.project, v.MetricID, v.Version, "retry", offset); e != nil || job.ID != jobID {
		t.Fatal("equivalent time range failed retry", job, e)
	}
	changed := in
	changed.Timezone = "Asia/Shanghai"
	_, e := f.s.EnqueueMetricVersion(ctx, f.viewer, f.project, v.MetricID, v.Version, "retry", changed)
	faultCode(t, e, "IDEMPOTENCY_CONFLICT")
	if _, e = f.s.CancelQuery(ctx, f.viewer, f.project, jobID); e != nil {
		t.Fatal(e)
	}
	if job, e := f.s.EnqueueMetricVersion(ctx, f.viewer, f.project, v.MetricID, v.Version, "retry", in); e != nil || job.ID != jobID || job.State != analysis.JobStateCanceled {
		t.Fatal("replay did not return current job", job, e)
	}
	assertMetricRunCounts(t, f, 1)
	if job, e := f.s.EnqueueMetricVersion(ctx, f.viewer, f.project, v.MetricID, v.Version, "another", in); e != nil || job.ID == jobID {
		t.Fatal("new key did not create a job", job, e)
	}
	assertMetricRunCounts(t, f, 2)
	claim, e := f.s.ClaimQuery(ctx, "metric-run-worker", time.Minute)
	if e != nil || claim == nil {
		t.Fatal(claim, e)
	}
	if e = f.s.CompleteQuery(ctx, *claim, queryOutcome(claim.JobID, claim.Request)); e != nil {
		t.Fatal(e)
	}
	if job, e := f.s.EnqueueMetricVersion(ctx, f.viewer, f.project, v.MetricID, v.Version, "another", in); e != nil || job.ID != claim.JobID || job.State != analysis.JobStateSucceeded || job.ResultURL == "" {
		t.Fatal("replay did not expose the existing result", job, e)
	}
	assertMetricRunCounts(t, f, 2)
	_, e = f.s.EnqueueMetricVersion(ctx, f.outsider, f.project, v.MetricID, v.Version, "retry", in)
	faultCode(t, e, "PROJECT_NOT_FOUND")
	if _, e = f.s.Pool.Exec(ctx, `DELETE FROM project_members WHERE project_id=$1 AND user_id=$2`, f.project, f.viewer); e != nil {
		t.Fatal(e)
	}
	_, e = f.s.EnqueueMetricVersion(ctx, f.viewer, f.project, v.MetricID, v.Version, "retry", in)
	faultCode(t, e, "PROJECT_NOT_FOUND")
	assertMetricRunCounts(t, f, 2)
}

func TestMetricQueryExactVersionBindingScopeAndSharedQueue(t *testing.T) {
	f, first, in := metricRunFixture(t)
	ctx := context.Background()
	oldJob, e := f.s.EnqueueMetricVersion(ctx, f.viewer, f.project, first.MetricID, 1, "version-retry", in)
	if e != nil {
		t.Fatal(e)
	}
	draft, e := f.s.GetMetricDraft(ctx, f.owner, f.project, first.MetricID)
	if e != nil {
		t.Fatal(e)
	}
	draft.Definition.Title = "Updated count"
	draft.Definition.Aggregation = analysis.Metric{Op: "count"}
	draft.Definition.Unit = "event"
	draft, e = f.s.SaveMetricDraft(ctx, f.owner, f.project, first.MetricID, draft.DraftRevision, draft.Definition)
	if e != nil {
		t.Fatal(e)
	}
	second := metricRunPublish(t, f, draft)
	_, e = f.s.EnqueueMetricVersion(ctx, f.viewer, f.project, first.MetricID, second.Version, "version-retry", in)
	faultCode(t, e, "IDEMPOTENCY_CONFLICT")
	job, e := f.s.EnqueueMetricVersion(ctx, f.viewer, f.project, first.MetricID, first.Version, "old-version", in)
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
	_, e = f.s.EnqueueMetricVersion(ctx, f.owner, f.otherProject, first.MetricID, 1, "wrong-project", in)
	faultCode(t, e, "METRIC_VERSION_NOT_FOUND")
	_, e = f.s.EnqueueMetricVersion(ctx, f.viewer, f.project, first.MetricID, 99, "wrong-version", in)
	faultCode(t, e, "METRIC_VERSION_NOT_FOUND")
	invalid := in
	invalid.TimeRange.To = invalid.TimeRange.From
	_, e = f.s.EnqueueMetricVersion(ctx, f.viewer, f.project, first.MetricID, 1, "invalid-range", invalid)
	faultCode(t, e, "INVALID_TIME_RANGE")
	invalid = in
	invalid.Timezone = "Local"
	_, e = f.s.EnqueueMetricVersion(ctx, f.viewer, f.project, first.MetricID, 1, "invalid-zone", invalid)
	faultCode(t, e, "INVALID_TIMEZONE")
	assertMetricRunCounts(t, f, 2)
	// Both entry points consume the same project queue of 32 active jobs.
	for i := 0; i < 29; i++ {
		if _, e = f.s.EnqueueQuery(ctx, f.viewer, f.project, queryRequest(t, "trend")); e != nil {
			t.Fatal(e)
		}
	}
	if _, e = f.s.EnqueueMetricVersion(ctx, f.viewer, f.project, first.MetricID, second.Version, "last-slot", in); e != nil {
		t.Fatal(e)
	}
	_, e = f.s.EnqueueQuery(ctx, f.viewer, f.project, queryRequest(t, "trend"))
	faultCode(t, e, "QUERY_CONCURRENCY_LIMIT")
	_, e = f.s.EnqueueMetricVersion(ctx, f.viewer, f.project, first.MetricID, first.Version, "queue-full", in)
	faultCode(t, e, "QUERY_CONCURRENCY_LIMIT")
	if replay, e := f.s.EnqueueMetricVersion(ctx, f.viewer, f.project, first.MetricID, 1, "version-retry", in); e != nil || replay.ID != oldJob.ID {
		t.Fatal("full queue rejected a replay", replay, e)
	}
}

func TestMetricQueryRollbackIncludesAuditAndProvenance(t *testing.T) {
	for _, target := range []string{"metric_query_requests", "audit_entries"} {
		t.Run(target, func(t *testing.T) {
			f, v, in := metricRunFixture(t)
			ctx := context.Background()
			if _, e := f.s.Pool.Exec(ctx, `CREATE FUNCTION fail_metric_run() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'synthetic execution failure'; END $$; CREATE TRIGGER fail_metric_run BEFORE INSERT ON `+target+` FOR EACH ROW EXECUTE FUNCTION fail_metric_run()`); e != nil {
				t.Fatal(e)
			}
			if _, e := f.s.EnqueueMetricVersion(ctx, f.viewer, f.project, v.MetricID, 1, "recoverable", in); e == nil {
				t.Fatal("failed final write accepted")
			}
			assertMetricRunCounts(t, f, 0)
			if _, e := f.s.Pool.Exec(ctx, `DROP TRIGGER fail_metric_run ON `+target); e != nil {
				t.Fatal(e)
			}
			if _, e := f.s.EnqueueMetricVersion(ctx, f.viewer, f.project, v.MetricID, 1, "recoverable", in); e != nil {
				t.Fatal("rollback consumed key", e)
			}
			assertMetricRunCounts(t, f, 1)
		})
	}
}

func TestMetricQueryExpiryDeletionAndMetadataCleanup(t *testing.T) {
	f, v, in := metricRunFixture(t)
	ctx := context.Background()
	job, e := f.s.EnqueueMetricVersion(ctx, f.viewer, f.project, v.MetricID, 1, "expired-job", in)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = f.s.Pool.Exec(ctx, `UPDATE analysis_query_jobs SET expires_at=clock_timestamp()-interval '2 hours' WHERE id=$1`, job.ID); e != nil {
		t.Fatal(e)
	}
	_, e = f.s.EnqueueMetricVersion(ctx, f.viewer, f.project, v.MetricID, 1, "expired-job", in)
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
	if e = f.s.Pool.QueryRow(ctx, `SELECT project_id::text,environment,query_id IS NULL FROM metric_query_requests WHERE idempotency_key='expired-job'`).Scan(&project, &environment, &missingQuery); e != nil || project != f.project || environment != "development" || !missingQuery {
		t.Fatal("job deletion lost retry scope", project, environment, missingQuery, e)
	}
	_, e = f.s.EnqueueMetricVersion(ctx, f.viewer, f.project, v.MetricID, 1, "expired-job", in)
	faultCode(t, e, "QUERY_REQUEST_EXPIRED")
	deleted, e := f.s.EnqueueMetricVersion(ctx, f.viewer, f.project, v.MetricID, 1, "deleted-job", in)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = f.s.Pool.Exec(ctx, `DELETE FROM analysis_query_jobs WHERE id=$1`, deleted.ID); e != nil {
		t.Fatal(e)
	}
	_, e = f.s.EnqueueMetricVersion(ctx, f.viewer, f.project, v.MetricID, 1, "deleted-job", in)
	faultCode(t, e, "QUERY_REQUEST_EXPIRED")
	if _, e = f.s.Pool.Exec(ctx, `UPDATE metric_query_requests SET created_at=clock_timestamp()-interval '8 days',expires_at=clock_timestamp()-interval '1 day' WHERE idempotency_key='expired-job'`); e != nil {
		t.Fatal(e)
	}
	if replacement, e := f.s.EnqueueMetricVersion(ctx, f.viewer, f.project, v.MetricID, 1, "expired-job", in); e != nil || replacement.ID == job.ID {
		t.Fatal("expired metadata prevented new admission", replacement, e)
	}
	// Synthetic expired metadata proves each admission has bounded maintenance.
	if _, e = f.s.Pool.Exec(ctx, `INSERT INTO metric_query_requests(actor_id,project_id,environment,metric_id,version,idempotency_key,request_hash,created_at,expires_at) SELECT $1,$2,'development',$3,1,'old-'||n,repeat('a',64),clock_timestamp()-interval '8 days',clock_timestamp()-interval '1 day' FROM generate_series(1,1005) n`, f.viewer, f.project, v.MetricID); e != nil {
		t.Fatal(e)
	}
	if _, e = f.s.EnqueueMetricVersion(ctx, f.viewer, f.project, v.MetricID, 1, "cleanup", in); e != nil {
		t.Fatal(e)
	}
	var remaining int
	if e = f.s.Pool.QueryRow(ctx, `SELECT count(*) FROM metric_query_requests WHERE idempotency_key LIKE 'old-%'`).Scan(&remaining); e != nil || remaining != 5 {
		t.Fatal("metadata cleanup was not bounded", remaining, e)
	}
}

func TestMetricQueryProvenanceScopedForeignKeys(t *testing.T) {
	f, v, in := metricRunFixture(t)
	ctx := context.Background()
	job, e := f.s.EnqueueMetricVersion(ctx, f.viewer, f.project, v.MetricID, 1, "valid", in)
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
		if _, e = f.s.Pool.Exec(ctx, `INSERT INTO metric_query_requests(actor_id,project_id,environment,metric_id,version,idempotency_key,request_hash,query_id) VALUES($1,$2,'development',$3,1,$4,repeat('a',64),$5)`, f.viewer, f.project, v.MetricID, fmt.Sprintf("wrong-scope-%d", i), queryID); e == nil {
			t.Fatal("cross-scope provenance accepted")
		}
	}
	if _, e = f.s.Pool.Exec(ctx, `UPDATE metric_query_requests SET version=2 WHERE query_id=$1`, job.ID); e == nil {
		t.Fatal("nonexistent version provenance accepted")
	}
}

func TestMetricWholeWindowAggregatePersistsAndBindsSeparateFromDaily(t *testing.T) {
	f, d := metricFixture(t)
	ctx := context.Background()
	definition := metricDefinition()
	definition.Aggregation = analysis.Metric{Op: "unique"}
	definition.Unit = "raw_uid"
	d, e := f.s.SaveMetricDraft(ctx, f.owner, f.project, d.ID, 1, definition)
	if e != nil {
		t.Fatal(e)
	}
	v := metricRunPublish(t, f, d)
	q := queryRequest(t, "trend")
	in := reporting.MetricExecute{TimeRange: q.TimeRange, Timezone: "UTC"}
	job, e := f.s.EnqueueMetricVersion(ctx, f.viewer, f.project, v.MetricID, 1, "window", in)
	if e != nil {
		t.Fatal(e)
	}
	var data []byte
	var request analysis.Request
	if e = f.s.Pool.QueryRow(ctx, `SELECT request FROM analysis_query_jobs WHERE id=$1`, job.ID).Scan(&data); e != nil {
		t.Fatal(e)
	}
	if e = json.Unmarshal(data, &request); e != nil {
		t.Fatal(e)
	}
	if request.Interval != "" || request.Metrics[0].Op != "unique" || request.Subject != analysis.SubjectUID {
		t.Fatal(request)
	}
	plan, e := analysis.Compile("evia", "event_facts", analysis.Scope{ProjectID: f.project, Environment: "development"}, request)
	if e != nil {
		t.Fatal(e)
	}
	if strings.Contains(plan.SQL, "bucket_ms") || !strings.Contains(plan.SQL, "uniqExact") {
		t.Fatal("whole-window UV gained buckets", plan.SQL)
	}
	daily := in
	daily.Interval = analysis.IntervalDay
	_, e = f.s.EnqueueMetricVersion(ctx, f.viewer, f.project, v.MetricID, 1, "window", daily)
	faultCode(t, e, "IDEMPOTENCY_CONFLICT")
	if _, e = f.s.EnqueueMetricVersion(ctx, f.viewer, f.project, v.MetricID, 1, "daily", daily); e != nil {
		t.Fatal(e)
	}
}

func TestMetricJobsShareFourRunningBudgetWithDirectQueries(t *testing.T) {
	f, v, in := metricRunFixture(t)
	ctx := context.Background()
	for i := range 5 {
		if _, e := f.s.EnqueueMetricVersion(ctx, f.viewer, f.project, v.MetricID, 1, fmt.Sprintf("running-%d", i), in); e != nil {
			t.Fatal(e)
		}
	}
	if _, e := f.s.EnqueueQuery(ctx, f.viewer, f.project, queryRequest(t, "trend")); e != nil {
		t.Fatal(e)
	}
	for range 4 {
		claim, e := f.s.ClaimQuery(ctx, "metric-budget", time.Minute)
		if e != nil || claim == nil {
			t.Fatal(claim, e)
		}
	}
	if claim, e := f.s.ClaimQuery(ctx, "metric-budget", time.Minute); e != nil || claim != nil {
		t.Fatal("shared four-running budget exceeded", claim, e)
	}
	var running, queued int
	if e := f.s.Pool.QueryRow(ctx, `SELECT count(*) FILTER(WHERE state='running'),count(*) FILTER(WHERE state='queued') FROM analysis_query_jobs`).Scan(&running, &queued); e != nil || running != 4 || queued != 2 {
		t.Fatal(running, queued, e)
	}
}
