package pipeline

import (
	"context"
	"errors"
	"log/slog"
	"time"
)

// PartitionLag is one point-in-time consumer lag sample for a topic partition
// assigned to this worker. CommittedOffset is the next offset the group will
// consume; LogEndOffset is the broker log end offset; Lag is their difference.
type PartitionLag struct {
	Topic           string
	Partition       int32
	CommittedOffset int64
	LogEndOffset    int64
	Lag             int64
}

// LagObserver receives consumer lag samples without influencing ingestion.
// Implementations must be safe for concurrent use and must never return an
// error: a metrics outage cannot block or fail message durability.
type LagObserver interface {
	// PartitionLagObserved reports one sample per assigned partition with a
	// known committed position.
	PartitionLagObserved(sample PartitionLag)
}

// NoopLagObserver discards every sample. It keeps the monitor constructible in
// tests and in deployments that do not wire telemetry.
type NoopLagObserver struct{}

func (NoopLagObserver) PartitionLagObserved(PartitionLag) {}

// LagReader reports lag for the partitions currently assigned to this
// process. An empty slice with no error means the group holds no assignment
// with a known committed position yet, for example right after a rebalance.
type LagReader interface {
	SampleLag(ctx context.Context) ([]PartitionLag, error)
}

// LagMonitor polls a LagReader on a fixed interval and forwards samples to the
// observer. Sampling failures are logged and retried on the next tick rather
// than stopping the task, because telemetry must never take ingestion down.
type LagMonitor struct {
	Reader   LagReader
	Observer LagObserver
	Interval time.Duration
	Logger   *slog.Logger
}

const (
	minLagInterval = 5 * time.Second
	maxLagInterval = 10 * time.Minute
)

func (m *LagMonitor) observer() LagObserver {
	if m.Observer == nil {
		return NoopLagObserver{}
	}
	return m.Observer
}

func (m *LagMonitor) logger() *slog.Logger {
	if m.Logger == nil {
		return slog.Default()
	}
	return m.Logger
}

// Run samples once immediately so a stuck consumer is visible without waiting
// for the first tick, then samples on every interval until ctx is cancelled.
func (m *LagMonitor) Run(ctx context.Context) error {
	if m.Reader == nil {
		return errors.New("lag monitor requires a reader")
	}
	if m.Interval < minLagInterval || m.Interval > maxLagInterval {
		return errors.New("lag monitor interval must be between 5s and 10m")
	}
	m.sample(ctx)
	ticker := time.NewTicker(m.Interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			m.sample(ctx)
		}
	}
}

func (m *LagMonitor) sample(ctx context.Context) {
	samples, err := m.Reader.SampleLag(ctx)
	if err != nil {
		// Cancellation is the normal shutdown path; only genuine sampling
		// failures deserve a warning.
		if ctx.Err() == nil {
			m.logger().Warn("consumer lag sampling failed", "error", err)
		}
		return
	}
	observer := m.observer()
	for _, sample := range samples {
		observer.PartitionLagObserved(sample)
	}
}
