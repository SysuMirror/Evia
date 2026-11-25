package analysis

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/howtomakeaname/Evia/backend/internal/platform/fault"
)

type Service struct {
	store     Store
	engine    Engine
	validator Validator
}

func New(store Store, engine Engine, validator Validator) *Service {
	return &Service{store: store, engine: engine, validator: validator}
}
func (s *Service) Create(ctx context.Context, actor, project string, raw json.RawMessage) (Job, error) {
	request, e := Parse(raw, s.validator)
	if e != nil {
		return Job{}, e
	}
	if request.Kind == QueryKindJourney && request.Cursor != "" {
		if _, e = ValidateJourneyCursorScope(Scope{ProjectID: project, Environment: request.Environment}, request); e != nil {
			return Job{}, e
		}
	}
	if s.engine == nil {
		return Job{}, fault.New(fault.Unavailable, "QUERY_STORAGE_UNAVAILABLE", "分析存储尚未配置")
	}
	return s.store.EnqueueQuery(ctx, actor, project, request)
}
func (s *Service) Get(ctx context.Context, actor, project, id string) (Job, error) {
	return s.store.GetQuery(ctx, actor, project, id)
}
func (s *Service) Cancel(ctx context.Context, actor, project, id string) (Job, error) {
	return s.store.CancelQuery(ctx, actor, project, id)
}
func (s *Service) Result(ctx context.Context, actor, project, id string) (Result, error) {
	return s.store.QueryResult(ctx, actor, project, id)
}

type Worker struct {
	store    JobStore
	engine   Engine
	observer QueryObserver
	id       string
}

func NewWorker(store JobStore, engine Engine, workerID string, observer QueryObserver) *Worker {
	if observer == nil {
		observer = NoopQueryObserver{}
	}
	return &Worker{store: store, engine: engine, observer: observer, id: workerID}
}
func (w *Worker) Run(ctx context.Context) error {
	for {
		if e := ctx.Err(); e != nil {
			return e
		}
		worked, e := w.RunOnce(ctx)
		if e != nil && !errors.Is(e, context.Canceled) {
			var known *fault.Error
			if !errors.As(e, &known) || known.Code != "QUERY_LEASE_LOST" {
				return e
			}
		}
		if !worked {
			timer := time.NewTimer(250 * time.Millisecond)
			select {
			case <-ctx.Done():
				timer.Stop()
				return ctx.Err()
			case <-timer.C:
			}
		}
	}
}
func (w *Worker) RunOnce(ctx context.Context) (bool, error) {
	if w.engine == nil {
		return false, fault.New(fault.Unavailable, "QUERY_STORAGE_UNAVAILABLE", "分析存储尚未配置")
	}
	claim, e := w.store.ClaimQuery(ctx, w.id, 45*time.Second)
	if e != nil {
		w.observer.ClaimPollCompleted(ClaimOutcomeError)
		return false, e
	}
	if claim == nil {
		w.observer.ClaimPollCompleted(ClaimOutcomeEmpty)
		return false, nil
	}
	w.observer.ClaimPollCompleted(ClaimOutcomeClaimed)
	started := time.Now()
	execution, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	monitorDone := make(chan struct{})
	go func() {
		defer close(monitorDone)
		ticker := time.NewTicker(250 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-execution.Done():
				return
			case <-ticker.C:
				valid, e := w.store.QueryLeaseValid(execution, *claim)
				if e != nil || !valid {
					cancel()
					return
				}
			}
		}
	}()
	result, e := w.engine.Execute(execution, Scope{ProjectID: claim.ProjectID, Environment: claim.Request.Environment}, claim.Request)
	cancel()
	<-monitorDone
	if ctx.Err() != nil {
		return true, ctx.Err()
	}
	outcome := Outcome{}
	if e != nil {
		code := "QUERY_EXECUTION_FAILED"
		retryable := false
		var known *fault.Error
		if errors.As(e, &known) {
			code = known.Code
			retryable = known.Kind == fault.Unavailable
		}
		if errors.Is(e, context.DeadlineExceeded) {
			code = "QUERY_TIMEOUT"
		}
		if errors.Is(e, context.Canceled) {
			code = "QUERY_CANCELED"
		}
		outcome.Error = &JobError{Code: code, Retryable: retryable}
	} else {
		result.QueryID = claim.JobID
		encoded, encodingError := json.Marshal(result)
		if encodingError != nil || len(result.Rows) > 500 || len(encoded) > 1536*1024 {
			outcome.Error = &JobError{Code: "QUERY_RESULT_LIMIT_EXCEEDED", Retryable: false}
		} else {
			outcome.Result = &result
		}
	}
	finishCtx, finishCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer finishCancel()
	if e := w.store.CompleteQuery(finishCtx, *claim, outcome); e != nil {
		return true, e
	}
	w.observer.QueryProcessed(queryResultCode(outcome),
		outcome.Error != nil && outcome.Error.Retryable, time.Since(started))
	return true, nil
}
