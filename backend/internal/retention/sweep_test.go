package retention

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"
)

type fakeSweepStore struct {
	policies  []Policy
	records   []SweepRecord
	listErr   error
	recordErr error
}

func (f *fakeSweepStore) ListRetentionPolicies(context.Context) ([]Policy, error) {
	return f.policies, f.listErr
}

func (f *fakeSweepStore) RecordSweep(_ context.Context, record SweepRecord) error {
	if f.recordErr != nil {
		return f.recordErr
	}
	f.records = append(f.records, record)
	return nil
}

type fakeFactSweeper struct {
	expired     map[string]uint64
	calls       []string
	deleteErr   error
	failProject string
	failErr     error
}

func key(project, environment string) string { return project + "|" + environment }

func (f *fakeFactSweeper) CountExpired(_ context.Context, project, environment string, _ time.Time) (uint64, error) {
	f.calls = append(f.calls, "count:"+key(project, environment))
	if f.failProject != "" && project == f.failProject {
		return 0, f.failErr
	}
	return f.expired[key(project, environment)], nil
}

func (f *fakeFactSweeper) DeleteExpired(_ context.Context, project, environment string, _ time.Time) (uint64, error) {
	f.calls = append(f.calls, "delete:"+key(project, environment))
	if f.deleteErr != nil {
		return 0, f.deleteErr
	}
	return f.expired[key(project, environment)], nil
}

func testPolicy(project, environment string, days int) Policy {
	return Policy{ProjectID: project, Environment: environment, RetentionDays: days, Revision: 1}
}

func discardLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func TestPlanSweepCutoffIsWindowBeforeNow(t *testing.T) {
	now := time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)
	plan := PlanSweep(testPolicy("p", "production", 90), now)
	want := time.Date(2026, 8, 13, 12, 0, 0, 0, time.UTC)
	if !plan.Cutoff.Equal(want) {
		t.Fatalf("cutoff=%s want=%s", plan.Cutoff, want)
	}
	if plan.RetentionDays != 90 || plan.Environment != "production" {
		t.Fatalf("plan fields wrong: %+v", plan)
	}
}

func TestDryRunCountsButNeverDeletesAndRecordsSucceeded(t *testing.T) {
	store := &fakeSweepStore{policies: []Policy{testPolicy("p1", "development", 30)}}
	sweeper := &fakeFactSweeper{expired: map[string]uint64{key("p1", "development"): 7}}
	worker := NewSweepWorker(store, sweeper, false, time.Minute, discardLogger())

	results, err := worker.RunOnce(context.Background())
	if err != nil || len(results) != 1 {
		t.Fatalf("results=%v err=%v", results, err)
	}
	if results[0].Candidates != 7 || !results[0].DryRun || results[0].Err != nil {
		t.Fatalf("dry-run result wrong: %+v", results[0])
	}
	for _, call := range sweeper.calls {
		if call[:6] == "delete" {
			t.Fatal("dry run must never call DeleteExpired")
		}
	}
	if len(store.records) != 1 {
		t.Fatalf("expected one audit record, got %d", len(store.records))
	}
	record := store.records[0]
	if !record.DryRun || record.Status != SweepSucceeded || record.RowsCandidates != 7 || record.CompletedAt == nil {
		t.Fatalf("dry-run audit record wrong: %+v", record)
	}
}

func TestEnforcementDeletesAndPersistsActualCount(t *testing.T) {
	store := &fakeSweepStore{policies: []Policy{testPolicy("p1", "production", 365)}}
	sweeper := &fakeFactSweeper{expired: map[string]uint64{key("p1", "production"): 42}}
	worker := NewSweepWorker(store, sweeper, true, time.Minute, discardLogger())

	results, err := worker.RunOnce(context.Background())
	if err != nil || results[0].Err != nil || results[0].Candidates != 42 || results[0].DryRun {
		t.Fatalf("enforce result wrong: %+v err=%v", results, err)
	}
	foundDelete := false
	for _, call := range sweeper.calls {
		if call == "delete:"+key("p1", "production") {
			foundDelete = true
		}
	}
	if !foundDelete {
		t.Fatal("enforcement must call DeleteExpired")
	}
	record := store.records[0]
	if record.DryRun || record.Status != SweepSucceeded || record.RowsCandidates != 42 {
		t.Fatalf("enforce audit record wrong: %+v", record)
	}
}

func TestSingleScopeFailureIsRecordedAndDoesNotAbortOthers(t *testing.T) {
	store := &fakeSweepStore{policies: []Policy{
		testPolicy("p-bad", "development", 30),
		testPolicy("p-good", "staging", 60),
	}}
	sweeper := &fakeFactSweeper{
		expired:     map[string]uint64{key("p-good", "staging"): 3},
		failProject: "p-bad",
		failErr:     errors.New("clickhouse unavailable"),
	}
	worker := NewSweepWorker(store, sweeper, false, time.Minute, discardLogger())

	results, err := worker.RunOnce(context.Background())
	if err != nil || len(results) != 2 {
		t.Fatalf("results=%v err=%v", results, err)
	}
	if results[0].Err == nil || results[1].Err != nil || results[1].Candidates != 3 {
		t.Fatalf("expected first failure and second success, got %+v", results)
	}
	if len(store.records) != 2 {
		t.Fatalf("both scopes must be audited, got %d records", len(store.records))
	}
	if store.records[0].Status != SweepFailed || store.records[1].Status != SweepSucceeded {
		t.Fatalf("audit statuses wrong: %+v", store.records)
	}
}

func TestRunOncePropagatesContextCancellation(t *testing.T) {
	store := &fakeSweepStore{policies: []Policy{testPolicy("p1", "development", 30), testPolicy("p2", "development", 30)}}
	sweeper := &fakeFactSweeper{expired: map[string]uint64{}}
	worker := NewSweepWorker(store, sweeper, false, time.Minute, discardLogger())
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := worker.RunOnce(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
}

func TestListFailureSurfacesFromRunOnce(t *testing.T) {
	store := &fakeSweepStore{listErr: errors.New("pg unavailable")}
	worker := NewSweepWorker(store, &fakeFactSweeper{}, false, time.Minute, discardLogger())
	if _, err := worker.RunOnce(context.Background()); err == nil {
		t.Fatal("listing failure must surface")
	}
}
