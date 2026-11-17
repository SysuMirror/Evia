//go:build integration

package postgres

import (
	"context"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/howtomakeaname/Evia/backend/internal/analysis"
	"github.com/howtomakeaname/Evia/backend/internal/platform/fault"
	"github.com/howtomakeaname/Evia/backend/internal/platform/ids"
	"github.com/howtomakeaname/Evia/backend/internal/reporting"
)

func dashboardDefinition(v reporting.SavedAnalysisVersion, n int) reporting.DashboardDefinition {
	d := reporting.DashboardDefinition{Title: "Lesson dashboard", Description: "Synthetic dashboard", Widgets: []reporting.DashboardWidget{}}
	for i := 0; i < n; i++ {
		d.Widgets = append(d.Widgets, reporting.DashboardWidget{ID: ids.New(), AnalysisID: v.AnalysisID, AnalysisVersion: v.Version, Title: "Lesson total", ReadingOrder: i, Layout: reporting.DashboardLayout{X: 0, Y: i * 2, Width: 12, Height: 2}})
	}
	return d
}
func dashboardFixture(t *testing.T, n int) (catalogFixture, reporting.DashboardDraft, reporting.DashboardExecute) {
	t.Helper()
	f, v, r := savedRunFixture(t)
	d, e := f.s.CreateDashboard(context.Background(), f.owner, f.project, "dashboard-create", reporting.DashboardCreate{Environment: "development", Definition: dashboardDefinition(v, n)})
	if e != nil {
		t.Fatal(e)
	}
	return f, d, reporting.DashboardExecute{TimeRange: r.TimeRange, Timezone: r.Timezone}
}
func dashboardValidate(t *testing.T, f catalogFixture, d reporting.DashboardDraft) reporting.DashboardValidation {
	t.Helper()
	snap, e := f.s.DashboardValidationSnapshot(context.Background(), f.viewer, f.project, d.ID, reporting.DashboardValidate{DraftRevision: d.DraftRevision, ExpectedPublishedVersion: d.PublishedVersion})
	if e != nil {
		t.Fatal(e)
	}
	v, e := reporting.BuildDashboardValidation(snap)
	if e != nil {
		t.Fatal(e)
	}
	return v
}
func dashboardPublish(t *testing.T, f catalogFixture, d reporting.DashboardDraft) reporting.DashboardVersion {
	t.Helper()
	v := dashboardValidate(t, f, d)
	out, e := f.s.PublishDashboard(context.Background(), f.owner, f.project, d.ID, ids.New(), reporting.DashboardPublish{DraftRevision: d.DraftRevision, ExpectedPublishedVersion: d.PublishedVersion, ValidationHash: v.ValidationHash})
	if e != nil {
		t.Fatal(e)
	}
	return out
}
func dashboardCounts(t *testing.T, f catalogFixture) []int64 {
	t.Helper()
	out := []int64{}
	for _, table := range []string{"dashboards", "dashboard_versions", "dashboard_dependencies", "dashboard_idempotency", "dashboard_runs", "dashboard_run_widgets", "analysis_query_jobs", "audit_entries"} {
		var n int64
		if e := f.s.Pool.QueryRow(context.Background(), "SELECT count(*) FROM "+table).Scan(&n); e != nil {
			t.Fatal(e)
		}
		out = append(out, n)
	}
	return out
}
func TestDashboardPublicationRevisionIdempotencyAndImmutability(t *testing.T) {
	f, d, _ := dashboardFixture(t, 2)
	ctx := context.Background()
	before := dashboardCounts(t, f)
	v := dashboardValidate(t, f, d)
	again := dashboardValidate(t, f, d)
	if !reflect.DeepEqual(v, again) || !reflect.DeepEqual(before, dashboardCounts(t, f)) || !v.Valid || v.ExecutionPerformed {
		t.Fatal("validation must be stable and have no side effects")
	}
	in := reporting.DashboardPublish{DraftRevision: 1, ExpectedPublishedVersion: 0, ValidationHash: v.ValidationHash}
	bad := in
	bad.ValidationHash = strings.Repeat("0", 64)
	_, e := f.s.PublishDashboard(ctx, f.owner, f.project, d.ID, "wrong", bad)
	faultCode(t, e, "VALIDATION_HASH_CONFLICT")
	one, e := f.s.PublishDashboard(ctx, f.owner, f.project, d.ID, "first", in)
	if e != nil || one.Version != 1 || len(one.Dependencies.Analyses) != 2 {
		t.Fatal(one, e)
	}
	changed := d.Definition
	changed.Title = "Changed dashboard"
	current, e := f.s.SaveDashboardDraft(ctx, f.owner, f.project, d.ID, 1, changed, "")
	if e != nil || current.DraftRevision != 2 || current.PublishedVersion != 1 {
		t.Fatal(current, e)
	}
	replay, e := f.s.PublishDashboard(ctx, f.owner, f.project, d.ID, "first", in)
	if e != nil || !savedWireEqual(one, replay) {
		t.Fatal("publication replay changed", replay, e)
	}
	_, e = f.s.SaveDashboardDraft(ctx, f.owner, f.project, d.ID, 1, changed, "")
	faultCode(t, e, "DASHBOARD_REVISION_CONFLICT")
	_, e = f.s.PublishDashboard(ctx, f.owner, f.project, d.ID, "stale", in)
	faultCode(t, e, "DASHBOARD_REVISION_CONFLICT")
	_, e = f.s.DashboardValidationSnapshot(ctx, f.viewer, f.project, d.ID, reporting.DashboardValidate{DraftRevision: 2})
	faultCode(t, e, "PUBLISHED_VERSION_CONFLICT")
	dashboardPublish(t, f, current)
	old, e := f.s.GetDashboardVersion(ctx, f.viewer, f.project, d.ID, 1)
	if e != nil || old.Definition.Title != d.Title {
		t.Fatal(old, e)
	}
	list, e := f.s.ListDashboardVersions(ctx, f.viewer, f.project, d.ID, 1, 2)
	if e != nil || len(list) != 1 || list[0].Version != 2 {
		t.Fatal(list, e)
	}
	for _, sql := range []string{`UPDATE dashboard_versions SET definition=definition WHERE dashboard_id=$1`, `DELETE FROM dashboard_versions WHERE dashboard_id=$1`, `UPDATE dashboard_dependencies SET definition_hash=definition_hash WHERE dashboard_id=$1`, `DELETE FROM dashboard_dependencies WHERE dashboard_id=$1`, `UPDATE dashboards SET environment='production' WHERE dashboard_id=$1`, `UPDATE dashboards SET published_version=1 WHERE dashboard_id=$1`, `UPDATE dashboards SET published_version=99 WHERE dashboard_id=$1`} {
		if _, e = f.s.Pool.Exec(ctx, sql, d.ID); e == nil {
			t.Fatal("immutable/scoped relation accepted mutation", sql)
		}
	}
}
func TestDashboardScopeAuthorizationAndPinnedDependencies(t *testing.T) {
	f, d, _ := dashboardFixture(t, 1)
	ctx := context.Background()
	_, e := f.s.GetDashboard(ctx, f.outsider, f.project, d.ID)
	faultCode(t, e, "PROJECT_NOT_FOUND")
	_, e = f.s.GetDashboard(ctx, f.owner, f.otherProject, d.ID)
	faultCode(t, e, "DASHBOARD_NOT_FOUND")
	_, e = f.s.CreateDashboard(ctx, f.viewer, f.project, "denied", reporting.DashboardCreate{Environment: "development", Definition: d.Definition})
	faultCode(t, e, "PROJECT_FORBIDDEN")
	_, e = f.s.SaveDashboardDraft(ctx, f.viewer, f.project, d.ID, 1, d.Definition, "")
	faultCode(t, e, "PROJECT_FORBIDDEN")
	check := dashboardValidate(t, f, d)
	_, e = f.s.PublishDashboard(ctx, f.viewer, f.project, d.ID, "denied", reporting.DashboardPublish{DraftRevision: 1, ValidationHash: check.ValidationHash})
	faultCode(t, e, "PROJECT_FORBIDDEN")
	staging, e := f.s.CreateDashboard(ctx, f.owner, f.project, "staging", reporting.DashboardCreate{Environment: "staging", Definition: d.Definition})
	if e != nil {
		t.Fatal(e)
	}
	_, e = f.s.DashboardValidationSnapshot(ctx, f.viewer, f.project, staging.ID, reporting.DashboardValidate{DraftRevision: 1})
	faultCode(t, e, "DASHBOARD_ANALYSIS_ENVIRONMENT_MISMATCH")
	listed, e := f.s.ListDashboards(ctx, f.viewer, f.project, "staging", "", 2, false, "")
	if e != nil || len(listed) != 1 || listed[0].ID != staging.ID {
		t.Fatal(listed, e)
	}
	bad := d.Definition
	bad.Widgets = append([]reporting.DashboardWidget(nil), bad.Widgets...)
	bad.Widgets[0].AnalysisVersion = 99
	saved, e := f.s.SaveDashboardDraft(ctx, f.owner, f.project, d.ID, 1, bad, "")
	if e != nil {
		t.Fatal(e)
	}
	_, e = f.s.DashboardValidationSnapshot(ctx, f.viewer, f.project, d.ID, reporting.DashboardValidate{DraftRevision: saved.DraftRevision})
	faultCode(t, e, "SAVED_ANALYSIS_VERSION_NOT_FOUND")
	if _, e = f.s.Pool.Exec(ctx, `UPDATE accounts SET disabled=true WHERE id=$1`, f.viewer); e != nil {
		t.Fatal(e)
	}
	_, e = f.s.GetDashboardDraft(ctx, f.viewer, f.project, d.ID)
	faultCode(t, e, "PROJECT_NOT_FOUND")
}
func TestDashboardConcurrentDraftPublishAndRunReplay(t *testing.T) {
	f, d, in := dashboardFixture(t, 3)
	ctx := context.Background()
	var wg sync.WaitGroup
	var saved atomic.Int32
	for range 8 {
		wg.Go(func() {
			_, e := f.s.SaveDashboardDraft(ctx, f.owner, f.project, d.ID, 1, d.Definition, "")
			if e == nil {
				saved.Add(1)
			} else {
				faultCode(t, e, "DASHBOARD_REVISION_CONFLICT")
			}
		})
	}
	wg.Wait()
	if saved.Load() != 1 {
		t.Fatal("lost revision race", saved.Load())
	}
	d, e := f.s.GetDashboardDraft(ctx, f.owner, f.project, d.ID)
	if e != nil {
		t.Fatal(e)
	}
	check := dashboardValidate(t, f, d)
	pub := reporting.DashboardPublish{DraftRevision: 2, ValidationHash: check.ValidationHash}
	for range 8 {
		wg.Go(func() {
			v, e := f.s.PublishDashboard(ctx, f.owner, f.project, d.ID, "publish-retry", pub)
			if e != nil || v.Version != 1 {
				t.Error(v, e)
			}
		})
	}
	wg.Wait()
	results := make(chan reporting.DashboardRun, 12)
	for range 12 {
		wg.Go(func() {
			r, e := f.s.EnqueueDashboardRun(ctx, f.viewer, f.project, d.ID, 1, "run-retry", in)
			if e != nil {
				t.Error(e)
			} else {
				results <- r
			}
		})
	}
	wg.Wait()
	close(results)
	var first reporting.DashboardRun
	for r := range results {
		if first.ID == "" {
			first = r
		} else if !savedWireEqual(first, r) {
			t.Fatal("retry duplicated fanout", r)
		}
	}
	if first.ID == "" || len(first.Widgets) != 3 {
		t.Fatal(first)
	}
	counts := dashboardCounts(t, f)
	if counts[4] != 1 || counts[5] != 3 || counts[6] != 3 {
		t.Fatal("replay writes", counts)
	}
	offset := in
	offset.TimeRange.From = in.TimeRange.From.In(time.FixedZone("offset", 8*3600))
	offset.TimeRange.To = in.TimeRange.To.In(time.FixedZone("offset", 8*3600))
	if r, e := f.s.EnqueueDashboardRun(ctx, f.viewer, f.project, d.ID, 1, "run-retry", offset); e != nil || r.ID != first.ID {
		t.Fatal(r, e)
	}
	changed := in
	changed.Timezone = "Asia/Shanghai"
	_, e = f.s.EnqueueDashboardRun(ctx, f.viewer, f.project, d.ID, 1, "run-retry", changed)
	faultCode(t, e, "IDEMPOTENCY_CONFLICT")
	_, e = f.s.GetDashboardRun(ctx, f.owner, f.otherProject, first.ID)
	faultCode(t, e, "DASHBOARD_RUN_NOT_FOUND")
	if _, e = f.s.Pool.Exec(ctx, `DELETE FROM project_members WHERE project_id=$1 AND user_id=$2`, f.project, f.viewer); e != nil {
		t.Fatal(e)
	}
	_, e = f.s.EnqueueDashboardRun(ctx, f.viewer, f.project, d.ID, 1, "run-retry", in)
	faultCode(t, e, "PROJECT_NOT_FOUND")
	_, e = f.s.GetDashboardRun(ctx, f.viewer, f.project, first.ID)
	faultCode(t, e, "PROJECT_NOT_FOUND")
}
func TestDashboardAtomicSharedQueueAdmissionAndAuditRollback(t *testing.T) {
	f, d, in := dashboardFixture(t, 3)
	dashboardPublish(t, f, d)
	ctx := context.Background()
	request := queryRequest(t, "trend")
	for range 30 {
		if _, e := f.s.EnqueueQuery(ctx, f.viewer, f.project, request); e != nil {
			t.Fatal(e)
		}
	}
	before := dashboardCounts(t, f)
	_, e := f.s.EnqueueDashboardRun(ctx, f.viewer, f.project, d.ID, 1, "atomic", in)
	faultCode(t, e, "QUERY_CONCURRENCY_LIMIT")
	if !reflect.DeepEqual(before, dashboardCounts(t, f)) {
		t.Fatal("partial fanout escaped queue rejection", before, dashboardCounts(t, f))
	}
	if _, e = f.s.Pool.Exec(ctx, `UPDATE analysis_query_jobs SET state='canceled' WHERE state='queued'`); e != nil {
		t.Fatal(e)
	}
	if _, e = f.s.Pool.Exec(ctx, `CREATE FUNCTION reject_dashboard_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action LIKE 'dashboard.%' THEN RAISE EXCEPTION 'synthetic audit failure'; END IF; RETURN NEW; END; $$; CREATE TRIGGER reject_dashboard_audit BEFORE INSERT ON audit_entries FOR EACH ROW EXECUTE FUNCTION reject_dashboard_audit()`); e != nil {
		t.Fatal(e)
	}
	before = dashboardCounts(t, f)
	if _, e = f.s.EnqueueDashboardRun(ctx, f.viewer, f.project, d.ID, 1, "atomic", in); e == nil {
		t.Fatal("audit failure admitted fanout")
	}
	if !reflect.DeepEqual(before, dashboardCounts(t, f)) {
		t.Fatal("audit failure left query children")
	}
	if _, e = f.s.Pool.Exec(ctx, `DROP TRIGGER reject_dashboard_audit ON audit_entries`); e != nil {
		t.Fatal(e)
	}
	run, e := f.s.EnqueueDashboardRun(ctx, f.viewer, f.project, d.ID, 1, "atomic", in)
	if e != nil || len(run.Widgets) != 3 {
		t.Fatal(run, e)
	}
	// Direct, saved and dashboard execution all consume the same 32 queued budget.
	for range 29 {
		if _, e = f.s.EnqueueQuery(ctx, f.viewer, f.project, request); e != nil {
			t.Fatal(e)
		}
	}
	_, e = f.s.EnqueueQuery(ctx, f.viewer, f.project, request)
	faultCode(t, e, "QUERY_CONCURRENCY_LIMIT")
	var claims []*analysis.Claim
	for range 4 {
		claim, e := f.s.ClaimQuery(ctx, "dashboard-budget", time.Minute)
		if e != nil || claim == nil {
			t.Fatal(claim, e)
		}
		claims = append(claims, claim)
	}
	if claim, e := f.s.ClaimQuery(ctx, "dashboard-budget", time.Minute); e != nil || claim != nil {
		t.Fatal("running budget exceeded", claim, e)
	}
}
func TestDashboardMixedChildLifecycleExpiryAndDeletion(t *testing.T) {
	f, d, in := dashboardFixture(t, 3)
	dashboardPublish(t, f, d)
	ctx := context.Background()
	run, e := f.s.EnqueueDashboardRun(ctx, f.viewer, f.project, d.ID, 1, "lifecycle", in)
	if e != nil {
		t.Fatal(e)
	}
	claim, e := f.s.ClaimQuery(ctx, "dashboard-lifecycle", time.Minute)
	if e != nil || claim == nil {
		t.Fatal(claim, e)
	}
	if e = f.s.CompleteQuery(ctx, *claim, queryOutcome(claim.JobID, claim.Request)); e != nil {
		t.Fatal(e)
	}
	next, e := f.s.ClaimQuery(ctx, "dashboard-lifecycle", time.Minute)
	if e != nil || next == nil {
		t.Fatal(next, e)
	}
	if e = f.s.CompleteQuery(ctx, *next, analysis.Outcome{Error: &analysis.JobError{Code: "SYNTHETIC_FAILURE", Retryable: false}}); e != nil {
		t.Fatal(e)
	}
	for _, widget := range run.Widgets {
		if widget.Query.ID != claim.JobID && widget.Query.ID != next.JobID {
			if _, e = f.s.CancelQuery(ctx, f.viewer, f.project, widget.Query.ID); e != nil {
				t.Fatal(e)
			}
		}
	}
	mixed, e := f.s.GetDashboardRun(ctx, f.owner, f.project, run.ID)
	if e != nil || mixed.State != "partial" || mixed.PollAfterMS != nil {
		t.Fatal(mixed, e)
	}
	succeeded := 0
	failed := 0
	canceled := 0
	for _, w := range mixed.Widgets {
		switch w.Query.State {
		case "succeeded":
			succeeded++
			if w.Query.ResultURL == "" {
				t.Fatal("no shared result URL")
			}
		case "failed":
			failed++
		case "canceled":
			canceled++
		}
	}
	if succeeded != 1 || failed != 1 || canceled != 1 || !mixed.ExpiresAt.Before(run.ExpiresAt) {
		t.Fatal("mixed child results lost", mixed)
	}
	if _, e = f.s.Pool.Exec(ctx, `UPDATE analysis_query_jobs SET result_expires_at=clock_timestamp()-interval '1 second' WHERE id=$1`, claim.JobID); e != nil {
		t.Fatal(e)
	}
	_, e = f.s.EnqueueDashboardRun(ctx, f.viewer, f.project, d.ID, 1, "lifecycle", in)
	faultCode(t, e, "DASHBOARD_RUN_REQUEST_EXPIRED")
	expired, e := f.s.GetDashboardRun(ctx, f.viewer, f.project, run.ID)
	if e != nil {
		t.Fatal(e)
	}
	found := false
	for _, w := range expired.Widgets {
		if w.Query.ID == claim.JobID {
			found = w.Query.State == "expired" && w.Query.ResultURL == ""
		}
	}
	if !found {
		t.Fatal("result expiry not reflected")
	}
	if _, e = f.s.Pool.Exec(ctx, `DELETE FROM analysis_query_jobs WHERE id=$1`, claim.JobID); e != nil {
		t.Fatal("query cleanup blocked", e)
	}
	deleted, e := f.s.GetDashboardRun(ctx, f.viewer, f.project, run.ID)
	if e != nil || len(deleted.Widgets) != 3 {
		t.Fatal(deleted, e)
	}
	found = false
	for _, w := range deleted.Widgets {
		if w.Query.ID == claim.JobID {
			found = w.Query.State == "expired" && w.WidgetID != "" && w.AnalysisID != ""
		}
	}
	if !found {
		t.Fatal("deleted child identity lost")
	}
	_, e = f.s.EnqueueDashboardRun(ctx, f.viewer, f.project, d.ID, 1, "lifecycle", in)
	faultCode(t, e, "DASHBOARD_RUN_REQUEST_EXPIRED")
	if r, e := f.s.EnqueueDashboardRun(ctx, f.viewer, f.project, d.ID, 1, "new-lifecycle", in); e != nil || r.ID == run.ID {
		t.Fatal(r, e)
	}
}
func TestDashboardCancellationOwnerPolicyAndRevocation(t *testing.T) {
	f, d, in := dashboardFixture(t, 3)
	dashboardPublish(t, f, d)
	ctx := context.Background()
	owned, e := f.s.EnqueueDashboardRun(ctx, f.owner, f.project, d.ID, 1, "owner", in)
	if e != nil {
		t.Fatal(e)
	}
	_, e = f.s.CancelDashboardRun(ctx, f.viewer, f.project, owned.ID)
	faultCode(t, e, "DASHBOARD_RUN_CANCEL_FORBIDDEN")
	run, e := f.s.EnqueueDashboardRun(ctx, f.viewer, f.project, d.ID, 1, "viewer", in)
	if e != nil {
		t.Fatal(e)
	}
	var claim *analysis.Claim
	for range 4 {
		next, e := f.s.ClaimQuery(ctx, "dashboard-cancel", time.Minute)
		if e != nil || next == nil {
			t.Fatal(next, e)
		}
		if next.ActorID == f.viewer {
			claim = next
		}
	}
	if claim == nil {
		t.Fatal("no dashboard child claimed")
	}
	canceled, e := f.s.CancelDashboardRun(ctx, f.owner, f.project, run.ID)
	if e != nil || canceled.State != "canceled" {
		t.Fatal(canceled, e)
	}
	if valid, e := f.s.QueryLeaseValid(ctx, *claim); e != nil || valid {
		t.Fatal("canceled dashboard child lease remains valid", valid, e)
	}
	before := dashboardCounts(t, f)
	if _, e = f.s.CancelDashboardRun(ctx, f.viewer, f.project, run.ID); e != nil {
		t.Fatal(e)
	}
	if !reflect.DeepEqual(before, dashboardCounts(t, f)) {
		t.Fatal("terminal cancel wrote duplicate audit")
	}
	if _, e = f.s.CancelDashboardRun(ctx, f.owner, f.project, owned.ID); e != nil {
		t.Fatal(e)
	}
	revoked, e := f.s.EnqueueDashboardRun(ctx, f.viewer, f.project, d.ID, 1, "revoked", in)
	if e != nil {
		t.Fatal(e)
	}
	claim, e = f.s.ClaimQuery(ctx, "dashboard-revoked", time.Minute)
	if e != nil || claim == nil {
		t.Fatal(claim, e)
	}
	if _, e = f.s.Pool.Exec(ctx, `DELETE FROM project_members WHERE project_id=$1 AND user_id=$2`, f.project, f.viewer); e != nil {
		t.Fatal(e)
	}
	if valid, e := f.s.QueryLeaseValid(ctx, *claim); e != nil || valid {
		t.Fatal(valid, e)
	}
	state, e := f.s.GetDashboardRun(ctx, f.owner, f.project, revoked.ID)
	if e != nil {
		t.Fatal(e)
	}
	found := false
	for _, w := range state.Widgets {
		if w.Query.ID == claim.JobID {
			found = w.Query.State == "canceled" && w.Query.Error != nil && w.Query.Error.Code == "ACCESS_REVOKED"
		}
	}
	if !found {
		t.Fatal("actor revocation not reflected")
	}
}
func TestDashboardRuntimeFiltersPreservePinnedPredicateAndRetryHash(t *testing.T) {
	f, v, r := savedRunFixture(t)
	ctx := context.Background()
	saved, e := f.s.GetSavedAnalysisDraft(ctx, f.owner, f.project, v.AnalysisID)
	if e != nil {
		t.Fatal(e)
	}
	saved.Definition.Query.Filter = &analysis.Filter{Op: "gte", Field: &analysis.Field{Kind: "property", EventKey: "lesson_opened", SchemaVersion: 1, Key: "lesson_id"}, Value: 1}
	saved, e = f.s.SaveSavedAnalysisDraft(ctx, f.owner, f.project, v.AnalysisID, 1, saved.Definition)
	if e != nil {
		t.Fatal(e)
	}
	v = savedRunPublish(t, f, saved)
	d, e := f.s.CreateDashboard(ctx, f.owner, f.project, "filters", reporting.DashboardCreate{Environment: "development", Definition: dashboardDefinition(v, 2)})
	if e != nil {
		t.Fatal(e)
	}
	dashboardPublish(t, f, d)
	filter := &analysis.Filter{Op: "lte", Field: &analysis.Field{Kind: "property", EventKey: "lesson_opened", SchemaVersion: 1, Key: "lesson_id"}, Value: 10}
	in := reporting.DashboardExecute{TimeRange: r.TimeRange, Timezone: r.Timezone, WidgetFilters: []reporting.DashboardWidgetFilter{{WidgetID: d.Definition.Widgets[0].ID, Filter: filter}}}
	run, e := f.s.EnqueueDashboardRun(ctx, f.viewer, f.project, d.ID, 1, "filters-run", in)
	if e != nil {
		t.Fatal(e)
	}
	var data []byte
	if e = f.s.Pool.QueryRow(ctx, `SELECT request FROM analysis_query_jobs WHERE id=$1`, run.Widgets[0].Query.ID).Scan(&data); e != nil {
		t.Fatal(e)
	}
	var request analysis.Request
	if e = decodeSaved(data, &request); e != nil {
		t.Fatal(e)
	}
	if request.Filter == nil || request.Filter.Op != "and" || len(request.Filter.Args) != 2 || request.Filter.Args[0].Op != "gte" || request.Filter.Args[1].Op != "lte" || request.Filter.Args[1].Field.ResolvedType != "integer" {
		t.Fatal("runtime filter replaced pinned predicate", string(data))
	}
	fetched, e := f.s.GetDashboardRun(ctx, f.viewer, f.project, run.ID)
	if e != nil || !savedWireEqual(fetched.WidgetFilters, in.WidgetFilters) {
		t.Fatal(fetched, e)
	}
	changed := in
	changed.WidgetFilters = []reporting.DashboardWidgetFilter{{WidgetID: d.Definition.Widgets[0].ID, Filter: &analysis.Filter{Op: "lte", Field: filter.Field, Value: 9}}}
	_, e = f.s.EnqueueDashboardRun(ctx, f.viewer, f.project, d.ID, 1, "filters-run", changed)
	faultCode(t, e, "IDEMPOTENCY_CONFLICT")
	bad := in
	bad.WidgetFilters = []reporting.DashboardWidgetFilter{{WidgetID: ids.New(), Filter: filter}}
	before := dashboardCounts(t, f)
	_, e = f.s.EnqueueDashboardRun(ctx, f.viewer, f.project, d.ID, 1, "bad-filter", bad)
	faultCode(t, e, "INVALID_WIDGET_FILTER")
	if !reflect.DeepEqual(before, dashboardCounts(t, f)) {
		t.Fatal("unknown widget left writes")
	}
	bad.WidgetFilters = []reporting.DashboardWidgetFilter{{WidgetID: d.Definition.Widgets[1].ID, Filter: &analysis.Filter{Op: "eq", Field: filter.Field, Value: false}}}
	_, e = f.s.EnqueueDashboardRun(ctx, f.viewer, f.project, d.ID, 1, "bad-type", bad)
	faultCode(t, e, "INVALID_FILTER_TYPE")
	if !reflect.DeepEqual(before, dashboardCounts(t, f)) {
		t.Fatal("later child type rejection left writes")
	}
	// Published dashboard and dependency pins remain immutable after runtime filters.
	old, e := f.s.GetDashboardVersion(ctx, f.viewer, f.project, d.ID, 1)
	if e != nil || !savedWireEqual(old.Definition, d.Definition) {
		t.Fatal(old, e)
	}
}

