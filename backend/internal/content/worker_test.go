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
