package erasure

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/howtomakeaname/Evia/backend/internal/platform/fault"
)

type fakeWorkerStore struct {
	claim       *Claim
	claimErr    error
	beginErr    error
	advanceErr  error
	completeErr error
	failErr     error
	transitions []string
	failedCode  string
	completed   int
	begun       int
}

func (s *fakeWorkerStore) CreateJob(context.Context, Input) (Job, bool, error) { panic("unused") }
func (s *fakeWorkerStore) GetJob(context.Context, string, string, string) (Job, error) {
	panic("unused")
}
func (s *fakeWorkerStore) ListJobs(context.Context, string, string, ListFilter) (Page, error) {
	panic("unused")
}
func (s *fakeWorkerStore) ClaimJob(context.Context, string, time.Duration) (*Claim, error) {
	return s.claim, s.claimErr
}
func (s *fakeWorkerStore) BeginHiding(_ context.Context, claim Claim) error {
	// Mirror the PostgreSQL pre-check so worker/store state contracts cannot drift.
	if claim.State != StateHiding {
		return fault.New(fault.Invalid, "INVALID_ERASURE_INPUT", "作业状态不允许写入删除屏障")
	}
	s.begun++
	return s.beginErr
}
func (s *fakeWorkerStore) Advance(_ context.Context, claim Claim, state string) error {
	expected := map[string]string{StatePurging: StateHiding, StateRebuilding: StatePurging}[state]
	if claim.State != expected {
		return fault.New(fault.Invalid, "INVALID_ERASURE_INPUT", "删除作业当前状态不允许该流转")
	}
	s.transitions = append(s.transitions, state)
	return s.advanceErr
}
func (s *fakeWorkerStore) Complete(context.Context, Claim) error {
	s.completed++
	return s.completeErr
}
func (s *fakeWorkerStore) Fail(_ context.Context, _ Claim, code string) error {
	s.failedCode = code
	return s.failErr
}
func (s *fakeWorkerStore) ErasedSubjects(context.Context, string, string, []string, []string) (ErasedSet, error) {
	panic("unused")
}

type fakeDeleter struct {
	deleteErr error
	remaining uint64
	deleted   int
	checked   int
	lastScope [4]string
}

func (d *fakeDeleter) DeleteIdentity(_ context.Context, project, environment, kind, value string) error {
	d.deleted++
	d.lastScope = [4]string{project, environment, kind, value}
	return d.deleteErr
}
func (d *fakeDeleter) RemainingIdentityRows(_ context.Context, project, environment, kind, value string) (uint64, error) {
	d.checked++
	d.lastScope = [4]string{project, environment, kind, value}
	return d.remaining, nil
}

func testWorker(store Store, deleter FactDeleter) *Worker {
	return &Worker{Store: store, Deleter: deleter, ID: "erasure-test", Lease: time.Minute,
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
}

func sampleClaim() *Claim {
	return &Claim{Worker: "erasure-test", Generation: 1, Job: Job{
		JobID: "88888888-8888-4888-8888-888888888888", ProjectID: "project", Environment: "development",
		SubjectKind: SubjectUID, SubjectValue: "user-1", State: StateHiding,
	}}
}

func TestErasureWorkerHappyPathLifecycle(t *testing.T) {
	store := &fakeWorkerStore{claim: sampleClaim()}
	deleter := &fakeDeleter{}
	worked, err := testWorker(store, deleter).RunOnce(context.Background())
	if err != nil || !worked {
		t.Fatal(worked, err)
	}
	if store.begun != 1 || deleter.deleted != 1 || deleter.checked != 1 || store.completed != 1 {
		t.Fatalf("lifecycle steps missing: %+v %+v", store, deleter)
	}
	if len(store.transitions) != 2 || store.transitions[0] != StatePurging || store.transitions[1] != StateRebuilding {
		t.Fatalf("wrong state transitions: %v", store.transitions)
	}
	if deleter.lastScope != [4]string{"project", "development", SubjectUID, "user-1"} {
		t.Fatalf("delete scope wrong: %v", deleter.lastScope)
	}
	if store.failedCode != "" {
		t.Fatal("successful job was marked failed")
	}
}

func TestErasureWorkerDeleteFailureParksJob(t *testing.T) {
	store := &fakeWorkerStore{claim: sampleClaim()}
	deleter := &fakeDeleter{deleteErr: errors.New("clickhouse down")}
	if _, err := testWorker(store, deleter).RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if store.failedCode != "ERASURE_DELETE_FAILED" || store.completed != 0 || deleter.checked != 0 {
		t.Fatalf("delete failure handling wrong: %+v", store)
	}
}

func TestErasureWorkerRemainingRowsParksJob(t *testing.T) {
	store := &fakeWorkerStore{claim: sampleClaim()}
	deleter := &fakeDeleter{remaining: 3}
	if _, err := testWorker(store, deleter).RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if store.failedCode != "ERASURE_ROWS_REMAIN" || store.completed != 0 {
		t.Fatalf("remaining-rows handling wrong: %+v", store)
	}
}

func TestErasureWorkerLeaseLostIsNotOverwritten(t *testing.T) {
	store := &fakeWorkerStore{claim: sampleClaim(), beginErr: fault.New(fault.Conflict, erasureLeaseLostCode, "租约失效")}
	deleter := &fakeDeleter{}
	if _, err := testWorker(store, deleter).RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if store.failedCode != "" || deleter.deleted != 0 {
		t.Fatal("lost-lease job was overwritten", store.failedCode)
	}
}

func TestErasureWorkerWithoutDeleterIsUnavailable(t *testing.T) {
	worker := testWorker(&fakeWorkerStore{}, nil)
	worked, err := worker.RunOnce(context.Background())
	if worked || err == nil {
		t.Fatal("missing deleter should fail closed")
	}
}

func TestErasureWorkerEmptyQueue(t *testing.T) {
	store := &fakeWorkerStore{claim: nil}
	worked, err := testWorker(store, &fakeDeleter{}).RunOnce(context.Background())
	if worked || err != nil {
		t.Fatal("empty queue should report no work and no error")
	}
}
