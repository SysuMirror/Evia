// Package content owns the encrypted-sensitive-content processing boundary.
// Browsers submit opaque sys.content_chunk events; the collector/Kafka/fact
// path never decrypts them. A dedicated worker unwraps each message key through
// a KeyProvider, authenticates AES-GCM chunks, writes plaintext to a protected
// artifact store and indexes only safe metadata in PostgreSQL.
package content

import (
	"context"
	"encoding/base64"
	"time"
)

const (
	MaxScanBatch               = 200
	MaxContentChunks           = 32
	MaxPlaintextChunkLength    = 4094 // fixed-4096-v2 block minus the authenticated two-byte length
	MaxCiphertextEncodedLength = 5483 // base64url(4096 padded bytes + 16-byte GCM tag)
)

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

const (
	AuditActionItemRead  = "content.item.read"
	AccessOutcomeSuccess = "success"
	AccessOutcomeFailure = "failure"
)

type AccessAudit struct {
	ActorID, ProjectID, Environment        string
	ContentType, StreamID, ItemID, Outcome string
}

// AccessResourceKey is safe operational metadata. It deliberately excludes
// plaintext, ciphertext, object keys, key identifiers and credential material.
func accessKeyPart(value string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(value))
}

func AccessResourcePrefix(streamID string) string {
	return "content.item?stream=" + accessKeyPart(streamID) + "&"
}

func AccessResourceKey(contentType, streamID, itemID string) string {
	return AccessResourcePrefix(streamID) + "type=" + accessKeyPart(contentType) + "&item=" + accessKeyPart(itemID)
}

type DiscoveryPosition struct {
	ReceivedAt time.Time
	ID         string
}

type StreamSummary struct {
	StreamID                 string
	ContentTypes, ActorTypes []string
	FirstReceivedAt          time.Time
	LastReceivedAt           time.Time
	ItemCount                int
}

type ItemSummary struct {
	ContentType, StreamID, ItemID, ActorType string
	ReceivedAt                               time.Time
	ChunkCount, ContentLength                int
	Truncated                                bool
	Status                                   string
	DurationMS                               int
}

type StreamPage struct {
	Items []StreamSummary
	Next  *DiscoveryPosition
}

type ItemPage struct {
	Items []ItemSummary
	Next  *DiscoveryPosition
}

type DiscoveryFilter struct {
	ContentTypes []string
	AsOf         time.Time
	After        *DiscoveryPosition
	Limit        int
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

type WorkerStore interface {
	ClaimContent(context.Context, string, time.Duration) (*Claim, error)
	CompleteContent(context.Context, Claim, []Record, Cursor) error
	ReleaseContent(context.Context, Claim) error
}

type ReadStore interface {
	ListContentStreams(context.Context, string, string, string, DiscoveryFilter) (StreamPage, error)
	ListContentItems(context.Context, string, string, string, string, DiscoveryFilter) (ItemPage, error)
	GetContentItem(context.Context, string, string, string, string) ([]Record, error)
}

type AccessAuditStore interface {
	RecordContentAccess(context.Context, AccessAudit) error
}

type Store interface {
	ReadStore
	AccessAuditStore
}
