// Package content owns the encrypted-sensitive-content processing boundary.
// Browsers submit opaque sys.content_chunk events; the collector/Kafka/fact
// path never decrypts them. A dedicated worker unwraps each message key through
// a KeyProvider, authenticates AES-GCM chunks, writes plaintext to a protected
// artifact store and indexes only safe metadata in PostgreSQL.
package content

import (
	"context"
	"time"
)

const MaxScanBatch = 200

type Cursor struct {
	ReceivedAt time.Time
	EventID    string
}

type EncryptedChunk struct {
	ProjectID, Environment, EventID          string
	ReceivedAt                               time.Time
	ContentType, StreamID, ItemID, ActorType string
	ChunkIndex, ChunkCount                   int
	Ciphertext, Nonce, WrappedKey            string
	KeyID, Algorithm                         string
	AADVersion, Padding                      string
	ContentLength                            int
	Truncated                                bool
	Status                                   string
	DurationMS                               int
}

type DecryptedChunk struct {
	EncryptedChunk
	Plaintext []byte
}

type Record struct {
	ProjectID, Environment, EventID          string
	ReceivedAt                               time.Time
	ContentType, StreamID, ItemID, ActorType string
	ChunkIndex, ChunkCount                   int
	ObjectKey, SHA256, KeyID                 string
	ContentLength                            int
	Truncated                                bool
	Status                                   string
	DurationMS                               int
}

type Claim struct {
	Worker     string
	Generation int64
	Cursor     Cursor
}

type Item struct {
	ProjectID, Environment                   string
	ContentType, StreamID, ItemID, ActorType string
	Content                                  string
	Truncated                                bool
	ContentLength                            int
	KeyID                                    string
	ReceivedAt                               time.Time
	Status                                   string
	DurationMS                               int
}

type Scanner interface {
	ScanEncryptedContent(context.Context, Cursor, int) ([]EncryptedChunk, error)
}

type Decryptor interface {
	Decrypt(context.Context, EncryptedChunk) (DecryptedChunk, error)
}

type ArtifactStore interface {
	Put(context.Context, DecryptedChunk) (objectKey, sha256 string, err error)
	Read(context.Context, string) ([]byte, error)
}

type Store interface {
	ClaimContent(context.Context, string, time.Duration) (*Claim, error)
	CompleteContent(context.Context, Claim, []Record, Cursor) error
	ReleaseContent(context.Context, Claim) error
	GetContentItem(context.Context, string, string, string, string) ([]Record, error)
}
