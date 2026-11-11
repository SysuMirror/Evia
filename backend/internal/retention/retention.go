// Package retention models per-environment event retention policies. A policy
// declares how long event facts must remain queryable; it is configuration only.
// Applying the window to the fact store is the job of a separate, audited
// enforcement component that must preserve the erasure resurrection barrier.
package retention

import (
	"context"
	"regexp"

	"github.com/howtomakeaname/Evia/backend/internal/access"
	"github.com/howtomakeaname/Evia/backend/internal/platform/fault"
)

const (
	// MinRetentionDays guards against an accidentally near-immediate destructive
	// window; MaxRetentionDays bounds the column CHECK and keeps windows sane.
	MinRetentionDays = 30
	MaxRetentionDays = 3650
	// CreateRevision means "no policy row exists yet"; an optimistic create must
	// fail if another owner published one concurrently.
	CreateRevision int64 = 0
)

var uuidPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// Policy is the durable retention configuration for one project/environment.
type Policy struct {
	ProjectID     string
	Environment   string
	RetentionDays int
	Revision      int64
	CreatedAt     string
	UpdatedAt     string
}

// Input carries an owner-accepted retention write from the HTTP boundary.
type Input struct {
	ProjectID      string
	ActorID        string
	Environment    string
	RetentionDays  int
	IdempotencyKey string
	RequestHash    string
}

// Store is the persistence contract implemented by PostgreSQL. Reads and
// writes re-authorize the human owner inside their own transactions.
type Store interface {
	// GetPolicy returns configured=false when no policy exists for the scope.
	GetPolicy(ctx context.Context, actor, project, environment string) (policy Policy, configured bool, err error)
	// PutPolicy inserts (expectedRevision==CreateRevision) or updates a policy,
	// rejecting a stale or concurrently-created revision as a conflict.
	PutPolicy(ctx context.Context, in Input, expectedRevision int64) (Policy, error)
}

// Service holds domain validation independent of transport and storage.
type Service struct{ store Store }

func NewService(store Store) *Service { return &Service{store: store} }

func caller(ctx context.Context, actor, project string) error {
	if _, machine := access.IdentityFromContext(ctx); machine {
		return fault.New(fault.Forbidden, "MACHINE_FORBIDDEN", "机器主体无权管理保留策略")
	}
	if !uuidPattern.MatchString(actor) {
		return fault.New(fault.Unauthenticated, "SESSION_REQUIRED", "请先登录")
	}
	if !uuidPattern.MatchString(project) {
		return fault.New(fault.Invalid, "INVALID_PROJECT_ID", "项目标识格式不正确")
	}
	return nil
}

func validEnvironment(environment string) bool {
	return environment == "development" || environment == "staging" || environment == "production"
}

func validate(in Input, expectedRevision int64) error {
	if !uuidPattern.MatchString(in.ProjectID) || !uuidPattern.MatchString(in.ActorID) {
		return fault.New(fault.Invalid, "INVALID_RETENTION_INPUT", "项目或操作主体标识格式不正确")
	}
	if !validEnvironment(in.Environment) {
		return fault.New(fault.Invalid, "INVALID_RETENTION_INPUT", "环境标识无效")
	}
	if in.RetentionDays < MinRetentionDays || in.RetentionDays > MaxRetentionDays {
		return fault.New(fault.Invalid, "INVALID_RETENTION_INPUT", "保留天数必须在 30 到 3650 天之间")
	}
	if expectedRevision < CreateRevision || expectedRevision > 9007199254740991 {
		return fault.New(fault.Invalid, "REVISION_REQUIRED", "必须提供有效修订")
	}
	if len(in.IdempotencyKey) < 1 || len(in.IdempotencyKey) > 128 {
		return fault.New(fault.Invalid, "INVALID_RETENTION_INPUT", "幂等键长度无效")
	}
	if len(in.RequestHash) != 64 {
		return fault.New(fault.Invalid, "INVALID_RETENTION_INPUT", "请求摘要无效")
	}
	return nil
}

// Get returns the current policy for a scope. An unconfigured scope is reported
// with configured=false rather than a 404 so callers can render a create form.
func (s *Service) Get(ctx context.Context, actor, project, environment string) (Policy, bool, error) {
	if e := caller(ctx, actor, project); e != nil {
		return Policy{}, false, e
	}
	if !validEnvironment(environment) {
		return Policy{}, false, fault.New(fault.Invalid, "INVALID_RETENTION_INPUT", "环境标识无效")
	}
	return s.store.GetPolicy(ctx, actor, project, environment)
}

// Put validates and persists an owner retention write with optimistic revision
// control; identical idempotent replays are resolved inside the store.
func (s *Service) Put(ctx context.Context, in Input, expectedRevision int64) (Policy, error) {
	if e := caller(ctx, in.ActorID, in.ProjectID); e != nil {
		return Policy{}, e
	}
	if e := validate(in, expectedRevision); e != nil {
		return Policy{}, e
	}
	return s.store.PutPolicy(ctx, in, expectedRevision)
}
