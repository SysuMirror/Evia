// Package erasure models right-to-erasure jobs and the durable tombstone
// barrier that stops erased identities from being reintroduced by queue
// replays or source retries. The ClickHouse deletion mechanism and the write
// worker barrier live in their own adapters; this package owns the lifecycle
// vocabulary and storage contract.
package erasure

import (
	"context"
	"time"
)

const (
	StateQueued     = "queued"
	StateHiding     = "hiding"
	StatePurging    = "purging"
	StateRebuilding = "rebuilding"
	StateCompleted  = "completed"
	StateFailed     = "failed"
)

// ErasureStates is the single Go-side source of truth for the six erasure
// job lifecycle states, mirrored by the ErasureJob.state OpenAPI enum and
// guarded by TestErasureStateVocabularyMatchesContract. Raw SQL keeps its own
// string literals because they live inside query text and are covered by the
// erasure_jobs CHECK constraint in migration 015; every Go-side comparison or
// Job.State assignment must use the State* constants.
var ErasureStates = []string{
	StateQueued, StateHiding, StatePurging,
	StateRebuilding, StateCompleted, StateFailed,
}

const (
	SubjectUID         = "uid"
	SubjectAnonymousID = "anonymous_id"
)

// MaxAttempts bounds worker retries before a job is parked as failed.
const MaxAttempts = 5

// Job is the durable erasure lifecycle record.
type Job struct {
	JobID           string
	ProjectID       string
	Environment     string
	ActorID         string
	SubjectKind     string
	SubjectValue    string
	Reason          string
	State           string
	IdempotencyKey  string
	RequestHash     string
	BackupExpiryAt  *time.Time
	ErrorCode       *string
	Attempts        int
	LeaseGeneration int64
	LeaseOwner      *string
	LeaseUntil      *time.Time
	CreatedAt       time.Time
	UpdatedAt       time.Time
	FinishedAt      *time.Time
}

// Input carries a validated erasure request accepted at the HTTP boundary.
type Input struct {
	ProjectID      string
	Environment    string
	ActorID        string
	SubjectKind    string
	SubjectValue   string
	Reason         string
	IdempotencyKey string
	RequestHash    string
	BackupExpiryAt *time.Time
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

// Claim is a leased queued job handed to an erasure worker.
type Claim struct {
	Job
	Worker     string
	Generation int64
}

// ErasedSet reports which requested identities carry a tombstone. The write
// worker drops every record whose uid or anonymous_id is present.
type ErasedSet struct {
	UIDs         map[string]struct{}
	AnonymousIDs map[string]struct{}
}

func (s ErasedSet) Contains(kind, value string) bool {
	switch kind {
	case SubjectUID:
		_, ok := s.UIDs[value]
		return ok
	case SubjectAnonymousID:
		_, ok := s.AnonymousIDs[value]
		return ok
	}
	return false
}

// Store is the persistence contract implemented by PostgreSQL.
type Store interface {
	// CreateJob records a queued job or replays the accepted snapshot for the
	// same actor + idempotency key. A second in-flight job for the same target
	// identity conflicts. The boolean reports whether a new row was inserted.
	CreateJob(ctx context.Context, in Input) (Job, bool, error)
	GetJob(ctx context.Context, actor, project, jobID string) (Job, error)
	ListJobs(ctx context.Context, actor, project string, filter ListFilter) (Page, error)
	// ClaimJob leases the oldest queued job using SKIP LOCKED semantics.
	ClaimJob(ctx context.Context, worker string, lease time.Duration) (*Claim, error)
	// BeginHiding moves queued to hiding and inserts the permanent tombstone
	// atomically, so the barrier exists before the storage delete is issued.
	BeginHiding(ctx context.Context, claim Claim) error
	// Advance enforces the hiding -> purging -> rebuilding progression.
	Advance(ctx context.Context, claim Claim, state string) error
	Complete(ctx context.Context, claim Claim) error
	Fail(ctx context.Context, claim Claim, code string) error
	// ErasedSubjects resolves tombstones for a batch of candidate identities.
	ErasedSubjects(ctx context.Context, project, environment string, uids, anonymousIDs []string) (ErasedSet, error)
}
