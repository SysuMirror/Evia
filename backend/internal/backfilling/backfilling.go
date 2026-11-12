// Package backfilling re-ingests facts materialized by the archiving package.
// A backfill job references one archive manifest, streams its JSONL artifact
// back into ClickHouse and records the inserted row count. The fact view
// deduplicates by event identity, so reinserted rows stay query-safe; the job
// still verifies row counts so a truncated or drifted artifact fails loudly.
package backfilling

import (
	"context"
	"io"
	"time"

	"github.com/howtomakeaname/Evia/backend/internal/archiving"
)

const (
	StateQueued    = "queued"
	StateRunning   = "running"
	StateCompleted = "completed"
	StateFailed    = "failed"
	StateCancelled = "cancelled"
)

const (
	MaxAttempts   = 5
	MaxNameLength = 200
	LeaseLostCode = "BACKFILL_LEASE_LOST"
	// Lease is the claim duration; pollInterval only spaces idle polls.
	Lease          = 2 * time.Minute
	pollInterval   = 250 * time.Millisecond
	executionLimit = 60 * time.Second
)

// Job is the durable backfill lifecycle record.
type Job struct {
	JobID           string
	ProjectID       string
	Environment     string
	ManifestID      string
	Name            string
	State           string
	IdempotencyKey  string
	RequestHash     string
	ErrorCode       *string
	RowsExpected    int64
	RowsInserted    int64
	Attempts        int
	LeaseGeneration int64
	LeaseOwner      *string
	LeaseUntil      *time.Time
	CreatedAt       time.Time
	UpdatedAt       time.Time
	FinishedAt      *time.Time
}

// Input carries an owner-accepted backfill request.
type Input struct {
	ProjectID      string
	Environment    string
	ActorID        string
	ManifestID     string
	Name           string
	IdempotencyKey string
	RequestHash    string
}

type ListFilter struct {
	After string
	Limit int
}

type Page struct {
	Items []Job
	After string
}

type Claim struct {
	Job
	Worker     string
	Generation int64
}

// ManifestReader resolves the archive manifest without a human actor; the
// worker authenticates via the leased backfill job rather than a session.
type ManifestReader interface {
	GetSystemManifest(ctx context.Context, project, manifestID string) (archiving.Manifest, error)
}

// ArchiveSource opens the JSONL artifact pinned by a manifest. Implementations
// must confine reads to the configured archive root.
type ArchiveSource interface {
	OpenArtifact(ctx context.Context, artifactPath string) (io.ReadCloser, error)
}

// FactSink re-inserts JSONEachRow facts and reports the accepted row count.
type FactSink interface {
	InsertFacts(ctx context.Context, project, environment string, reader io.Reader) (int64, error)
}

// Store is the persistence contract implemented by PostgreSQL.
type Store interface {
	CreateBackfillJob(ctx context.Context, in Input) (Job, bool, error)
	GetBackfillJob(ctx context.Context, actor, project, jobID string) (Job, error)
	ListBackfillJobs(ctx context.Context, actor, project string, filter ListFilter) (Page, error)
	CancelBackfillJob(ctx context.Context, actor, project, jobID string) (Job, error)
	ClaimBackfillJob(ctx context.Context, worker string, lease time.Duration) (*Claim, error)
	CompleteBackfillJob(ctx context.Context, claim Claim, rowsInserted int64) error
	FailBackfillJob(ctx context.Context, claim Claim, code string) error
}
