package analysis

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

type workerStore struct {
	claim       *Claim
	valid       bool
	outcome     *Outcome
	completions int
}

func (s *workerStore) ClaimQuery(context.Context, string, time.Duration) (*Claim, error) {
	return s.claim, nil
}
func (s *workerStore) QueryLeaseValid(context.Context, Claim) (bool, error) { return s.valid, nil }
func (s *workerStore) CompleteQuery(_ context.Context, _ Claim, outcome Outcome) error {
	s.outcome = &outcome
	s.completions++
	return nil
}

type engineFunc func(context.Context, Scope, Request) (Result, error)

func (fn engineFunc) Execute(ctx context.Context, scope Scope, request Request) (Result, error) {
	return fn(ctx, scope, request)
}
func TestWorkerPersistsBoundedResultsAndLeaseCancellation(t *testing.T) {
	request := Request{Environment: "development"}
	claim := &Claim{JobID: "query", ActorID: "actor", ProjectID: "project", Worker: "worker", Generation: 2, Request: request}
	store := &workerStore{claim: claim, valid: true}
	engine := engineFunc(func(_ context.Context, scope Scope, _ Request) (Result, error) {
		if scope.ProjectID != "project" || scope.Environment != "development" {
			t.Fatal("scope lost")
		}
		return Result{Rows: []map[string]any{{"value": 0, "enabled": false}}}, nil
	})
	if worked, e := NewWorker(store, engine, "worker").RunOnce(context.Background()); e != nil || !worked || store.outcome == nil || store.outcome.Result.QueryID != "query" {
		t.Fatal(worked, e, store.outcome)
	}
	store = &workerStore{claim: claim, valid: false}
	stopped := false
	engine = engineFunc(func(ctx context.Context, _ Scope, _ Request) (Result, error) {
		<-ctx.Done()
		stopped = true
		return Result{}, ctx.Err()
	})
	if _, e := NewWorker(store, engine, "worker").RunOnce(context.Background()); e != nil || !stopped || store.outcome.Error == nil {
		t.Fatal("revoked lease failed to interrupt engine", e)
	}
	store = &workerStore{claim: claim, valid: true}
	engine = engineFunc(func(context.Context, Scope, Request) (Result, error) {
		return Result{Rows: []map[string]any{{"large": strings.Repeat("a", 2*1024*1024)}}}, nil
	})
	if _, e := NewWorker(store, engine, "worker").RunOnce(context.Background()); e != nil || store.outcome.Error == nil || store.outcome.Error.Code != "QUERY_RESULT_LIMIT_EXCEEDED" {
		t.Fatal("oversized result left running", store.outcome, e)
	}
}
func TestWorkerShutdownDoesNotPublishCanceledResult(t *testing.T) {
	store := &workerStore{claim: &Claim{Request: Request{}}, valid: true}
	ctx, cancel := context.WithCancel(context.Background())
	engine := engineFunc(func(context.Context, Scope, Request) (Result, error) { cancel(); return Result{}, context.Canceled })
	if _, e := NewWorker(store, engine, "worker").RunOnce(ctx); !errors.Is(e, context.Canceled) || store.completions != 0 {
		t.Fatal("shutdown published a terminal result instead of leaving a recoverable lease", e, store.completions)
	}
}
