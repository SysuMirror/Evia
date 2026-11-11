package analysis

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/howtomakeaname/Evia/backend/internal/platform/fault"
)

type queryEvent struct {
	code      string
	retryable bool
	duration  time.Duration
}

type recordingQueryObserver struct {
	claims []string
	jobs   []queryEvent
}

func (r *recordingQueryObserver) ClaimPollCompleted(outcome string) {
	r.claims = append(r.claims, outcome)
}

func (r *recordingQueryObserver) QueryProcessed(code string, retryable bool, duration time.Duration) {
	r.jobs = append(r.jobs, queryEvent{code: code, retryable: retryable, duration: duration})
}

func observedQueryWorker(store JobStore, engine Engine, observer QueryObserver) *Worker {
	return NewWorker(store, engine, "worker", observer)
}

func sampleQueryClaim() *Claim {
	return &Claim{JobID: "query", ActorID: "actor", ProjectID: "project",
		Worker: "worker", Generation: 2, Request: Request{Environment: "development"}}
}

func TestQueryObserverRecordsSuccessfulJobAndClaimedPoll(t *testing.T) {
	store := &workerStore{claim: sampleQueryClaim(), valid: true}
	engine := engineFunc(func(context.Context, Scope, Request) (Result, error) {
		return Result{Rows: []map[string]any{{"value": 1}}}, nil
	})
	observer := &recordingQueryObserver{}

	if worked, err := observedQueryWorker(store, engine, observer).RunOnce(context.Background()); err != nil || !worked {
		t.Fatal(worked, err)
	}
	if len(observer.claims) != 1 || observer.claims[0] != ClaimOutcomeClaimed {
		t.Fatalf("claim event wrong: %+v", observer.claims)
	}
	if len(observer.jobs) != 1 {
		t.Fatalf("expected one query event, got %d", len(observer.jobs))
	}
	job := observer.jobs[0]
	if job.code != QueryOutcomeOK || job.retryable || job.duration < 0 {
		t.Fatalf("success event wrong: %+v", job)
	}
}

func TestQueryObserverRecordsEmptyQueueWithoutJobEvent(t *testing.T) {
	observer := &recordingQueryObserver{}
	worker := observedQueryWorker(&workerStore{claim: nil}, engineFunc(func(context.Context, Scope, Request) (Result, error) {
		t.Fatal("engine must not run on empty queue")
		return Result{}, nil
	}), observer)

	if worked, err := worker.RunOnce(context.Background()); err != nil || worked {
		t.Fatal(worked, err)
	}
	if len(observer.claims) != 1 || observer.claims[0] != ClaimOutcomeEmpty || len(observer.jobs) != 0 {
		t.Fatalf("empty-queue events wrong: claims=%+v jobs=%+v", observer.claims, observer.jobs)
	}
}

func TestQueryObserverRecordsFailureCodeAndRetryable(t *testing.T) {
	store := &workerStore{claim: sampleQueryClaim(), valid: true}
	engine := engineFunc(func(context.Context, Scope, Request) (Result, error) {
		return Result{}, fault.New(fault.Unavailable, "QUERY_EXECUTION_FAILED", "clickhouse unavailable")
	})
	observer := &recordingQueryObserver{}

	if _, err := observedQueryWorker(store, engine, observer).RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	job := observer.jobs[0]
	if job.code != "QUERY_EXECUTION_FAILED" || !job.retryable {
		t.Fatalf("retryable failure event wrong: %+v", job)
	}
}

func TestQueryObserverRecordsCostRejectionAsNonRetryable(t *testing.T) {
	store := &workerStore{claim: sampleQueryClaim(), valid: true}
	engine := engineFunc(func(context.Context, Scope, Request) (Result, error) {
		return Result{}, fault.New(fault.Invalid, "QUERY_COST_EXCEEDED", "查询超过执行资源预算")
	})
	observer := &recordingQueryObserver{}

	if _, err := observedQueryWorker(store, engine, observer).RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	job := observer.jobs[0]
	if job.code != "QUERY_COST_EXCEEDED" || job.retryable {
		t.Fatalf("cost rejection event wrong: %+v", job)
	}
}

func TestQueryObserverRecordsOversizedResultCode(t *testing.T) {
	store := &workerStore{claim: sampleQueryClaim(), valid: true}
	engine := engineFunc(func(context.Context, Scope, Request) (Result, error) {
		return Result{Rows: []map[string]any{{"large": make([]byte, 2*1024*1024)}}}, nil
	})
	observer := &recordingQueryObserver{}

	if _, err := observedQueryWorker(store, engine, observer).RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if observer.jobs[0].code != "QUERY_RESULT_LIMIT_EXCEEDED" {
		t.Fatalf("oversized result event wrong: %+v", observer.jobs)
	}
}

func TestQueryObserverRecordsClaimError(t *testing.T) {
	store := &workerStore{claimErr: errors.New("pg unavailable")}
	observer := &recordingQueryObserver{}
	worker := observedQueryWorker(store, engineFunc(func(context.Context, Scope, Request) (Result, error) {
		t.Fatal("engine must not run when claim fails")
		return Result{}, nil
	}), observer)

	if _, err := worker.RunOnce(context.Background()); err == nil {
		t.Fatal("claim error must surface")
	}
	if len(observer.claims) != 1 || observer.claims[0] != ClaimOutcomeError || len(observer.jobs) != 0 {
		t.Fatalf("claim error events wrong: claims=%+v jobs=%+v", observer.claims, observer.jobs)
	}
}

func TestNoopQueryObserverIsSafeAndNilDefaultsToNoop(t *testing.T) {
	NoopQueryObserver{}.ClaimPollCompleted(ClaimOutcomeClaimed)
	NoopQueryObserver{}.QueryProcessed(QueryOutcomeOK, false, time.Millisecond)
	store := &workerStore{claim: sampleQueryClaim(), valid: true}
	engine := engineFunc(func(context.Context, Scope, Request) (Result, error) { return Result{}, nil })
	if _, err := NewWorker(store, engine, "worker", nil).RunOnce(context.Background()); err != nil {
		t.Fatalf("nil observer must default to noop: %v", err)
	}
}
