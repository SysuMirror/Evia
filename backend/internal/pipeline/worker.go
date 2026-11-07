package pipeline

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/howtomakeaname/Evia/backend/internal/ingestion"
)

// Message retains the Kafka position needed for an exclusive offset commit.
type Message struct {
	Topic       string
	Partition   int32
	Offset      int64
	LeaderEpoch int32
	Value       []byte
}
type Reader interface {
	Poll(context.Context, int) ([]Message, error)
	Commit(context.Context, []Message) error
	AllowRebalance()
	Close()
}
type Sink interface {
	Write(context.Context, []ingestion.Record) error
}
type Poison struct {
	Version   int    `json:"version"`
	Topic     string `json:"topic"`
	Partition int32  `json:"partition"`
	Offset    int64  `json:"offset"`
	Code      string `json:"code"`
	Value     []byte `json:"value"`
}
type Quarantiner interface {
	Quarantine(context.Context, []Poison) error
}
type Worker struct {
	BatchTimeout time.Duration
	Reader       Reader
	Sink         Sink
	Quarantine   Quarantiner
	BatchSize    int
	RetryInitial time.Duration
	RetryMax     time.Duration
	Logger       *slog.Logger
}

// Run holds each polled batch through durable fact and quarantine writes, then
// commits offsets. Cancellation leaves unfinished offsets uncommitted for replay.
// Retries stop polling, bounding memory and propagating sink backpressure.
func (w *Worker) Run(ctx context.Context) error {
	if w.Reader == nil || w.Sink == nil || w.Quarantine == nil {
		return errors.New("pipeline worker requires reader, sink and quarantine")
	}
	batch := w.BatchSize
	if batch == 0 {
		batch = 128
	}
	if batch < 1 || batch > MaxBatchRecords {
		return errors.New("invalid pipeline batch size")
	}
	initial, max := w.RetryInitial, w.RetryMax
	if initial == 0 {
		initial = 100 * time.Millisecond
	}
	if max == 0 {
		max = 5 * time.Second
	}
	if initial < time.Millisecond || max < initial || max > time.Minute {
		return errors.New("invalid pipeline retry bounds")
	}
	batchTimeout := w.BatchTimeout
	if batchTimeout == 0 {
		batchTimeout = 30 * time.Second
	}
	if batchTimeout < time.Millisecond || batchTimeout > 45*time.Second {
		return errors.New("invalid pipeline batch processing timeout")
	}
	defer w.Reader.Close()
	defer w.Reader.AllowRebalance()
	for {
		messages, e := w.Reader.Poll(ctx, batch)
		if e != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			// A reader may return records and an error. Discarding those records without
			// committing is safe, but re-polling could advance beyond them. Stop instead.
			return e
		}
		if len(messages) == 0 {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			w.Reader.AllowRebalance()
			continue
		}
		if len(messages) > batch {
			return errors.New("reader exceeded bounded batch")
		}
		batchCtx, cancelBatch := context.WithTimeout(ctx, batchTimeout)
		e = w.process(batchCtx, messages, initial, max)
		cancelBatch()
		if e != nil {
			return e
		}

		w.Reader.AllowRebalance()
	}
}
func retry(ctx context.Context, initial, max time.Duration, fn func() error) error {
	delay := initial
	for {
		if e := ctx.Err(); e != nil {
			return e
		}
		if e := fn(); e == nil {
			return nil
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
		if delay < max {
			delay *= 2
			if delay > max {
				delay = max
			}
		}
	}
}

func (w *Worker) retryStage(ctx context.Context, initial, max time.Duration, stage string, fn func() error) error {
	logger := w.Logger
	if logger == nil {
		logger = slog.Default()
	}
	attempts := 0
	return retry(ctx, initial, max, func() error {
		e := fn()
		if e != nil {
			attempts++
			logger.Warn("pipeline operation will retry", "stage", stage, "attempt", attempts)
		}
		return e
	})
}

func (w *Worker) process(ctx context.Context, messages []Message, initial, max time.Duration) error {
	records := make([]ingestion.Record, 0, len(messages))
	poison := make([]Poison, 0)
	for _, message := range messages {
		record, e := DecodeRecord(message.Value)
		if e != nil {
			poison = append(poison, Poison{Version: 1, Topic: message.Topic, Partition: message.Partition, Offset: message.Offset, Code: "INVALID_INTERNAL_EVENT", Value: message.Value})
		} else {
			records = append(records, record)
		}
	}
	if len(records) > 0 {
		if e := w.retryStage(ctx, initial, max, "facts", func() error { return w.Sink.Write(ctx, records) }); e != nil {
			return e
		}
	}
	if len(poison) > 0 {
		if e := w.retryStage(ctx, initial, max, "quarantine", func() error { return w.Quarantine.Quarantine(ctx, poison) }); e != nil {
			return e
		}
	}
	if e := w.retryStage(ctx, initial, max, "offset_commit", func() error { return w.Reader.Commit(ctx, messages) }); e != nil {
		return e
	}
	return nil
}
