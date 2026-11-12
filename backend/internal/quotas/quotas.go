// Package quotas owns durable per-project collection admission overrides.
// The Redis budget engine remains the runtime enforcer; this package only
// persists owner-approved policies and the revision/idempotency ledger around
// them. A missing row means the static serve default policy stays effective.
package quotas

import "context"

// CreateRevision is the expect-absent If-Match value for a first override.
const CreateRevision int64 = 0

// Policy is one persisted project/environment override.
type Policy struct {
	ProjectID   string `json:"project_id"`
	Environment string `json:"environment"`
	// Rate is events per second; Burst is the whole-batch token capacity.
	Rate        float64 `json:"rate_per_second"`
	Burst       int     `json:"burst"`
	Revision    int64   `json:"revision"`
	CreatedAt   string  `json:"created_at"`
	UpdatedAt   string  `json:"updated_at"`
}

// Input is an owner-scoped upsert request with its idempotency ledger fields.
type Input struct {
	ProjectID      string
	ActorID        string
	Environment    string
	Rate           float64
	Burst          int
	IdempotencyKey string
	RequestHash    string
}

// Store is the persistence port satisfied by the PostgreSQL store.
type Store interface {
	// GetQuota returns configured=false when no override exists for the scope.
	GetQuota(ctx context.Context, actor, project, environment string) (Policy, bool, error)
	// PutQuota inserts (expectedRevision==CreateRevision) or updates an override,
	// rejecting a stale or concurrently-created revision as a conflict.
	PutQuota(ctx context.Context, in Input, expectedRevision int64) (Policy, error)
	// ListQuotas has no human actor: the serve process loads every override at boot.
	ListQuotas(ctx context.Context) ([]Policy, error)
}