func TestDashboardMutationAuditFailuresRollbackAndScopeWitnesses(t *testing.T) {
	f, d, _ := dashboardFixture(t, 2)
	ctx := context.Background()
	check := dashboardValidate(t, f, d)
	in := reporting.DashboardPublish{DraftRevision: 1, ValidationHash: check.ValidationHash}
	if _, e := f.s.Pool.Exec(ctx, `CREATE FUNCTION reject_dashboard_mutation() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action LIKE 'dashboard.%' THEN RAISE EXCEPTION 'synthetic audit failure'; END IF; RETURN NEW; END; $$; CREATE TRIGGER reject_dashboard_mutation BEFORE INSERT ON audit_entries FOR EACH ROW EXECUTE FUNCTION reject_dashboard_mutation()`); e != nil {
		t.Fatal(e)
	}
	before := dashboardCounts(t, f)
	if _, e := f.s.CreateDashboard(ctx, f.owner, f.project, "rolled-back", reporting.DashboardCreate{Environment: "development", Definition: d.Definition}); e == nil {
		t.Fatal("create ignored audit failure")
	}
	if _, e := f.s.SaveDashboardDraft(ctx, f.owner, f.project, d.ID, 1, d.Definition, ""); e == nil {
		t.Fatal("save ignored audit failure")
	}
	if _, e := f.s.PublishDashboard(ctx, f.owner, f.project, d.ID, "rolled-back", in); e == nil {
		t.Fatal("publish ignored audit failure")
	}
	if !reflect.DeepEqual(before, dashboardCounts(t, f)) {
		t.Fatal("audit rejection left partial mutations")
	}
	if _, e := f.s.Pool.Exec(ctx, `DROP TRIGGER reject_dashboard_mutation ON audit_entries`); e != nil {
		t.Fatal(e)
	}
	v, e := f.s.PublishDashboard(ctx, f.owner, f.project, d.ID, "rolled-back", in)
	if e != nil {
		t.Fatal("rolled back publication poisoned retry", e)
	}
	// Build a separate unpublished version in a transaction to exercise the hash
	// witness FK, rather than merely colliding with the existing dependency PK.
	tx, e := f.s.Pool.Begin(ctx)
	if e != nil {
		t.Fatal(e)
	}
	defer tx.Rollback(context.Background())
	if _, e = tx.Exec(ctx, `INSERT INTO dashboard_versions(project_id,dashboard_id,environment,version,draft_revision,definition,dependencies,definition_hash,dependency_hash,validation_hash,created_by) SELECT project_id,dashboard_id,environment,2,draft_revision,definition,dependencies,definition_hash,dependency_hash,validation_hash,created_by FROM dashboard_versions WHERE dashboard_id=$1`, d.ID); e != nil {
		t.Fatal(e)
	}
	if _, e = tx.Exec(ctx, `INSERT INTO dashboard_dependencies(project_id,environment,dashboard_id,version,widget_id,analysis_id,analysis_version,definition_hash,dependency_hash,validation_hash) SELECT project_id,environment,dashboard_id,2,widget_id,analysis_id,analysis_version,repeat('0',64),dependency_hash,validation_hash FROM dashboard_dependencies WHERE dashboard_id=$1 LIMIT 1`, d.ID); e == nil {
		t.Fatal("published analysis hash witness was not enforced")
	}
	tx.Rollback(ctx)
	// A complete dependency set must accompany the immutable version, even when
	// a direct SQL caller bypasses the adapter's publish operation.
	if _, e = f.s.Pool.Exec(ctx, `INSERT INTO dashboard_versions(project_id,dashboard_id,environment,version,draft_revision,definition,dependencies,definition_hash,dependency_hash,validation_hash,created_by) SELECT project_id,dashboard_id,environment,2,draft_revision,definition,dependencies,definition_hash,dependency_hash,validation_hash,created_by FROM dashboard_versions WHERE dashboard_id=$1`, d.ID); e == nil {
		t.Fatal("publication without dependency witnesses committed")
	}
	if _, e = f.s.Pool.Exec(ctx, `UPDATE dashboards SET definition=json_build_object('title',title,'description','','widgets',json_build_array(json_build_object('widget_id','invalid'))) WHERE dashboard_id=$1`, d.ID); e == nil {
		t.Fatal("malformed persisted widget accepted")
	}
	if _, e = f.s.Pool.Exec(ctx, `UPDATE dashboard_idempotency SET request_hash=request_hash WHERE project_id=$1`, f.project); e == nil {
		t.Fatal("idempotency witness was mutable")
	}
	if fetched, e := f.s.GetDashboardVersion(ctx, f.viewer, f.project, d.ID, 1); e != nil || !savedWireEqual(v, fetched) {
		t.Fatal("failed SQL changed publication", fetched, e)
	}
}

