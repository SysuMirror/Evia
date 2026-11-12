// Package archiving materializes expired facts into bounded, checksummed
// artifacts before retention deletes them. A manifest records every archive's
// scope, coverage and integrity fingerprint so future backfills can locate
// and verify the data outside ClickHouse. The package owns the lifecycle
// vocabulary and fail-closed orchestration; ClickHouse reads, filesystem
// writes and PostgreSQL persistence live behind ports.
package archiving

import (
	"context"
	"io"
	"time"
)

const (
	StatusPending      = "pending"
	StatusMaterialized = "materialized"
	StatusFailed       = "failed"
)

const (
	// MaxArchiveRows bounds a single archive so a runaway scope cannot exhaust
	// disk or memory. A scope over the cap fails closed: nothing is archived
	// and the linked retention sweep must not delete the unarchived facts.
	MaxArchiveRows int64 = 100_000
	// MaxArtifactBytes is the serialized JSONL size ceiling for one archive.
	MaxArtifactBytes int64 = 256 * 1024 * 1024
	// MaxNameLength mirrors the other lifecycle job tables.
	MaxNameLength = 200
)

// Scope identifies the exact fact slice an archive must cover. Cutoff is the
// same exclusive lower bound the retention deletion uses, so an archived-then-
// deleted scope can never lose or duplicate a row at the boundary.
type Scope struct {
	ProjectID     string
	Environment   string
	RetentionDays int
	Cutoff        time.Time
	SweepID       string
}

// Manifest is the durable recovery index for one archive operation.
type Manifest struct {
	ManifestID    string
	ProjectID     string
	Environment   string
	RetentionDays int
	Cutoff        time.Time
	WindowStart   *time.Time
	WindowEnd     *time.Time
	RowCount      int64
	ArtifactPath  *string
	ArtifactSHA   *string
	ArtifactBytes *int64
	Status        string
	ErrorCode     *string
	SweepID       *string
	CreatedAt     time.Time
	CompletedAt   *time.Time
}

// Artifact describes a materialized JSONL file.
type Artifact struct {
	Path   string
	SHA256 string
	Bytes  int64
}

// ListFilter constrains cursor pagination over a project's manifests.
type ListFilter struct {
	After       string
	Limit       int
	Environment string
}

// Page is a cursor-ready manifest collection ordered by creation time.
type Page struct {
	Items []Manifest
	After string
}

// FactWindow reports the streamed row count and time coverage.
type FactWindow struct {
	Rows        int64
	WindowStart time.Time
	WindowEnd   time.Time
}

// FactReader is the ClickHouse-side port. Implementations stream the exact
// rows a retention deletion with the same cutoff would remove.
type FactReader interface {
	CountExpired(ctx context.Context, project, environment string, cutoff time.Time) (int64, error)
	// StreamExpired writes JSONEachRow rows older than cutoff into sink and
	// returns the row count plus observed event_time coverage.
	StreamExpired(ctx context.Context, sink io.Writer, scope Scope) (FactWindow, error)
}

// ArtifactStore persists the serialized archive and fingerprints it.
type ArtifactStore interface {
	// WriteStream opens the artifact for manifestID and invokes stream with a
	// writer that tees every byte into a sha256 hasher. The byte ceiling is
	// enforced while streaming so large scopes fail without buffering. The
	// stream callback returns the coverage observed while reading facts.
	WriteStream(ctx context.Context, manifestID string, stream func(io.Writer) (FactWindow, error)) (Artifact, error)
}

// ManifestStore is the persistence contract implemented by PostgreSQL.
type ManifestStore interface {
	CreateManifest(ctx context.Context, manifest Manifest) error
	MarkSucceeded(ctx context.Context, manifestID string, artifact Artifact, window FactWindow, rowCount int64, completedAt time.Time) error
	MarkFailed(ctx context.Context, manifestID, code string, completedAt time.Time) error
	ListManifests(ctx context.Context, actor, project string, filter ListFilter) (Page, error)
	GetManifest(ctx context.Context, actor, project, manifestID string) (Manifest, error)
}
