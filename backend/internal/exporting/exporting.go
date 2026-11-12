// Package exporting models bounded data export jobs that materialize
// analysis query results as auditable, downloadable artifacts. The execution
// engine, artifact storage and HTTP surface live in adapters; this package
// owns the lifecycle vocabulary, validation and persistence contract.
package exporting

import (
	"context"
	"time"

	"github.com/howtomakeaname/Evia/backend/internal/analysis"
)

const (
	StateQueued    = "queued"
	StateRunning   = "running"
	StateCompleted = "completed"
	StateFailed    = "failed"
	StateCancelled = "cancelled"
)

const (
	// FormatJSON materializes the full analysis.Result document.
	FormatJSON = "json"
	// FormatCSV renders the tabular rows with the result's column order.
	FormatCSV = "csv"
	// MaxAttempts bounds worker retries before a job is parked as failed.
	MaxAttempts = 5
	// MaxNameLength bounds the human-readable export name.
	MaxNameLength = 200
	// MaxResultRows and MaxArtifactBytes keep exports bounded even when the
	// embedded query limit is misconfigured; the worker enforces both.
	MaxResultRows    = 10000
	MaxArtifactBytes = 32 * 1024 * 1024
	// GrantTTL is the lifetime of a one-time download grant.
	GrantTTL      = 15 * time.Minute
	LeaseLostCode = "EXPORT_LEASE_LOST"
)

// Job is the durable export lifecycle record.
type Job struct {
	JobID           string
	ProjectID       string
	Environment     string
	ActorID         string
	Name            string
	Format          string
	Request         analysis.Request
	State           string
	IdempotencyKey  string
	RequestHash     string
	ErrorCode       *string
	Attempts        int
	LeaseGeneration int64
	LeaseOwner      *string
	LeaseUntil      *time.Time
	ArtifactPath    *string
	ArtifactSHA256  *string
	ArtifactBytes   *int64
	ResultRows      *int
	ExpiresAt       *time.Time
	CreatedAt       time.Time
	UpdatedAt       time.Time
	FinishedAt      *time.Time
}

// Input carries a validated export request accepted at the HTTP boundary.
type Input struct {
	ProjectID      string
	Environment    string
	ActorID        string
	Name           string
	Format         string
	Request        analysis.Request
	IdempotencyKey string
	RequestHash    string
	// ArtifactExpiry optionally bounds artifact retention; nil keeps the
	// default retention enforced by the operator.
	ArtifactExpiry *time.Time
}

// ListFilter constrains cursor pagination over a project's jobs.
type ListFilter struct {
	After string
	Limit int
}

// Page is a signed-cursor-ready job collection ordered by creation time.
type Page struct {
	Items []Job
	After string
}

// Claim is a leased queued job handed to an export worker.
type Claim struct {
	Job
	Worker     string
	Generation int64
}

// Artifact records the durable output of a successful execution.
type Artifact struct {
	Path   string
	SHA256 string
	Bytes  int64
	Rows   int
}

// Grant is a one-time download capability. Only the token hash is persisted;
// the plaintext token is returned exactly once when the grant is minted.
type Grant struct {
	TokenHash  string
	JobID      string
	ProjectID  string
	ActorID    string
	ExpiresAt  time.Time
	RedeemedAt *time.Time
	CreatedAt  time.Time
}

// Store is the persistence contract implemented by PostgreSQL.
type Store interface {
	// CreateJob records a queued job or replays the accepted snapshot for the
	// same actor + idempotency key. The boolean reports a new insertion.
	CreateExportJob(ctx context.Context, in Input) (Job, bool, error)
	GetExportJob(ctx context.Context, actor, project, jobID string) (Job, error)
	ListExportJobs(ctx context.Context, actor, project string, filter ListFilter) (Page, error)
	// CancelJob transitions a queued job to cancelled; running and terminal
	// jobs are rejected.
	CancelExportJob(ctx context.Context, actor, project, jobID string) (Job, error)
	// ClaimJob leases the oldest queued job using SKIP LOCKED semantics.
	ClaimExportJob(ctx context.Context, worker string, lease time.Duration) (*Claim, error)
	CompleteExportJob(ctx context.Context, claim Claim, artifact Artifact, expiresAt *time.Time) error
	FailExportJob(ctx context.Context, claim Claim, code string) error
	// CreateGrant persists a hashed one-time token for a completed job.
	CreateExportGrant(ctx context.Context, actor, project, jobID, tokenHash string, expiresAt time.Time) (Grant, error)
	// RedeemGrant atomically consumes an unexpired, unused token. A nil grant
	// with no error means the token is unknown, expired or already redeemed.
	RedeemExportGrant(ctx context.Context, tokenHash string) (*Grant, error)
	// GetSystemJob reads a job without an actor for the download redemption
	// path, which authenticates via the grant token rather than a session.
	GetSystemExportJob(ctx context.Context, project, jobID string) (Job, error)
}
