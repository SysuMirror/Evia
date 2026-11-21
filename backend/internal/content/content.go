// Package content owns the encrypted-sensitive-content processing boundary.
// Browsers submit opaque chat_content_chunk events; the collector/Kafka/fact
// path never decrypts them. A dedicated worker unwraps each message key through
// a KeyProvider, authenticates AES-GCM chunks, writes plaintext to a protected
// artifact store and indexes only safe metadata in PostgreSQL.
package content

import (
	"context"
	"time"
)

const (
	RoleUser      = "user"
	RoleAssistant = "assistant"
	MaxScanBatch  = 200
)

type Cursor struct {
	ReceivedAt time.Time
	EventID    string
}

type EncryptedChunk struct {
	ProjectID, Environment, EventID string
	ReceivedAt                      time.Time
	ConversationID, MessageID, Role string
	ChunkIndex, ChunkCount          int
	Ciphertext, Nonce, WrappedKey   string
	KeyID, Algorithm                string
	AADVersion, Padding             string
	ContentLength                   int
	Truncated                       bool
	ResponseStatus                  string
	DurationMS                      int
}

type DecryptedChunk struct {
	EncryptedChunk
	Plaintext []byte
}

type Record struct {
	ProjectID, Environment, EventID string
	ReceivedAt                      time.Time
	ConversationID, MessageID, Role string
	ChunkIndex, ChunkCount          int
	ObjectKey, SHA256, KeyID        string
	ContentLength                   int
	Truncated                       bool
	ResponseStatus                  string
	DurationMS                      int
}

type Claim struct {
	Worker     string
	Generation int64
	Cursor     Cursor
}

type Message struct {
	ProjectID, Environment          string
	ConversationID, MessageID, Role string
	Content                         string
	Truncated                       bool
	ContentLength                   int
	KeyID                           string
	ReceivedAt                      time.Time
	ResponseStatus                  string
	DurationMS                      int
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
	GetContentMessage(context.Context, string, string, string, string) ([]Record, error)
}
