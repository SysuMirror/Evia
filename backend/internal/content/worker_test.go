package content

import (
	"context"
	"errors"
	"testing"
	"time"
)

type fakeStore struct {
	claim     *Claim
	records   []Record
	cursor    Cursor
	released  int
	completed int
}

func (s *fakeStore) ClaimContent(context.Context, string, time.Duration) (*Claim, error) {
	return s.claim, nil
}
func (s *fakeStore) CompleteContent(_ context.Context, _ Claim, records []Record, cursor Cursor) error {
	s.completed++
	s.records = records
	s.cursor = cursor
	return nil
}
func (s *fakeStore) ReleaseContent(context.Context, Claim) error { s.released++; return nil }
func (s *fakeStore) GetContentItem(context.Context, string, string, string, string) ([]Record, error) {
	return nil, nil
}

type fakeScanner struct {
	chunks []EncryptedChunk
	err    error
}

func (s *fakeScanner) ScanEncryptedContent(context.Context, Cursor, int) ([]EncryptedChunk, error) {
	return s.chunks, s.err
}

type sequenceScanner struct {
	batches [][]EncryptedChunk
	calls   chan int
}

func (s *sequenceScanner) ScanEncryptedContent(context.Context, Cursor, int) ([]EncryptedChunk, error) {
	index := len(s.batches)
	if index == 0 {
		s.calls <- 0
		return nil, nil
	}
	batch := s.batches[0]
	s.batches = s.batches[1:]
	s.calls <- index
	return batch, nil
}

type fakeDecryptor struct{ err error }

func (d *fakeDecryptor) Decrypt(_ context.Context, encrypted EncryptedChunk) (DecryptedChunk, error) {
	return DecryptedChunk{EncryptedChunk: encrypted, Plaintext: []byte("plain")}, d.err
}

type fakeArtifacts struct{}

func (fakeArtifacts) Put(_ context.Context, chunk DecryptedChunk) (string, string, error) {
	return "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa.chunk",
		"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", nil
}
func (fakeArtifacts) Read(context.Context, string) ([]byte, error) { return []byte("plain"), nil }

func TestWorkerDecryptsStoresAndAdvancesCursor(t *testing.T) {
	now := time.Now().UTC()
	store := &fakeStore{claim: &Claim{Worker: "worker", Generation: 1}}
	worker := &Worker{
		Store: store, Scanner: &fakeScanner{chunks: []EncryptedChunk{{
			ProjectID: "project", Environment: "development", EventID: "event", ReceivedAt: now,
			ContentType: "test_content", StreamID: "conversation", ItemID: "message", ActorType: "user",
			ChunkIndex: 0, ChunkCount: 1, KeyID: "key", ContentLength: 5,
		}}},
		Decryptor: &fakeDecryptor{}, Artifacts: fakeArtifacts{}, ID: "worker", Lease: time.Minute,
	}
	if err := worker.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if store.completed != 1 || store.released != 0 || len(store.records) != 1 || store.cursor.EventID != "event" || !store.cursor.ReceivedAt.Equal(now) {
		t.Fatalf("worker outcome wrong: completed=%d released=%d records=%+v cursor=%+v", store.completed, store.released, store.records, store.cursor)
	}
}

func TestWorkerImmediatelyContinuesAfterFullBacklogBatch(t *testing.T) {
	now := time.Now().UTC()
	chunks := make([]EncryptedChunk, MaxScanBatch)
	for index := range chunks {
		chunks[index] = EncryptedChunk{
			ProjectID: "project", Environment: "development", EventID: string(rune(index + 1)), ReceivedAt: now.Add(time.Duration(index) * time.Nanosecond),
			ContentType: "test_content", StreamID: "stream", ItemID: "item", ActorType: "browser",
			ChunkIndex: 0, ChunkCount: 1, KeyID: "key", ContentLength: 5,
		}
	}
	calls := make(chan int, 2)
	scanner := &sequenceScanner{batches: [][]EncryptedChunk{chunks, nil}, calls: calls}
	store := &fakeStore{claim: &Claim{Worker: "worker", Generation: 1}}
	worker := &Worker{
		Store: store, Scanner: scanner, Decryptor: &fakeDecryptor{}, Artifacts: fakeArtifacts{},
		ID: "worker", Lease: time.Minute, Interval: 10 * time.Second,
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- worker.Run(ctx) }()
	select {
	case <-calls:
	case <-time.After(time.Second):
		t.Fatal("first scan did not run")
	}
	select {
	case <-calls:
		// A fixed polling delay would make this wait ten seconds.
	case <-time.After(2 * time.Second):
		t.Fatal("full batch did not trigger immediate continuation")
	}
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("worker cancellation mismatch: %v", err)
	}
	if store.completed != 1 || store.released != 1 {
		t.Fatalf("unexpected rounds: completed=%d released=%d", store.completed, store.released)
	}
}

func TestWorkerFailsClosedWithoutAdvancingOnDecryptError(t *testing.T) {
	store := &fakeStore{claim: &Claim{Worker: "worker", Generation: 1}}
	worker := &Worker{
		Store: store, Scanner: &fakeScanner{chunks: []EncryptedChunk{{EventID: "poison"}}},
		Decryptor: &fakeDecryptor{err: errors.New("auth failed")}, Artifacts: fakeArtifacts{}, ID: "worker",
	}
	if err := worker.RunOnce(context.Background()); err == nil {
		t.Fatal("decrypt error must fail the round")
	}
	if store.completed != 0 || store.released != 1 {
		t.Fatalf("cursor must not advance: completed=%d released=%d", store.completed, store.released)
	}
}
