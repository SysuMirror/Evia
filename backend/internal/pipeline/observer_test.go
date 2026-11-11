package pipeline

import (
	"context"
	"errors"
	"testing"
	"time"
)

// recordingReader is a minimal Reader for process-level tests that never run
// the full Run loop; only Commit is exercised by Worker.process.
type recordingReader struct{ commits int }

func (r *recordingReader) Poll(context.Context, int) ([]Message, error) { return nil, nil }
func (r *recordingReader) Commit(context.Context, []Message) error      { r.commits++; return nil }
func (r *recordingReader) AllowRebalance()                              {}
func (r *recordingReader) Close()                                       {}

type recordingIngestObserver struct {
	polled      []int
	ingested    int
	quarantined int
	batchOK     int
	batchFailed int
	batchTimed  bool
}

func (r *recordingIngestObserver) MessagesPolled(count int) { r.polled = append(r.polled, count) }
func (r *recordingIngestObserver) BatchProcessed(succeeded bool, duration time.Duration) {
	if duration < 0 {
		r.batchTimed = true
	}
	if succeeded {
		r.batchOK++
	} else {
		r.batchFailed++
	}
}
func (r *recordingIngestObserver) RecordsIngested(count int)    { r.ingested += count }
func (r *recordingIngestObserver) RecordsQuarantined(count int) { r.quarantined += count }

func TestObserverCountsIngestedAndQuarantinedRecords(t *testing.T) {
	ctx := context.Background()
	events := []string{}
	observer := &recordingIngestObserver{}
	worker := &Worker{
		Reader:       &recordingReader{},
		Sink:         &memorySink{events: &events},
		Quarantine:   &memoryQuarantine{events: &events},
		Observer:     observer,
		RetryInitial: time.Millisecond,
		RetryMax:     time.Millisecond,
	}
	messages := []Message{testMessage(t), {Topic: "test.events", Partition: 1, Offset: 13, Value: []byte("poison")}}

	if e := worker.process(ctx, messages, time.Millisecond, time.Millisecond); e != nil {
		t.Fatal(e)
	}
	if observer.ingested != 1 || observer.quarantined != 1 {
		t.Fatalf("record counters wrong: %+v", observer)
	}
}

func TestObserverDoesNotCountWhenSinkWriteFails(t *testing.T) {
	ctx := context.Background()
	observer := &recordingIngestObserver{}
	worker := &Worker{
		Sink:         &memorySink{failAlways: true},
		Quarantine:   &memoryQuarantine{},
		Observer:     observer,
		RetryInitial: time.Millisecond,
		RetryMax:     time.Millisecond,
	}
	// An already-canceled stage context makes the retry loop stop immediately.
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if e := worker.process(canceled, []Message{testMessage(t)}, time.Millisecond, time.Millisecond); e == nil {
		t.Fatal("failed sink must surface")
	}
	if observer.ingested != 0 || observer.quarantined != 0 {
		t.Fatalf("failed stage must not emit record counters: %+v", observer)
	}
}

func TestObserverEmitsSucceededBatchFromRun(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	events := []string{}
	reader := &memoryReader{
		events:   &events,
		messages: []Message{testMessage(t), {Topic: "test.events", Partition: 1, Offset: 13, Value: []byte("poison")}},
		cancel:   cancel,
	}
	observer := &recordingIngestObserver{}
	worker := Worker{
		Reader:       reader,
		Sink:         &memorySink{events: &events},
		Quarantine:   &memoryQuarantine{events: &events},
		Observer:     observer,
		RetryInitial: time.Millisecond,
		RetryMax:     time.Millisecond,
	}
	if e := worker.Run(ctx); !errors.Is(e, context.Canceled) {
		t.Fatal(e)
	}
	if len(observer.polled) != 1 || observer.polled[0] != 2 {
		t.Fatalf("poll event wrong: %+v", observer.polled)
	}
	if observer.batchOK != 1 || observer.batchFailed != 0 || observer.batchTimed {
		t.Fatalf("batch events wrong: %+v", observer)
	}
	if observer.ingested != 1 || observer.quarantined != 1 {
		t.Fatalf("record counters wrong: %+v", observer)
	}
}

func TestObserverEmitsFailedBatchWhenStageTimesOut(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	events := []string{}
	reader := &memoryReader{
		events:   &events,
		messages: []Message{testMessage(t)},
		cancel:   cancel,
	}
	observer := &recordingIngestObserver{}
	worker := Worker{
		Reader:       reader,
		Sink:         &memorySink{events: &events, failAlways: true},
		Quarantine:   &memoryQuarantine{events: &events},
		Observer:     observer,
		BatchTimeout: 2 * time.Millisecond,
		RetryInitial: time.Millisecond,
		RetryMax:     time.Millisecond,
	}
	// The batch deadline expires while the sink keeps failing; the parent ctx
	// stays alive, so this is an operational failure rather than shutdown.
	if e := worker.Run(ctx); errors.Is(e, context.Canceled) {
		t.Fatal("expected batch deadline error, got shutdown cancellation")
	}
	if observer.batchFailed != 1 || observer.batchOK != 0 {
		t.Fatalf("failed batch event wrong: %+v", observer)
	}
	if observer.ingested != 0 {
		t.Fatalf("failed batch must not count ingested records: %+v", observer)
	}
}

func TestNoopIngestObserverIsSafeAndNilDefaultsToNoop(t *testing.T) {
	NoopIngestObserver{}.MessagesPolled(1)
	NoopIngestObserver{}.BatchProcessed(true, time.Millisecond)
	NoopIngestObserver{}.RecordsIngested(1)
	NoopIngestObserver{}.RecordsQuarantined(1)
	ctx := context.Background()
	events := []string{}
	worker := &Worker{
		Reader:     &recordingReader{},
		Sink:       &memorySink{events: &events},
		Quarantine: &memoryQuarantine{events: &events},
	}
	if e := worker.process(ctx, []Message{testMessage(t)}, time.Millisecond, time.Millisecond); e != nil {
		t.Fatalf("nil observer must default to noop: %v", e)
	}
}
