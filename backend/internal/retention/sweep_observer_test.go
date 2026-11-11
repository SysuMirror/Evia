package retention

import (
	"context"
	"errors"
	"testing"
	"time"
)

type scopeEvent struct {
	environment string
	mode        string
	succeeded   bool
	candidates  uint64
	duration    time.Duration
}

type passEvent struct {
	succeeded bool
	duration  time.Duration
}

type recordingObserver struct {
	scopes []scopeEvent
	passes []passEvent
}

func (r *recordingObserver) SweepPassCompleted(succeeded bool, duration time.Duration) {
	r.passes = append(r.passes, passEvent{succeeded: succeeded, duration: duration})
}

func (r *recordingObserver) ScopeSwept(environment, mode string, succeeded bool, candidates uint64, duration time.Duration) {
	r.scopes = append(r.scopes, scopeEvent{environment: environment, mode: mode, succeeded: succeeded, candidates: candidates, duration: duration})
}

func TestObserverRecordsSuccessfulDryRunScopeAndPass(t *testing.T) {
	store := &fakeSweepStore{policies: []Policy{testPolicy("p1", "development", 30)}}
	sweeper := &fakeFactSweeper{expired: map[string]uint64{key("p1", "development"): 7}}
	observer := &recordingObserver{}
	worker := NewSweepWorker(store, sweeper, false, time.Minute, discardLogger(), observer)

	if _, err := worker.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(observer.passes) != 1 || !observer.passes[0].succeeded || observer.passes[0].duration < 0 {
		t.Fatalf("pass event wrong: %+v", observer.passes)
	}
	if len(observer.scopes) != 1 {
		t.Fatalf("expected one scope event, got %d", len(observer.scopes))
	}
	scope := observer.scopes[0]
	if scope.environment != "development" || scope.mode != ModeDryRun || !scope.succeeded || scope.candidates != 7 {
		t.Fatalf("dry-run scope event wrong: %+v", scope)
	}
}

func TestObserverRecordsEnforcementModeAndCandidates(t *testing.T) {
	store := &fakeSweepStore{policies: []Policy{testPolicy("p1", "production", 365)}}
	sweeper := &fakeFactSweeper{expired: map[string]uint64{key("p1", "production"): 42}}
	observer := &recordingObserver{}
	worker := NewSweepWorker(store, sweeper, true, time.Minute, discardLogger(), observer)

	if _, err := worker.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	scope := observer.scopes[0]
	if scope.mode != ModeEnforce || !scope.succeeded || scope.candidates != 42 {
		t.Fatalf("enforcement scope event wrong: %+v", scope)
	}
}

func TestObserverMarksFailedScopeAndContinuesPass(t *testing.T) {
	store := &fakeSweepStore{policies: []Policy{
		testPolicy("p-bad", "development", 30),
		testPolicy("p-good", "staging", 60),
	}}
	sweeper := &fakeFactSweeper{
		expired:     map[string]uint64{key("p-good", "staging"): 3},
		failProject: "p-bad",
		failErr:     errors.New("clickhouse unavailable"),
	}
	observer := &recordingObserver{}
	worker := NewSweepWorker(store, sweeper, false, time.Minute, discardLogger(), observer)

	if _, err := worker.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	// One surviving scope still means the overall pass completed.
	if len(observer.passes) != 1 || !observer.passes[0].succeeded {
		t.Fatalf("pass should succeed despite one failed scope: %+v", observer.passes)
	}
	if len(observer.scopes) != 2 {
		t.Fatalf("expected two scope events, got %d", len(observer.scopes))
	}
	if observer.scopes[0].succeeded || observer.scopes[0].candidates != 0 || observer.scopes[0].environment != "development" {
		t.Fatalf("failed scope event wrong: %+v", observer.scopes[0])
	}
	if !observer.scopes[1].succeeded || observer.scopes[1].candidates != 3 || observer.scopes[1].environment != "staging" {
		t.Fatalf("successful scope event wrong: %+v", observer.scopes[1])
	}
}

func TestObserverReportsFailedPassWhenPolicyListUnavailable(t *testing.T) {
	store := &fakeSweepStore{listErr: errors.New("pg unavailable")}
	observer := &recordingObserver{}
	worker := NewSweepWorker(store, &fakeFactSweeper{}, false, time.Minute, discardLogger(), observer)

	if _, err := worker.RunOnce(context.Background()); err == nil {
		t.Fatal("listing failure must surface")
	}
	if len(observer.passes) != 1 || observer.passes[0].succeeded {
		t.Fatalf("failed pass event wrong: %+v", observer.passes)
	}
	if len(observer.scopes) != 0 {
		t.Fatalf("listing failure must not emit scope events: %+v", observer.scopes)
	}
}

func TestObserverSkipsPassEventOnContextCancellation(t *testing.T) {
	store := &fakeSweepStore{policies: []Policy{testPolicy("p1", "development", 30)}}
	observer := &recordingObserver{}
	worker := NewSweepWorker(store, &fakeFactSweeper{}, false, time.Minute, discardLogger(), observer)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := worker.RunOnce(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
	if len(observer.passes) != 0 {
		t.Fatalf("shutdown cancellation must not record a pass: %+v", observer.passes)
	}
}

func TestNoopObserverIsSafeAndNilDefaultsToNoop(t *testing.T) {
	NoopSweepObserver{}.SweepPassCompleted(true, time.Millisecond)
	NoopSweepObserver{}.ScopeSwept("development", ModeDryRun, true, 1, time.Millisecond)
	store := &fakeSweepStore{policies: []Policy{testPolicy("p1", "development", 30)}}
	sweeper := &fakeFactSweeper{expired: map[string]uint64{key("p1", "development"): 1}}
	worker := NewSweepWorker(store, sweeper, false, time.Minute, discardLogger(), nil)
	if _, err := worker.RunOnce(context.Background()); err != nil {
		t.Fatalf("nil observer must default to noop: %v", err)
	}
}