func TestDashboardVersionsRunExactPinnedAnalysisSnapshots(t *testing.T) {
	f, d, in := dashboardFixture(t, 1)
	ctx := context.Background()
	dashboardPublish(t, f, d)
	analysisID := d.Definition.Widgets[0].AnalysisID
	saved, e := f.s.GetSavedAnalysisDraft(ctx, f.owner, f.project, analysisID)
	if e != nil {
		t.Fatal(e)
	}
	saved.Definition.Title = "New saved analysis"
	saved.Definition.Query.Metrics = []analysis.Metric{{Op: "count"}}
	saved, e = f.s.SaveSavedAnalysisDraft(ctx, f.owner, f.project, analysisID, 1, saved.Definition)
	if e != nil {
		t.Fatal(e)
	}
	published := savedRunPublish(t, f, saved)
	definition := d.Definition
	definition.Widgets = append([]reporting.DashboardWidget(nil), definition.Widgets...)
	definition.Widgets[0].AnalysisVersion = published.Version
	definition.Title = "New dashboard"
	d, e = f.s.SaveDashboardDraft(ctx, f.owner, f.project, d.ID, 1, definition, "")
	if e != nil {
		t.Fatal(e)
	}
	dashboardPublish(t, f, d)
	old, e := f.s.EnqueueDashboardRun(ctx, f.viewer, f.project, d.ID, 1, "version-bound", in)
	if e != nil {
		t.Fatal(e)
	}
	_, e = f.s.EnqueueDashboardRun(ctx, f.viewer, f.project, d.ID, 2, "version-bound", in)
	faultCode(t, e, "IDEMPOTENCY_CONFLICT")
	current, e := f.s.EnqueueDashboardRun(ctx, f.viewer, f.project, d.ID, 2, "version-two", in)
	if e != nil {
		t.Fatal(e)
	}
	for _, tc := range []struct {
		run     reporting.DashboardRun
		want    string
		version int64
	}{{old, "sum", 1}, {current, "count", 2}} {
		var data []byte
		if e = f.s.Pool.QueryRow(ctx, `SELECT request FROM analysis_query_jobs WHERE id=$1`, tc.run.Widgets[0].Query.ID).Scan(&data); e != nil {
			t.Fatal(e)
		}
		var request analysis.Request
		if e = decodeSaved(data, &request); e != nil {
			t.Fatal(e)
		}
		if len(request.Metrics) != 1 || request.Metrics[0].Op != tc.want || tc.run.Widgets[0].AnalysisVersion != tc.version {
			t.Fatal("run used current mutable template", tc.run, string(data))
		}
	}
	// The same textual key belongs to each current actor's scope independently.
	owner, e := f.s.EnqueueDashboardRun(ctx, f.owner, f.project, d.ID, 1, "version-bound", in)
	if e != nil || owner.ID == old.ID {
		t.Fatal(owner, e)
	}
}

