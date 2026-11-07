package delivery

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"
)

// Worker renews leases using actual pipeline readback. A failed refresh leaves
// the existing expiry unchanged, so readiness cannot outlive a stopped worker.
type Worker struct {
	Service  *Service
	Interval time.Duration
	Logger   *slog.Logger
}

func (w *Worker) Run(ctx context.Context) error {
	if w.Service == nil {
		return errors.New("delivery worker requires service")
	}
	interval := w.Interval
	if interval == 0 {
		interval = 10 * time.Second
	}
	if interval < time.Second || interval > 20*time.Second {
		return errors.New("invalid delivery refresh interval")
	}
	logger := w.Logger
	if logger == nil {
		logger = slog.Default()
	}
	for {
		scanCtx, scanCancel := context.WithTimeout(ctx, 2*time.Second)
		candidates, e := w.Service.store.ListReadiness(scanCtx, 8)
		scanCancel()
		if e != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			logger.Warn("release readiness scan failed")
		}
		var wg sync.WaitGroup
		slots := make(chan struct{}, 4)
		for _, candidate := range candidates {
			select {
			case slots <- struct{}{}:
			case <-ctx.Done():
				wg.Wait()
				return ctx.Err()
			}
			wg.Add(1)
			go func() {
				defer wg.Done()
				defer func() { <-slots }()
				if e := w.Service.Renew(ctx, candidate); e != nil && ctx.Err() == nil {
					logger.Warn("release readiness renewal failed", "release_id", candidate.ReleaseID)
				}
			}()
		}
		wg.Wait()
		timer := time.NewTimer(interval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}
