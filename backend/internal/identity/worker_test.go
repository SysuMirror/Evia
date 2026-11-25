package identity

import (
	"context"
	"errors"
	"testing"
	"time"
)

var foldScope = Scope{ProjectID: "00000000-0000-4000-8000-0000000000aa", Environment: "development"}

type fakeFoldStore struct {
	scopes    []Scope
	claim     *FoldClaim
	claimOK   bool
	claimErr  error
	outcome   FoldOutcome
	commitErr error
	committed int
	released  int
	pruned    int
	pruneDel  int64
	pruneErr  error
}

func (s *fakeFoldStore) FoldScopes(context.Context) ([]Scope, error) { return s.scopes, nil }
func (s *fakeFoldStore) ClaimScope(_ context.Context, _ Scope, _ string, _ time.Duration) (*FoldClaim, bool, error) {
	return s.claim, s.claimOK, s.claimErr
}
func (s *fakeFoldStore) CommitFold(_ context.Context, _ *FoldClaim, outcome FoldOutcome) error {
	s.committed++
	s.outcome = outcome
	return s.commitErr
}
func (s *fakeFoldStore) ReleaseFold(context.Context, *FoldClaim) error {
	s.released++
	return nil
}
func (s *fakeFoldStore) RecordFoldFailure(context.Context, *FoldClaim, string) error { return nil }
func (s *fakeFoldStore) PruneRevisions(context.Context, Scope, time.Time, time.Duration, int) (int64, error) {
	s.pruned++
	return s.pruneDel, s.pruneErr
}
func (s *fakeFoldStore) ListRevisions(context.Context, string, string, string, ReadFilter) (Page, error) {
	return Page{}, nil
}
func (s *fakeFoldStore) GetRevision(context.Context, string, string, string, int64) (Revision, error) {
	return Revision{}, nil
}
func (s *fakeFoldStore) ResolveMember(context.Context, string, string, string, int64, string, string) (Resolution, error) {
	return Resolution{}, nil
}

type fakeLinkFacts struct {
	links []LinkScan
	err   error
}

func (f *fakeLinkFacts) ScanLinks(_ context.Context, _ Scope, _ time.Time, _ int) ([]LinkScan, bool, error) {
	return f.links, len(f.links) < 2, f.err
}

type fakeTombstones struct {
	set ErasedSet
	err error
}

func (b *fakeTombstones) Erased(context.Context, Scope, []string, []string) (ErasedSet, error) {
	return b.set, b.err
}

type recordingObserver struct {
	NoopObserver
	events []string
}

func (o *recordingObserver) FoldScopeClaimed(Scope)  { o.events = append(o.events, "claimed") }
func (o *recordingObserver) FoldScanFailed(Scope)    { o.events = append(o.events, "scan-failed") }
func (o *recordingObserver) FoldBarrierFailed(Scope) { o.events = append(o.events, "barrier-failed") }
func (o *recordingObserver) FoldUnchanged(Scope)     { o.events = append(o.events, "unchanged") }
func (o *recordingObserver) FoldCompleted(Scope, int64, int64, int64) {
	o.events = append(o.events, "completed")
}
func (o *recordingObserver) FoldPruned(Scope, int64) { o.events = append(o.events, "pruned") }
func (o *recordingObserver) FoldPruneFailed(Scope)   { o.events = append(o.events, "prune-failed") }

func claimedFoldStore() *fakeFoldStore {
	return &fakeFoldStore{
		scopes:  []Scope{foldScope},
		claimOK: true,
		claim: &FoldClaim{
			Scope:        foldScope,
			Worker:       "w1",
			Generation:   1,
			NextRevision: 1,
			Graph:        &Graph{},
		},
	}
}

func TestWorkerCommitsChangedFold(t *testing.T) {
	store := claimedFoldStore()
	facts := &fakeLinkFacts{links: []LinkScan{
		{UID: "u1", AnonymousID: "00000000-0000-4000-8000-0000000000b1", FirstSeenAt: base, LastSeenAt: base},
	}}
	observer := &recordingObserver{}
	worker := NewWorker(store, facts, &fakeTombstones{set: ErasedSet{UIDs: map[string]struct{}{}, AnonymousIDs: map[string]struct{}{}}}, observer, WorkerOptions{Worker: "w1"})
	worker.RunOnce(context.Background())
	if store.committed != 1 || store.released != 0 {
		t.Fatalf("changed fold must commit exactly once: committed=%d released=%d", store.committed, store.released)
	}
	if store.outcome.NextRevision != 1 || !store.outcome.Changed {
		t.Fatalf("commit outcome wrong: %+v", store.outcome)
	}
	if len(observer.events) != 2 || observer.events[0] != "claimed" || observer.events[1] != "completed" {
		t.Fatalf("observer events wrong: %v", observer.events)
	}
}

