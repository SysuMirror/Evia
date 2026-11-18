package exporting

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"regexp"
	"time"

	"github.com/howtomakeaname/Evia/backend/internal/platform/fault"
)

var uuidPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

const invalidCode = "INVALID_EXPORT_INPUT"

func invalid(message string) error {
	return fault.New(fault.Invalid, invalidCode, message)
}

// NormalizeFormat maps an omitted format to the json default and rejects
// anything outside the closed vocabulary before it reaches persistence.
func NormalizeFormat(format string) (string, error) {
	switch format {
	case "", FormatJSON:
		return FormatJSON, nil
	case FormatCSV:
		return FormatCSV, nil
	case FormatNDJSON:
		return FormatNDJSON, nil
	default:
		return "", invalid("导出格式仅支持 json、csv 或 ndjson")
	}
}

// ValidateInput repeats the HTTP contract at the domain boundary so a
// bypassed validator cannot persist a malformed export job.
func ValidateInput(in Input) error {
	if _, err := NormalizeFormat(in.Format); err != nil {
		return err
	}
	if !uuidPattern.MatchString(in.ProjectID) || !uuidPattern.MatchString(in.ActorID) {
		return invalid("项目或操作主体标识格式不正确")
	}
	switch in.Environment {
	case "development", "staging", "production":
	default:
		return invalid("环境标识无效")
	}
	if len(in.Name) < 1 || len(in.Name) > MaxNameLength {
		return invalid("导出名称长度必须在 1 到 200 字符之间")
	}
	if len(in.IdempotencyKey) < 1 || len(in.IdempotencyKey) > 128 {
		return invalid("幂等键长度无效")
	}
	if len(in.RequestHash) != 64 {
		return invalid("请求摘要无效")
	}
	if in.Request.Kind == "" || in.Request.Environment == "" {
		return invalid("导出必须包含有界分析请求")
	}
	if in.Request.Environment != in.Environment {
		return invalid("导出环境与查询环境不一致")
	}
	if in.ArtifactExpiry != nil && !in.ArtifactExpiry.After(time.Now()) {
		return invalid("产物过期时间必须晚于当前时间")
	}
	return nil
}

// Service coordinates domain validation and one-time grant minting on top of
// the durable store. It never executes queries; the worker owns execution.
type Service struct {
	store    Store
	now      func() time.Time
	grantTTL time.Duration
}

func NewService(store Store) *Service {
	return &Service{store: store, now: time.Now, grantTTL: GrantTTL}
}

func (s *Service) CreateJob(ctx context.Context, in Input) (Job, bool, error) {
	if err := ValidateInput(in); err != nil {
		return Job{}, false, err
	}
	return s.store.CreateExportJob(ctx, in)
}

func (s *Service) GetJob(ctx context.Context, actor, project, jobID string) (Job, error) {
	return s.store.GetExportJob(ctx, actor, project, jobID)
}

// Export-prefixed aliases satisfy the transport surface while keeping the
// persistence vocabulary consistent across worker and HTTP callers.
func (s *Service) GetExportJob(ctx context.Context, actor, project, jobID string) (Job, error) {
	return s.GetJob(ctx, actor, project, jobID)
}

func (s *Service) ListJobs(ctx context.Context, actor, project string, filter ListFilter) (Page, error) {
	return s.store.ListExportJobs(ctx, actor, project, filter)
}

func (s *Service) ListExportJobs(ctx context.Context, actor, project string, filter ListFilter) (Page, error) {
	return s.ListJobs(ctx, actor, project, filter)
}

func (s *Service) CancelJob(ctx context.Context, actor, project, jobID string) (Job, error) {
	return s.store.CancelExportJob(ctx, actor, project, jobID)
}

func (s *Service) CancelExportJob(ctx context.Context, actor, project, jobID string) (Job, error) {
	return s.CancelJob(ctx, actor, project, jobID)
}

// MintGrant creates a one-time download token for a completed job. The
// plaintext token is returned once; only its sha256 hash is stored.
func (s *Service) MintGrant(ctx context.Context, actor, project, jobID string) (token string, grant Grant, err error) {
	job, err := s.store.GetExportJob(ctx, actor, project, jobID)
	if err != nil {
		return "", Grant{}, err
	}
	if job.State != StateCompleted {
		return "", Grant{}, fault.New(fault.Invalid, "EXPORT_NOT_READY", "只有已完成的导出可以创建下载授权")
	}
	raw := make([]byte, 32)
	if _, err = rand.Read(raw); err != nil {
		return "", Grant{}, err
	}
	token = hex.EncodeToString(raw)
	sum := sha256.Sum256([]byte(token))
	expiresAt := s.now().Add(s.grantTTL)
	grant, err = s.store.CreateExportGrant(ctx, actor, project, jobID, hex.EncodeToString(sum[:]), expiresAt)
	if err != nil {
		return "", Grant{}, err
	}
	return token, grant, nil
}

// Redeem consumes a one-time token and resolves the completed job it grants
// access to. Unknown, expired, reused or non-completed targets are rejected.
func (s *Service) Redeem(ctx context.Context, token string) (Job, Grant, error) {
	sum := sha256.Sum256([]byte(token))
	grant, err := s.store.RedeemExportGrant(ctx, hex.EncodeToString(sum[:]))
	if err != nil {
		return Job{}, Grant{}, err
	}
	if grant == nil {
		return Job{}, Grant{}, fault.New(fault.Forbidden, "GRANT_INVALID", "下载授权不存在、已过期或已使用")
	}
	job, err := s.store.GetSystemExportJob(ctx, grant.ProjectID, grant.JobID)
	if err != nil {
		return Job{}, Grant{}, err
	}
	if job.State != StateCompleted || job.ArtifactPath == nil {
		return Job{}, Grant{}, fault.New(fault.Forbidden, "EXPORT_NOT_READY", "导出产物尚不可用")
	}
	if job.ExpiresAt != nil && job.ExpiresAt.Before(s.now()) {
		return Job{}, Grant{}, fault.New(fault.Forbidden, "EXPORT_EXPIRED", "导出产物已过期")
	}
	return job, *grant, nil
}

// ErrNotCancellable distinguishes lifecycle rejection from storage failures.
var ErrNotCancellable = errors.New("export job is not cancellable")
