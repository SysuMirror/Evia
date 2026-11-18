//go:build integration

package postgres

import (
	"context"
	"encoding/json"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/howtomakeaname/Evia/backend/internal/analysis"
	"github.com/howtomakeaname/Evia/backend/internal/platform/contract"
)

func queryRequest(t *testing.T, kind string) analysis.Request {
	t.Helper()
	validator, e := contract.New()
	if e != nil {
		t.Fatal(e)
	}
	raw := `{"kind":"` + kind + `","environment":"development","time_range":{"from":"2026-11-07T10:00:00Z","to":"2026-11-07T11:00:00Z"},"timezone":"UTC"}`
	r, e := analysis.Parse([]byte(raw), validator)
	if e != nil {
		t.Fatal(e)
	}
	return r
}
func queryOutcome(id string, r analysis.Request) analysis.Outcome {
	return analysis.Outcome{Result: &analysis.Result{QueryID: id, Columns: []analysis.Column{{Key: "observed", Type: "number"}}, Rows: []map[string]any{{"observed": 0, "enabled": false, "unknown": nil}}, Metadata: analysis.Metadata{Accuracy: "exact", SampleRate: 1, Coverage: analysis.Coverage{TimeRange: r.TimeRange, State: "provisional", KnownGaps: []string{"synthetic_lifecycle_fixture"}}, GeneratedAt: time.Now().UTC()}}}
}
func TestQueryJobsAuthorizationLeasesAndCancellation(t *testing.T) {
	f := catalogDB(t)
	s := f.s
	ctx := context.Background()
	request := queryRequest(t, "trend")
	job, e := s.EnqueueQuery(ctx, f.viewer, f.project, request)
	if e != nil || job.State != analysis.JobStateQueued {
		t.Fatal(job, e)
	}
	_, e = s.GetQuery(ctx, f.outsider, f.project, job.ID)
	faultCode(t, e, "PROJECT_NOT_FOUND")
	_, e = s.GetQuery(ctx, f.owner, f.otherProject, job.ID)
	faultCode(t, e, "QUERY_NOT_FOUND")
	_, e = s.QueryResult(ctx, f.viewer, f.project, job.ID)
	faultCode(t, e, "QUERY_RESULT_NOT_READY")
	claim, e := s.ClaimQuery(ctx, "worker-one", time.Second)
	if e != nil || claim == nil || claim.JobID != job.ID || claim.Generation != 1 {
		t.Fatal(claim, e)
	}
	if _, e = s.Pool.Exec(ctx, `UPDATE analysis_query_jobs SET lease_until=clock_timestamp()-interval '1 second' WHERE id=$1`, job.ID); e != nil {
		t.Fatal(e)
	}
	reclaimed, e := s.ClaimQuery(ctx, "worker-two", time.Minute)
	if e != nil || reclaimed == nil || reclaimed.Generation != 2 {
		t.Fatal(reclaimed, e)
	}
	faultCode(t, s.CompleteQuery(ctx, *claim, queryOutcome(job.ID, request)), "QUERY_LEASE_LOST")
	if e = s.CompleteQuery(ctx, *reclaimed, queryOutcome(job.ID, request)); e != nil {
		t.Fatal(e)
	}
	result, e := s.QueryResult(ctx, f.viewer, f.project, job.ID)
	if e != nil || len(result.Rows) != 1 || result.Rows[0]["enabled"] != false || result.Rows[0]["observed"] != json.Number("0") || result.Rows[0]["unknown"] != nil {
		t.Fatal(result, e)
	}
	canceled, e := s.CancelQuery(ctx, f.viewer, f.project, job.ID)
	if e != nil || canceled.State != analysis.JobStateSucceeded {
		t.Fatal("terminal result changed by cancellation", canceled, e)
	}
	next, e := s.EnqueueQuery(ctx, f.viewer, f.project, request)
	if e != nil {
		t.Fatal(e)
	}
	running, e := s.ClaimQuery(ctx, "worker-three", time.Minute)
	if e != nil || running == nil {
		t.Fatal(running, e)
	}
	canceled, e = s.CancelQuery(ctx, f.viewer, f.project, next.ID)
	if e != nil || canceled.State != analysis.JobStateCanceled {
		t.Fatal(canceled, e)
	}
	if valid, e := s.QueryLeaseValid(ctx, *running); e != nil || valid {
		t.Fatal("canceled query retained execution lease", valid, e)
	}
	faultCode(t, s.CompleteQuery(ctx, *running, queryOutcome(next.ID, request)), "QUERY_LEASE_LOST")
	if _, e = s.Pool.Exec(ctx, `UPDATE analysis_query_jobs SET result_expires_at=clock_timestamp()-interval '1 second' WHERE id=$1`, job.ID); e != nil {
		t.Fatal(e)
	}
	expired, e := s.GetQuery(ctx, f.viewer, f.project, job.ID)
	if e != nil || expired.State != analysis.JobStateExpired {
		t.Fatal(expired, e)
	}
	_, e = s.QueryResult(ctx, f.viewer, f.project, job.ID)
	faultCode(t, e, "QUERY_EXPIRED")
}
func TestQueryDetailPermissionAndRevocation(t *testing.T) {
	f := catalogDB(t)
	s := f.s
	ctx := context.Background()
	details := queryRequest(t, "events")
	_, e := s.EnqueueQuery(ctx, f.viewer, f.project, details)
	faultCode(t, e, "PROJECT_FORBIDDEN")
	job, e := s.EnqueueQuery(ctx, f.owner, f.project, details)
	if e != nil {
		t.Fatal(e)
	}
	claim, e := s.ClaimQuery(ctx, "worker", time.Minute)
	if e != nil || claim == nil {
		t.Fatal(claim, e)
	}
	if e = s.CompleteQuery(ctx, *claim, queryOutcome(job.ID, details)); e != nil {
		t.Fatal(e)
	}
	_, e = s.QueryResult(ctx, f.viewer, f.project, job.ID)
	faultCode(t, e, "QUERY_DETAIL_FORBIDDEN")
	if _, e = s.Pool.Exec(ctx, `UPDATE project_members SET role='viewer' WHERE project_id=$1 AND user_id=$2`, f.project, f.owner); e != nil {
		t.Fatal(e)
	}
	_, e = s.QueryResult(ctx, f.owner, f.project, job.ID)
	faultCode(t, e, "QUERY_DETAIL_FORBIDDEN")
	aggregate := queryRequest(t, "trend")
	job, e = s.EnqueueQuery(ctx, f.viewer, f.project, aggregate)
	if e != nil {
		t.Fatal(e)
	}
	claim, e = s.ClaimQuery(ctx, "worker", time.Minute)
	if e != nil || claim == nil {
		t.Fatal(claim, e)
	}
	if _, e = s.Pool.Exec(ctx, `DELETE FROM project_members WHERE project_id=$1 AND user_id=$2`, f.project, f.viewer); e != nil {
		t.Fatal(e)
	}
	if valid, e := s.QueryLeaseValid(ctx, *claim); e != nil || valid {
		t.Fatal(valid, e)
	}
	faultCode(t, s.CompleteQuery(ctx, *claim, queryOutcome(job.ID, aggregate)), "QUERY_LEASE_LOST")
	var state string
	if e = s.Pool.QueryRow(ctx, `SELECT state FROM analysis_query_jobs WHERE id=$1`, job.ID).Scan(&state); e != nil || state != analysis.JobStateCanceled {
		t.Fatal("revoked query was not canceled", state, e)
	}
}
func TestQueryAdmissionAndConcurrentExecutionBounds(t *testing.T) {
	f := catalogDB(t)
	s := f.s
	ctx := context.Background()
	request := queryRequest(t, "trend")
	var admitted atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 40; i++ {
		wg.Go(func() {
			_, e := s.EnqueueQuery(ctx, f.viewer, f.project, request)
			if e == nil {
				admitted.Add(1)
			} else {
				faultCode(t, e, "QUERY_CONCURRENCY_LIMIT")
			}
		})
	}
	wg.Wait()
	if admitted.Load() != 32 {
		t.Fatal("queue budget not enforced atomically", admitted.Load())
	}
	for i := 0; i < 20; i++ {
		wg.Go(func() {
			if _, e := s.ClaimQuery(ctx, "parallel-worker", time.Minute); e != nil {
				t.Error(e)
			}
		})
	}
	wg.Wait()
	for i := 0; i < 4; i++ {
		if _, e := s.ClaimQuery(ctx, "fill-worker", time.Minute); e != nil {
			t.Fatal(e)
		}
	}
	var running int
	if e := s.Pool.QueryRow(ctx, `SELECT count(*) FROM analysis_query_jobs WHERE state='running'`).Scan(&running); e != nil || running != 4 {
		t.Fatal("execution concurrency exceeded or not fillable", running, e)
	}
	if claim, e := s.ClaimQuery(ctx, "excess-worker", time.Minute); e != nil || claim != nil {
		t.Fatal("fifth project query started", claim, e)
	}
}
func TestQueryPropertyResolutionAndUnpublishedVersion(t *testing.T) {
	f := catalogDB(t)
	ctx := context.Background()
	draft := f.event(t, "lesson_opened")
	request := queryRequest(t, "trend")
	request.Metrics = []analysis.Metric{{Op: "sum", Field: &analysis.Field{Kind: "property", EventKey: "lesson_opened", SchemaVersion: 1, Key: "lesson_id"}, Accuracy: "exact"}}
	_, e := f.s.EnqueueQuery(ctx, f.viewer, f.project, request)
	faultCode(t, e, "QUERY_EVENT_VERSION_NOT_PUBLISHED")
	if _, e = f.svc.CreateRelease(ctx, f.owner, f.project, "definition", releaseInput("lesson_opened", draft.Revision)); e != nil {
		t.Fatal(e)
	}
	if _, e = f.s.EnqueueQuery(ctx, f.viewer, f.project, request); e != nil {
		t.Fatal(e)
	}
	claim, e := f.s.ClaimQuery(ctx, "worker", time.Minute)
	if e != nil || claim == nil || claim.Request.Metrics[0].Field.ResolvedType != "integer" {
		t.Fatal("published property type not resolved", claim, e)
	}
	request.Metrics[0].Field.Key = "unknown"
	_, e = f.s.EnqueueQuery(ctx, f.viewer, f.project, request)
	faultCode(t, e, "QUERY_PROPERTY_NOT_DEFINED")
	request.Metrics[0].Field.Key = "lesson_id"
	request.Environment = "production"
	_, e = f.s.EnqueueQuery(ctx, f.viewer, f.project, request)
	faultCode(t, e, "QUERY_EVENT_VERSION_NOT_PUBLISHED")
}

