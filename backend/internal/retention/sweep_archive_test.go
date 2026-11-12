package retention

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/howtomakeaname/Evia/backend/internal/archiving"
)

type fakeScopeArchiver struct {
	calls []archiving.Scope
	fail  bool
	empty bool
}

func (f *fakeScopeArchiver) ArchiveScope(_ context.Context, scope archiving.Scope) (archiving.Manifest, error) {
	f.calls = append(f.calls, scope)
	if f.fail {
		return archiving.Manifest{}, errors.New("archive storage unavailable")
	}
	if f.empty {
		return archiving.Manifest{}, nil
	}
	return archiving.Manifest{ManifestID: "33333333-3333-4333-8333-333333333333", Status: archiving.StatusMaterialized}, nil
}

func TestEnforcementArchivesBeforeDeleteWithSweepLink(t *testing.T) {
	store := &fakeSweepStore{policies: []Policy{testPolicy("p1", "production", 365)}}
	sweeper := &fakeFactSweeper{expired: map[string]uint64{key("p1", "production"): 42}}
	archiver := &fakeScopeArchiver{}
	worker := NewSweepWorker(store, sweeper, true, time.Minute, discardLogger(), NoopSweepObserver{}, WithScopeArchiver(archiver))

	results, err := worker.RunOnce(context.Background())
	if err != nil || results[0].Err != nil {
		t.Fatalf("archive-before-delete failed: %+v err=%v", results, err)
	}
	if len(archiver.calls) != 1 {
		t.Fatalf("archiver invoked %d times", len(archiver.calls))
	}
	scope := archiver.calls[0]
	if scope.ProjectID != "p1" || scope.Environment != "production" || scope.RetentionDays != 365 ||
		scope.Cutoff.IsZero() || scope.SweepID == "" {
		t.Fatalf("archive scope wrong: %+v", scope)
	}
	foundDelete := false
	for _, call := range sweeper.calls {
		if call == "delete:"+key("p1", "production") {
			foundDelete = true
		}
	}
	if !foundDelete {
		t.Fatal("delete must still run after a successful archive")
	}
}

func TestArchiveFailureBlocksDeletionFailClosed(t *testing.T) {
	store := &fakeSweepStore{policies: []Policy{testPolicy("p1", "production", 365)}}
	sweeper := &fakeFactSweeper{expired: map[string]uint64{key("p1", "production"): 42}}
	archiver := &fakeScopeArchiver{fail: true}
	worker := NewSweepWorker(store, sweeper, true, time.Minute, discardLogger(), NoopSweepObserver{}, WithScopeArchiver(archiver))

	results, err := worker.RunOnce(context.Background())
	if err != nil || results[0].Err == nil {
		t.Fatalf("archive failure must surface on the result: %+v err=%v", results, err)
	}
	for _, call := range sweeper.calls {
		if call[:6] == "delete" {
			t.Fatal("deletion must be blocked when archiving fails")
		}
	}
	record := store.records[0]
	if record.Status != SweepFailed || record.Detail != "archive before delete failed" {
		t.Fatalf("failed sweep record wrong: %+v", record)
	}
}

func TestDryRunAndEmptyScopeSkipArchiver(t *testing.T) {
	// Dry run never reaches the archive hook.
	store := &fakeSweepStore{policies: []Policy{testPolicy("p1", "development", 30)}}
	sweeper := &fakeFactSweeper{expired: map[string]uint64{key("p1", "development"): 7}}
	archiver := &fakeScopeArchiver{}
	worker := NewSweepWorker(store, sweeper, false, time.Minute, discardLogger(), NoopSweepObserver{}, WithScopeArchiver(archiver))
	if _, err := worker.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(archiver.calls) != 0 {
		t.Fatal("dry run must not archive")
	}

	// Enforced but zero-candidate scope has nothing to preserve.
	emptyStore := &fakeSweepStore{policies: []Policy{testPolicy("p2", "development", 30)}}
	emptySweeper := &fakeFactSweeper{expired: map[string]uint64{key("p2", "development"): 0}}
	emptyArchiver := &fakeScopeArchiver{}
	emptyWorker := NewSweepWorker(emptyStore, emptySweeper, true, time.Minute, discardLogger(), NoopSweepObserver{}, WithScopeArchiver(emptyArchiver))
	results, err := emptyWorker.RunOnce(context.Background())
	if err != nil || results[0].Err != nil {
		t.Fatalf("empty scope result wrong: %+v err=%v", results, err)
	}
	if len(emptyArchiver.calls) != 0 {
		t.Fatal("zero-candidate scope must not invoke the archiver")
	}
}
