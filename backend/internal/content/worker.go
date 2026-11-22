package content

import (
	"context"
	"errors"
	"log/slog"
	"time"
)

type Worker struct {
	Store     WorkerStore
	Scanner   Scanner
	Decryptor Decryptor
	Artifacts ArtifactStore
	ID        string
	Lease     time.Duration
	Interval  time.Duration
	Logger    *slog.Logger
}

func (w *Worker) Run(ctx context.Context) error {
	interval := w.Interval
	if interval <= 0 {
		interval = 5 * time.Second
	}
	for {
		processed, err := w.runOnce(ctx)
		if err != nil && !errors.Is(err, context.Canceled) && w.Logger != nil {
			w.Logger.Warn("content processing round failed; cursor not advanced", "error", err)
		}
		// A full bounded scan proves more backlog may already be visible. Continue
		// immediately instead of imposing the idle polling interval on every 200
		// chunks; partial/empty/error rounds retain the normal backoff.
		if err == nil && processed == MaxScanBatch {
			continue
		}
		timer := time.NewTimer(interval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}

func (w *Worker) RunOnce(ctx context.Context) error {
	_, err := w.runOnce(ctx)
	return err
}

func (w *Worker) runOnce(ctx context.Context) (int, error) {
	lease := w.Lease
	if lease <= 0 {
		lease = time.Minute
	}
	claim, err := w.Store.ClaimContent(ctx, w.ID, lease)
	if err != nil || claim == nil {
		return 0, err
	}
	chunks, err := w.Scanner.ScanEncryptedContent(ctx, claim.Cursor, MaxScanBatch)
	if err != nil {
		_ = w.Store.ReleaseContent(context.Background(), *claim)
		return 0, err
	}
	if len(chunks) == 0 {
		return 0, w.Store.ReleaseContent(ctx, *claim)
	}
	records := make([]Record, 0, len(chunks))
	cursor := claim.Cursor
	for _, encrypted := range chunks {
		decrypted, decryptErr := w.Decryptor.Decrypt(ctx, encrypted)
		if decryptErr != nil {
			_ = w.Store.ReleaseContent(context.Background(), *claim)
			return 0, decryptErr
		}
		objectKey, hash, putErr := w.Artifacts.Put(ctx, decrypted)
		if putErr != nil {
			_ = w.Store.ReleaseContent(context.Background(), *claim)
			return 0, putErr
		}
		records = append(records, Record{
			ProjectID: encrypted.ProjectID, Environment: encrypted.Environment,
			EventID: encrypted.EventID, ReceivedAt: encrypted.ReceivedAt,
			ContentType: encrypted.ContentType,
			StreamID:    encrypted.StreamID, ItemID: encrypted.ItemID, ActorType: encrypted.ActorType,
			ChunkIndex: encrypted.ChunkIndex, ChunkCount: encrypted.ChunkCount,
			ObjectKey: objectKey, SHA256: hash, KeyID: encrypted.KeyID,
			ContentLength: encrypted.ContentLength, Truncated: encrypted.Truncated,
			Status: encrypted.Status, DurationMS: encrypted.DurationMS,
		})
		cursor = Cursor{ReceivedAt: encrypted.ReceivedAt, EventID: encrypted.EventID}
	}
	if err = w.Store.CompleteContent(ctx, *claim, records, cursor); err != nil {
		return 0, err
	}
	return len(chunks), nil
}
