package pipeline

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"
)

type fakeLagReader struct {
	mu      sync.Mutex
	calls   int
	samples []PartitionLag
	err     error
	errOnce bool
}

func (r *fakeLagReader) SampleLag(context.Context) ([]PartitionLag, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls++
	if r.err != nil {
		err := r.err
		if r.errOnce {
			r.err = nil
		}
		return nil, err
	}
	return append([]PartitionLag(nil), r.samples...), nil
}

func (r *fakeLagReader) callCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.calls
}

type recordingLagObserver struct {
	mu      sync.Mutex
	samples []PartitionLag
}

func (o *recordingLagObserver) PartitionLagObserved(sample PartitionLag) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.samples = append(o.samples, sample)
}

func (o *recordingLagObserver) snapshot() []PartitionLag {
	o.mu.Lock()
	defer o.mu.Unlock()
	return append([]PartitionLag(nil), o.samples...)
}

func TestLagMonitorEmitsImmediateSampleAndStopsOnCancel(t *testing.T) {
	t.Parallel()
	reader := &fakeLagReader{samples: []PartitionLag{
		{Topic: "events", Partition: 0, CommittedOffset: 4, LogEndOffset: 9, Lag: 5},
		{Topic: "events", Partition: 1, CommittedOffset: 7, LogEndOffset: 7, Lag: 0},
	}}
	observer := &recordingLagObserver{}
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(100 * time.Millisecond)
		cancel()
	}()
	monitor := &LagMonitor{Reader: reader, Observer: observer, Interval: 5 * time.Second,
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	if err := monitor.Run(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("Run() error = %v, want context.Canceled", err)
	}
	samples := observer.snapshot()
	if len(samples) != 2 {
		t.Fatalf("observer got %d samples, want 2", len(samples))
	}
	if samples[0].Lag != 5 || samples[1].Partition != 1 {
		t.Fatalf("unexpected samples: %+v", samples)
	}
	if reader.callCount() != 1 {
		t.Fatalf("reader calls = %d, want 1 immediate sample", reader.callCount())
	}
}

func TestLagMonitorSampleSwallowsErrorAndRecovers(t *testing.T) {
	t.Parallel()
	reader := &fakeLagReader{
		err:     errors.New("broker unavailable"),
		errOnce: true,
		samples: []PartitionLag{{Topic: "events", Partition: 0, CommittedOffset: 1, LogEndOffset: 2, Lag: 1}},
	}
	observer := &recordingLagObserver{}
	monitor := &LagMonitor{Reader: reader, Observer: observer,
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	// A failed sample must not propagate: the next tick can retry.
	monitor.sample(context.Background())
	if len(observer.snapshot()) != 0 || reader.callCount() != 1 {
		t.Fatal("failed sample must not reach the observer")
	}
	monitor.sample(context.Background())
	samples := observer.snapshot()
	if len(samples) != 1 || samples[0].Lag != 1 {
		t.Fatalf("retry sample not delivered: %+v", samples)
	}
}

func TestLagMonitorNilObserverIsNoop(t *testing.T) {
	t.Parallel()
	reader := &fakeLagReader{samples: []PartitionLag{{Topic: "events", Partition: 0, Lag: 1}}}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	monitor := &LagMonitor{Reader: reader, Interval: 5 * time.Second,
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	if err := monitor.Run(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("Run() error = %v, want context.Canceled", err)
	}
}

func TestLagMonitorValidatesConstruction(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	if err := (&LagMonitor{Interval: time.Second}).Run(ctx); err == nil {
		t.Fatal("Run without reader must fail")
	}
	reader := &fakeLagReader{}
	for _, interval := range []time.Duration{time.Second, 11 * time.Minute} {
		if err := (&LagMonitor{Reader: reader, Interval: interval}).Run(ctx); err == nil {
			t.Fatalf("interval %s must be rejected", interval)
		}
	}
}

func TestNoopLagObserver(t *testing.T) {
	t.Parallel()
	NoopLagObserver{}.PartitionLagObserved(PartitionLag{Lag: 99})
}