func TestQueryCompletionAuditFailureRollsBackResult(t *testing.T) {
	f := catalogDB(t)
	ctx := context.Background()
	r := queryRequest(t, "trend")
	job, e := f.s.EnqueueQuery(ctx, f.viewer, f.project, r)
	if e != nil {
		t.Fatal(e)
	}
	claim, e := f.s.ClaimQuery(ctx, "worker", time.Minute)
	if e != nil || claim == nil {
		t.Fatal(claim, e)
	}
	if _, e = f.s.Pool.Exec(ctx, `CREATE FUNCTION fail_query_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action='analysis.query_completed' THEN RAISE EXCEPTION 'synthetic audit failure'; END IF; RETURN NEW; END $$; CREATE TRIGGER fail_query_audit BEFORE INSERT ON audit_entries FOR EACH ROW EXECUTE FUNCTION fail_query_audit()`); e != nil {
		t.Fatal(e)
	}
	if e = f.s.CompleteQuery(ctx, *claim, queryOutcome(job.ID, r)); e == nil {
		t.Fatal("audit failure hidden")
	}
	var state string
	var noResult bool
	if e = f.s.Pool.QueryRow(ctx, `SELECT state,result_json IS NULL FROM analysis_query_jobs WHERE id=$1`, job.ID).Scan(&state, &noResult); e != nil || state != analysis.JobStateRunning || !noResult {
		t.Fatal("result escaped failed transaction", state, noResult, e)
	}
	if _, e = f.s.Pool.Exec(ctx, `DROP TRIGGER fail_query_audit ON audit_entries`); e != nil {
		t.Fatal(e)
	}
	if e = f.s.CompleteQuery(ctx, *claim, queryOutcome(job.ID, r)); e != nil {
		t.Fatal("same lease could not recover after rollback", e)
	}
}