func TestDashboardRunPreservesExactNanosecondExecutionContext(t *testing.T) {
	f, d, _ := dashboardFixture(t, 1)
	dashboardPublish(t, f, d)
	ctx := context.Background()
	for _, tc := range []struct{ name, from, to string }{
		{"fractional", "2026-11-07T10:00:00.123456789Z", "2026-11-07T11:00:00.987654321Z"},
		{"submicrosecond", "2026-11-07T10:00:00.123456100Z", "2026-11-07T10:00:00.123456200Z"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			from, e := time.Parse(time.RFC3339Nano, tc.from)
			if e != nil {
				t.Fatal(e)
			}
			to, e := time.Parse(time.RFC3339Nano, tc.to)
			if e != nil {
				t.Fatal(e)
			}
			in := reporting.DashboardExecute{TimeRange: analysis.TimeRange{From: from, To: to}, Timezone: "UTC", WidgetFilters: []reporting.DashboardWidgetFilter{{WidgetID: d.Definition.Widgets[0].ID, Filter: &analysis.Filter{Op: "eq", Field: &analysis.Field{Kind: "property", EventKey: "lesson_opened", SchemaVersion: 1, Key: "lesson_id"}, Value: 4}}}}
			accepted, e := f.s.EnqueueDashboardRun(ctx, f.viewer, f.project, d.ID, 1, tc.name, in)
			if e != nil {
				t.Fatal(e)
			}
			fetched, e := f.s.GetDashboardRun(ctx, f.viewer, f.project, accepted.ID)
			if e != nil {
				t.Fatal(e)
			}
			for _, run := range []reporting.DashboardRun{accepted, fetched} {
				if !run.TimeRange.From.Equal(from) || !run.TimeRange.To.Equal(to) || run.Timezone != in.Timezone || !savedWireEqual(run.WidgetFilters, in.WidgetFilters) {
					t.Fatal("exact runtime context changed", run)
				}
			}
			var data []byte
			if e = f.s.Pool.QueryRow(ctx, `SELECT request FROM analysis_query_jobs WHERE id=$1`, fetched.Widgets[0].Query.ID).Scan(&data); e != nil {
				t.Fatal(e)
			}
			var request analysis.Request
			if e = decodeSaved(data, &request); e != nil {
				t.Fatal(e)
			}
			if !request.TimeRange.From.Equal(fetched.TimeRange.From) || !request.TimeRange.To.Equal(fetched.TimeRange.To) || request.Timezone != fetched.Timezone {
				t.Fatal("run and child execution bounds differ", fetched.TimeRange, request.TimeRange)
			}
			offset := in
			offset.TimeRange.From = from.In(time.FixedZone("fixture", 8*3600))
			offset.TimeRange.To = to.In(time.FixedZone("fixture", 8*3600))
			replay, e := f.s.EnqueueDashboardRun(ctx, f.viewer, f.project, d.ID, 1, tc.name, offset)
			if e != nil || replay.ID != fetched.ID || !replay.TimeRange.From.Equal(from) || !replay.TimeRange.To.Equal(to) || replay.Widgets[0].Query.ID != fetched.Widgets[0].Query.ID {
				t.Fatal("offset replay lost exact input or duplicated fanout", replay, e)
			}
			if e = f.s.Pool.QueryRow(ctx, `SELECT execution_input FROM dashboard_runs WHERE run_id=$1`, fetched.ID).Scan(&data); e != nil {
				t.Fatal(e)
			}
			normalized, e := reporting.NormalizeDashboardExecution(in, d.Definition)
			if e != nil {
				t.Fatal(e)
			}
			var persisted reporting.DashboardExecute
			if e = decodeSaved(data, &persisted); e != nil || !savedWireEqual(persisted, normalized) {
				t.Fatal("immutable runtime input differs", persisted, e)
			}
			if _, e = f.s.Pool.Exec(ctx, `UPDATE dashboard_runs SET execution_input=execution_input WHERE run_id=$1`, fetched.ID); e == nil {
				t.Fatal("execution input was mutable")
			}
		})
	}
}

