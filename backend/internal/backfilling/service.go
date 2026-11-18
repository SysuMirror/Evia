package backfilling

import (
	"context"
	"regexp"

	"github.com/howtomakeaname/Evia/backend/internal/platform/env"
	"github.com/howtomakeaname/Evia/backend/internal/platform/fault"
)

var uuidPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

func invalid(message string) error {
	return fault.New(fault.Invalid, "INVALID_BACKFILL_INPUT", message)
}

// ValidateInput repeats the HTTP contract at the domain boundary.
func ValidateInput(in Input) error {
	if !uuidPattern.MatchString(in.ProjectID) || !uuidPattern.MatchString(in.ActorID) {
		return invalid("项目或操作主体标识格式不正确")
	}
	if !uuidPattern.MatchString(in.ManifestID) {
		return invalid("归档清单标识格式不正确")
	}
	switch in.Environment {
	case env.Development, env.Staging, env.Production:
	default:
		return invalid("环境标识无效")
	}
	if len(in.Name) < 1 || len(in.Name) > MaxNameLength {
		return invalid("回补名称长度无效")
	}
	if len(in.IdempotencyKey) < 1 || len(in.IdempotencyKey) > 128 {
		return invalid("幂等键长度无效")
	}
	if len(in.RequestHash) != 64 {
		return invalid("请求摘要无效")
	}
	return nil
}

// Service coordinates validation on top of the durable store.
type Service struct {
	store Store
}

func NewService(store Store) *Service { return &Service{store: store} }

func (s *Service) CreateJob(ctx context.Context, in Input) (Job, bool, error) {
	if err := ValidateInput(in); err != nil {
		return Job{}, false, err
	}
	return s.store.CreateBackfillJob(ctx, in)
}

func (s *Service) GetJob(ctx context.Context, actor, project, jobID string) (Job, error) {
	return s.store.GetBackfillJob(ctx, actor, project, jobID)
}

func (s *Service) ListJobs(ctx context.Context, actor, project string, filter ListFilter) (Page, error) {
	return s.store.ListBackfillJobs(ctx, actor, project, filter)
}

func (s *Service) CancelJob(ctx context.Context, actor, project, jobID string) (Job, error) {
	return s.store.CancelBackfillJob(ctx, actor, project, jobID)
}
