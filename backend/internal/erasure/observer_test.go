package erasure

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/howtomakeaname/Evia/backend/internal/platform/fault"
)

type jobEvent struct {
	outcome  string
	code     string
	duration time.Duration
}

type recordingErasureObserver struct {
	claims []string
	jobs   []jobEvent
}

func (r *recordingErasureObserver) ClaimPollCompleted(outcome string) {
	r.claims = append(r.claims, outcome)
}

func (r *recordingErasureObserver) JobProcessed(outcome, code string, duration time.Duration) {
	r.jobs = append(r.jobs, jobEvent{outcome: outcome, code: code, duration: duration})
}

func observedWorker(store Store, deleter FactDeleter, observer ErasureObserver) *Worker {
	worker := testWorker(store, deleter)
	worker.Observer = observer
	return worker
}

func TestObserverRecordsCompletedJobAndClaimedPoll(t *testing.T) {
	store := &fakeWorkerStore{claim: sampleClaim()}
	observer := &recordingErasureObserver{}
	worker := observedWorker(store, &fakeDeleter{}, observer)

	if worked, err := worker.RunOnce(context.Background()); err != nil || !worked {
		t.Fatal(worked, err)
	}
	if len(observer.claims) != 1 || observer.claims[0] != ClaimOutcomeClaimed {
		t.Fatalf("claim event wrong: %+v", observer.claims)
	}
	if len(observer.jobs) != 1 {
		t.Fatalf("expected one job event, got %d", len(observer.jobs))
	}
	job := observer.jobs[0]
	if job.outcome != JobOutcomeCompleted || job.code != "" || job.duration < 0 {
		t.Fatalf("completed job event wrong: %+v", job)
	}
}

func TestObserverRecordsEmptyQueueWithoutJobEvent(t *testing.T) {
	observer := &recordingErasureObserver{}
	worker := observedWorker(&fakeWorkerStore{claim: nil}, &fakeDeleter{}, observer)

	if worked, err := worker.RunOnce(context.Background()); err != nil || worked {
		t.Fatal(worked, err)
	}
	if len(observer.claims) != 1 || observer.claims[0] != ClaimOutcomeEmpty {
		t.Fatalf("empty-queue claim event wrong: %+v", observer.claims)
	}
	if len(observer.jobs) != 0 {
		t.Fatalf("empty queue must not emit a job event: %+v", observer.jobs)
	}
}

func TestObserverRecordsFailedOutcomeWithCode(t *testing.T) {
	store := &fakeWorkerStore{claim: sampleClaim()}
	observer := &recordingErasureObserver{}
	worker := observedWorker(store, &fakeDeleter{deleteErr: errors.New("clickhouse down")}, observer)

	if _, err := worker.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if observer.jobs[0].outcome != JobOutcomeFailed || observer.jobs[0].code != "ERASURE_DELETE_FAILED" {
		t.Fatalf("failed job event wrong: %+v", observer.jobs)
	}
}

func TestObserverDistinguishesLeaseLostFromFailure(t *testing.T) {
	store := &fakeWorkerStore{claim: sampleClaim(), beginErr: fault.New(fault.Conflict, erasureLeaseLostCode, "租约失效")}
	observer := &recordingErasureObserver{}
	worker := observedWorker(store, &fakeDeleter{}, observer)

	if _, err := worker.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if observer.jobs[0].outcome != JobOutcomeLeaseLost || observer.jobs[0].code != "" {
		t.Fatalf("lease-lost event wrong: %+v", observer.jobs)
	}
}

func TestObserverReportsFailureRecordingError(t *testing.T) {
	store := &fakeWorkerStore{
		claim:   sampleClaim(),
		failErr: errors.New("pg unavailable"),
	}
	observer := &recordingErasureObserver{}
	worker := observedWorker(store, &fakeDeleter{deleteErr: errors.New("clickhouse down")}, observer)

	if _, err := worker.RunOnce(context.Background()); err == nil {
		t.Fatal("Fail recording error must surface")
	}
	if observer.jobs[0].outcome != JobOutcomeFailureRecordFail || observer.jobs[0].code != "ERASURE_DELETE_FAILED" {
		t.Fatalf("failure-record event wrong: %+v", observer.jobs)
	}
}

func TestObserverRecordsClaimError(t *testing.T) {
	store := &fakeWorkerStore{claimErr: errors.New("pg unavailable")}
	observer := &recordingErasureObserver{}
	worker := observedWorker(store, &fakeDeleter{}, observer)

	if _, err := worker.RunOnce(context.Background()); err == nil {
		t.Fatal("claim error must surface")
	}
	if len(observer.claims) != 1 || observer.claims[0] != ClaimOutcomeError {
		t.Fatalf("claim error event wrong: %+v", observer.claims)
	}
	if len(observer.jobs) != 0 {
		t.Fatalf("claim error must not emit a job event: %+v", observer.jobs)
	}
}

func TestNoopErasureObserverIsSafeAndNilDefaultsToNoop(t *testing.T) {
	NoopErasureObserver{}.ClaimPollCompleted(ClaimOutcomeClaimed)
	NoopErasureObserver{}.JobProcessed(JobOutcomeCompleted, "", time.Millisecond)
	worker := testWorker(&fakeWorkerStore{claim: sampleClaim()}, &fakeDeleter{})
	if _, err := worker.RunOnce(context.Background()); err != nil {
		t.Fatalf("nil observer must default to noop: %v", err)
	}
}
