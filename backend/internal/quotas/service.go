package quotas

import (
	"context"
	"math"
	"regexp"

	"github.com/howtomakeaname/Evia/backend/internal/access"
	"github.com/howtomakeaname/Evia/backend/internal/platform/fault"
)

const (
	// MinRate/MaxRate and MinBurst/MaxBurst mirror the Redis budget engine
	// normalization bounds so a persisted override can always be applied.
	MinRate  = 0.001
	MaxRate  = 1_000_000
	MinBurst = 50
	MaxBurst = 1_000_000
)

var uuidPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// Service holds domain validation independent of transport and the Redis
// runtime enforcer.
type Service struct {
	store Store
}

func NewService(store Store) *Service { return &Service{store: store} }

func validEnvironment(environment string) bool {
	return environment == "development" || environment == "staging" || environment == "production"
}

func validate(in Input, expectedRevision int64) error {
	if !uuidPattern.MatchString(in.ProjectID) || !uuidPattern.MatchString(in.ActorID) {
		return fault.New(fault.Invalid, "INVALID_QUOTA_INPUT", "项目或操作主体标识格式不正确")
	}
	if !validEnvironment(in.Environment) {
		return fault.New(fault.Invalid, "INVALID_QUOTA_INPUT", "环境标识无效")
	}
	if math.IsNaN(in.Rate) || math.IsInf(in.Rate, 0) || in.Rate < MinRate || in.Rate > MaxRate {
		return fault.New(fault.Invalid, "INVALID_QUOTA_INPUT", "速率必须是 0.001 到 1000000 事件/秒之间的有限数")
	}
	if in.Burst < MinBurst || in.Burst > MaxBurst {
		return fault.New(fault.Invalid, "INVALID_QUOTA_INPUT", "突发容量必须在 50 到 1000000 之间")
	}
	if expectedRevision < CreateRevision || expectedRevision > 9007199254740991 {
		return fault.New(fault.Invalid, "REVISION_REQUIRED", "必须提供有效修订")
	}
	if len(in.IdempotencyKey) < 1 || len(in.IdempotencyKey) > 128 {
		return fault.New(fault.Invalid, "INVALID_QUOTA_INPUT", "幂等键长度无效")
	}
	if len(in.RequestHash) != 64 {
		return fault.New(fault.Invalid, "INVALID_QUOTA_INPUT", "请求摘要无效")
	}
	return nil
}

func (s *Service) caller(ctx context.Context, actor, project string) error {
	if _, machine := access.IdentityFromContext(ctx); machine {
		return fault.New(fault.Forbidden, "MACHINE_FORBIDDEN", "机器主体无权管理采集配额")
	}
	if !uuidPattern.MatchString(actor) {
		return fault.New(fault.Unauthenticated, "SESSION_REQUIRED", "请先登录")
	}
	if !uuidPattern.MatchString(project) {
		return fault.New(fault.Invalid, "INVALID_PROJECT_ID", "项目标识格式不正确")
	}
	return nil
}

// Get returns the override for a scope; configured=false means the static
// default policy remains effective.
func (s *Service) Get(ctx context.Context, actor, project, environment string) (Policy, bool, error) {
	if e := s.caller(ctx, actor, project); e != nil {
		return Policy{}, false, e
	}
	if !validEnvironment(environment) {
		return Policy{}, false, fault.New(fault.Invalid, "INVALID_QUOTA_INPUT", "环境标识无效")
	}
	return s.store.GetQuota(ctx, actor, project, environment)
}

// Put validates and persists an override, applying optimistic revision rules.
func (s *Service) Put(ctx context.Context, in Input, expectedRevision int64) (Policy, bool, error) {
	if e := s.caller(ctx, in.ActorID, in.ProjectID); e != nil {
		return Policy{}, false, e
	}
	if e := validate(in, expectedRevision); e != nil {
		return Policy{}, false, e
	}
	return s.store.PutQuota(ctx, in, expectedRevision)
}

// List returns every persisted override for the serve-process boot loader.
func (s *Service) List(ctx context.Context) ([]Policy, error) {
	return s.store.ListQuotas(ctx)
}