func TestDashboardArchiveLifecycleAndMutationGuards(t *testing.T) {
	f, d, exec := dashboardFixture(t, 1)
	ctx := context.Background()
	// Publish first so run authorization can find a version; the archive guard
	// then runs before the version is materialised.
	dashboardPublish(t, f, d)
	keep, e := f.s.CreateDashboard(ctx, f.owner, f.project, "keep-dashboard", reporting.DashboardCreate{Environment: "development", Definition: d.Definition})
	if e != nil {
		t.Fatal(e)
	}

	// A read-only viewer cannot change lifecycle state.
	if _, e = f.s.ArchiveDashboard(ctx, f.viewer, f.project, d.ID); e == nil || fault.Public(e).Code != "PROJECT_FORBIDDEN" {
		t.Fatalf("viewer archive must be forbidden: %v", e)
	}

	archived, e := f.s.ArchiveDashboard(ctx, f.owner, f.project, d.ID)
	if e != nil {
		t.Fatal(e)
	}
	if archived.ArchivedAt == nil || archived.ArchivedBy != f.owner {
		t.Fatalf("archive marker missing: %+v", archived)
	}
	// Re-archiving is an explicit conflict, not a silent no-op.
	if _, e = f.s.ArchiveDashboard(ctx, f.owner, f.project, d.ID); e == nil || fault.Public(e).Code != "DASHBOARD_ALREADY_ARCHIVED" {
		t.Fatalf("re-archive must conflict: %v", e)
	}

	// The default listing hides archived dashboards but keeps active ones;
	// direct reads still work and expose the archive marker.
	listed, e := f.s.ListDashboards(ctx, f.owner, f.project, "development", "", 100, false, "")
	if e != nil {
		t.Fatal(e)
	}
	for _, item := range listed {
		if item.ID == d.ID {
			t.Fatal("archived dashboard must leave the default listing")
		}
		if item.ArchivedAt != nil {
			t.Fatal("active dashboard must not carry an archive marker")
		}
	}
	if len(listed) != 1 || listed[0].ID != keep.ID {
		t.Fatalf("listing must retain only the active dashboard: %+v", listed)
	}
	// The archived stream is opt-in and returns only archived dashboards.
	archivedList, e := f.s.ListDashboards(ctx, f.owner, f.project, "development", "", 100, true, "")
	if e != nil {
		t.Fatal(e)
	}
	if len(archivedList) != 1 || archivedList[0].ID != d.ID || archivedList[0].ArchivedAt == nil {
		t.Fatalf("archived listing must return only the archived dashboard with marker: %+v", archivedList)
	}
	got, e := f.s.GetDashboard(ctx, f.owner, f.project, d.ID)
	if e != nil || got.ArchivedAt == nil {
		t.Fatalf("archived dashboard must remain readable by ID: %+v %v", got, e)
	}
	draft, e := f.s.GetDashboardDraft(ctx, f.owner, f.project, d.ID)
	if e != nil || draft.ArchivedAt == nil {
		t.Fatalf("archived draft must remain readable with marker: %+v %v", draft, e)
	}

	// Every mutating path fails closed while archived.
	if _, e = f.s.SaveDashboardDraft(ctx, f.owner, f.project, d.ID, d.DraftRevision, d.Definition, ""); e == nil || fault.Public(e).Code != "DASHBOARD_ARCHIVED" {
		t.Fatalf("draft save must reject archived dashboard: %v", e)
	}
	if _, e = f.s.DashboardValidationSnapshot(ctx, f.owner, f.project, d.ID, reporting.DashboardValidate{DraftRevision: d.DraftRevision}); e == nil || fault.Public(e).Code != "DASHBOARD_ARCHIVED" {
		t.Fatalf("validation must reject archived dashboard: %v", e)
	}
	if _, e = f.s.PublishDashboard(ctx, f.owner, f.project, d.ID, ids.New(), reporting.DashboardPublish{DraftRevision: d.DraftRevision, ValidationHash: strings.Repeat("d", 64)}); e == nil || fault.Public(e).Code != "DASHBOARD_ARCHIVED" {
		t.Fatalf("publish must reject archived dashboard: %v", e)
	}
	_, e = f.s.EnqueueDashboardRun(ctx, f.owner, f.project, d.ID, 1, ids.New(), exec)
	if e == nil || fault.Public(e).Code != "DASHBOARD_ARCHIVED" {
		t.Fatalf("run enqueue must reject archived dashboard: %v", e)
	}

	// Unknown id resolves to not-found, not an archive conflict.
	if _, e = f.s.ArchiveDashboard(ctx, f.owner, f.project, ids.New()); e == nil || fault.Public(e).Code != "DASHBOARD_NOT_FOUND" {
		t.Fatalf("unknown dashboard must be not-found: %v", e)
	}

	// Restoring an active dashboard conflicts; restoring the archived one clears
	// the marker and returns it to the listing.
	if _, e = f.s.UnarchiveDashboard(ctx, f.owner, f.project, keep.ID); e == nil || fault.Public(e).Code != "DASHBOARD_NOT_ARCHIVED" {
		t.Fatalf("unarchiving an active dashboard must conflict: %v", e)
	}
	restored, e := f.s.UnarchiveDashboard(ctx, f.owner, f.project, d.ID)
	if e != nil {
		t.Fatal(e)
	}
	if restored.ArchivedAt != nil || restored.ArchivedBy != "" {
		t.Fatalf("restore must clear archive markers: %+v", restored)
	}
	listedAgain, e := f.s.ListDashboards(ctx, f.owner, f.project, "development", "", 100, false, "")
	if e != nil || len(listedAgain) != 2 {
		t.Fatalf("restored dashboard must rejoin the listing: %d %v", len(listedAgain), e)
	}
	restoredArchive, e := f.s.ListDashboards(ctx, f.owner, f.project, "development", "", 100, true, "")
	if e != nil || len(restoredArchive) != 0 {
		t.Fatalf("restored dashboard must leave the archived stream: %d %v", len(restoredArchive), e)
	}

	// Both lifecycle transitions are audited, ordered after the create/publish
	// entries already produced by the fixture.
	var actions []string
	rows, e := f.s.Pool.Query(ctx, `SELECT action FROM audit_entries WHERE resource_key=$1 AND action IN ('dashboard.archived','dashboard.unarchived') ORDER BY id`, d.ID)
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
	if len(actions) != 2 || actions[0] != "dashboard.archived" || actions[1] != "dashboard.unarchived" {
		t.Fatalf("lifecycle audit trail wrong: %v", actions)
	}
}