func TestWorkerBarrierFailureFailsClosed(t *testing.T) {
	store := claimedFoldStore()
	facts := &fakeLinkFacts{links: []LinkScan{
		{UID: "u1", AnonymousID: "a1", FirstSeenAt: base, LastSeenAt: base},
	}}
	observer := &recordingObserver{}
	worker := NewWorker(store, facts, &fakeTombstones{err: errors.New("barrier down")}, observer, WorkerOptions{Worker: "w1"})
	worker.RunOnce(context.Background())
	if store.committed != 0 || store.released != 1 {
		t.Fatalf("barrier failure must release without committing: committed=%d released=%d", store.committed, store.released)
	}
	if len(observer.events) != 2 || observer.events[1] != "barrier-failed" {
		t.Fatalf("observer events wrong: %v", observer.events)
	}
}

func TestWorkerScanFailureFailsClosed(t *testing.T) {
	store := claimedFoldStore()
	facts := &fakeLinkFacts{err: errors.New("warehouse down")}
	observer := &recordingObserver{}
	worker := NewWorker(store, facts, &fakeTombstones{}, observer, WorkerOptions{Worker: "w1"})
	worker.RunOnce(context.Background())
	if store.committed != 0 || store.released != 1 {
		t.Fatalf("scan failure must release without committing")
	}
	if len(observer.events) != 2 || observer.events[1] != "scan-failed" {
		t.Fatalf("observer events wrong: %v", observer.events)
	}
}

func TestWorkerOverlapRescanReleasesWithoutRevision(t *testing.T) {
	store := claimedFoldStore()
	first := Fold(foldScope, &Graph{}, []LinkScan{
		{UID: "u1", AnonymousID: "a1", FirstSeenAt: base, LastSeenAt: base},
	}, 1, base, NewPersonID)
	apply(store.claim.Graph, first)
	facts := &fakeLinkFacts{links: []LinkScan{
		{UID: "u1", AnonymousID: "a1", FirstSeenAt: base, LastSeenAt: base},
	}}
	observer := &recordingObserver{}
	worker := NewWorker(store, facts, &fakeTombstones{}, observer, WorkerOptions{Worker: "w1"})
	worker.RunOnce(context.Background())
	if store.committed != 0 || store.released != 1 {
		t.Fatalf("unchanged fold must not write a revision")
	}
	if len(observer.events) != 2 || observer.events[1] != "unchanged" {
		t.Fatalf("observer events wrong: %v", observer.events)
	}
}

func TestWorkerSkipsTombstonedPairs(t *testing.T) {
	store := claimedFoldStore()
	facts := &fakeLinkFacts{links: []LinkScan{
		{UID: "u1", AnonymousID: "a1", FirstSeenAt: base, LastSeenAt: base},
		{UID: "gone", AnonymousID: "a2", FirstSeenAt: base, LastSeenAt: base},
	}}
	worker := NewWorker(store, facts, &fakeTombstones{set: ErasedSet{
		UIDs:         map[string]struct{}{"gone": {}},
		AnonymousIDs: map[string]struct{}{},
	}}, &recordingObserver{}, WorkerOptions{Worker: "w1"})
	worker.RunOnce(context.Background())
	if store.committed != 1 {
		t.Fatal("fold must still commit for surviving pairs")
	}
	for _, edge := range store.outcome.EdgeUpserts {
		if edge.UID == "gone" {
			t.Fatal("tombstoned identity must never enter the graph")
		}
	}
}

func TestWorkerSkipsHeldLease(t *testing.T) {
	store := &fakeFoldStore{scopes: []Scope{foldScope}, claimOK: false}
	observer := &recordingObserver{}
	worker := NewWorker(store, &fakeLinkFacts{}, &fakeTombstones{}, observer, WorkerOptions{Worker: "w1"})
	worker.RunOnce(context.Background())
	if store.committed != 0 || store.released != 0 || len(observer.events) != 0 {
		t.Fatalf("held lease must be a silent skip: committed=%d released=%d events=%v", store.committed, store.released, observer.events)
	}
}

func TestWorkerPrunesExpiredRevisions(t *testing.T) {
	store := claimedFoldStore()
	store.pruneDel = 3
	observer := &recordingObserver{}
	worker := NewWorker(store, &fakeLinkFacts{}, &fakeTombstones{}, observer, WorkerOptions{Worker: "w1"})
	worker.RunOnce(context.Background())
	if store.pruned != 1 {
		t.Fatalf("expected prune after unchanged round, got %d", store.pruned)
	}
	if len(observer.events) != 3 || observer.events[2] != "pruned" {
		t.Fatalf("observer events wrong: %v", observer.events)
	}

	store.pruneErr = errors.New("db busy")
	observer.events = nil
	worker.RunOnce(context.Background())
	if len(observer.events) != 3 || observer.events[2] != "prune-failed" {
		t.Fatalf("prune failure must be observed: %v", observer.events)
	}
}
