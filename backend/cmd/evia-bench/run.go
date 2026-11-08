package main

import (
	"context"
	"sync"
	"time"
)

func run(ctx context.Context, c Config, o Options) Report {
	started := time.Now()
	replayEnd := o.Events * o.ReplayPercent / 100
	m := &recorder{report: Report{
		Version: 1, Mode: "closed_loop_concurrency", RunID: c.RunID, SourceKind: c.SourceKind, EventKey: c.EventKey,
		ProjectID: c.ProjectID, SourceID: c.SourceID, Environment: c.Environment, SchemaVersion: c.SchemaVersion, ReleaseRevision: c.ReleaseRevision,
		Expected:        expectedFacts(o.Events, c.UIDCardinality),
		RequestedUnique: o.Events, ExpectedUIDs: min(o.Events, c.UIDCardinality), PlannedReplay: replayEnd,
		BatchSize: o.BatchSize, Concurrency: o.Concurrency, TargetRate: o.Rate,
		WindowStart: c.StartedAt.Add(-lookback).Format(time.RFC3339Nano), WindowEnd: c.StartedAt.Format(time.RFC3339Nano),
		StartedAt: started.UTC().Format(time.RFC3339Nano), HTTPStatuses: make(map[int]int), Failures: []Failure{},
		RetryPolicy: "none; 429 Retry-After observed as admission failure; replay is an explicit separate phase",
	}}
	if o.Rate > 0 {
		m.report.Mode = "open_loop_target_rate_drop_when_busy"
	}
	client := httpClient(o.Concurrency)
	defer client.CloseIdleConnections()
	jobs := make(chan batch)
	var workers, phase sync.WaitGroup
	for range o.Concurrency {
		workers.Go(func() {
			for b := range jobs {
				sendBatch(ctx, client, c, b, m)
				phase.Done()
			}
		})
	}
	scheduled := 0
	produce := func(end int, replay bool) {
		for i := 0; i < end; {
			if ctx.Err() != nil {
				return
			}
			n := batchCount(i, end, o.BatchSize, replayEnd)
			if o.Rate > 0 {
				due := started.Add(time.Duration(float64(scheduled) / o.Rate * float64(time.Second)))
				if wait := time.Until(due); wait > 0 {
					timer := time.NewTimer(wait)
					select {
					case <-ctx.Done():
						timer.Stop()
						return
					case <-timer.C:
					}
				}
				m.mu.Lock()
				m.report.MaxScheduleLag = max(m.report.MaxScheduleLag, float64(time.Since(due))/float64(time.Millisecond))
				m.mu.Unlock()
			}
			b := makeBatch(c, o, i, n, replay)
			phase.Add(1)
			if o.Rate == 0 {
				select {
				case jobs <- b:
				case <-ctx.Done():
					phase.Done()
					return
				}
			} else {
				select {
				case jobs <- b:
				default:
					phase.Done()
					m.mu.Lock()
					m.counts(replay).Dropped += n
					m.mu.Unlock()
				}
			}
			i += n
			scheduled += n
		}
	}
	produce(o.Events, false)
	phase.Wait()
	produce(replayEnd, true)
	phase.Wait()
	close(jobs)
	workers.Wait()
	finished := time.Now()
	r := m.report
	r.FinishedAt = finished.UTC().Format(time.RFC3339Nano)
	r.Elapsed = finished.Sub(started).Seconds()
	r.Latency = m.summarizeLatency()
	r.RequestsPerSecond = float64(r.Requests) / r.Elapsed
	r.UniqueAcceptedPerSecond = float64(r.Unique.Accepted) / r.Elapsed
	r.Complete = ctx.Err() == nil && r.Unique.Accepted == o.Events && r.Replay.Accepted == replayEnd
	r.StopReason = "all_attempts_finished_with_failures"
	if r.Complete {
		r.StopReason = "completed"
	}
	if ctx.Err() == context.Canceled {
		r.StopReason = "interrupted"
	}
	if ctx.Err() == context.DeadlineExceeded {
		r.StopReason = "deadline_exceeded"
	}
	return r
}
