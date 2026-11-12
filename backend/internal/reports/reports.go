// Package reports owns managed, ordered compilations of published saved
// analyses. A report is a reading-order document rather than a spatial
// dashboard: it pins concrete published analysis versions and never references
// drafts. Persistence enforces referential integrity through foreign keys.
package reports

import (
	"context"
	"regexp"

	"github.com/howtomakeaname/Evia/backend/internal/access"
	"github.com/howtomakeaname/Evia/backend/internal/platform/fault"
)

const (
	MaxSections          = 50
	MaxTitleLength       = 128
	MaxDescriptionLength = 2000
	MaxSectionTitle      = 128
	// CreateRevision is the expect-absent If-Match value for a first create.
	CreateRevision int64 = 0
)

var uuidPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// Section pins one published analysis at a fixed reading position.
type Section struct {
	SectionID       string `json:"section_id"`
	ReadingOrder    int    `json:"reading_order"`
	SectionTitle    string `json:"section_title"`
	AnalysisID      string `json:"analysis_id"`
	AnalysisVersion int64  `json:"analysis_version"`
}

// Definition is the report content independent of its lifecycle columns.
type Definition struct {
	Title       string    `json:"title"`
	Description string    `json:"description"`
	Sections    []Section `json:"sections"`
}

// Report is one persisted compilation with its optimistic revision.
type Report struct {
	ID          string     `json:"report_id"`
	ProjectID   string     `json:"project_id"`
	Environment string     `json:"environment"`
	Revision    int64      `json:"revision"`
	Definition  Definition `json:"definition"`
	CreatedAt   string     `json:"created_at"`
	UpdatedAt   string     `json:"updated_at"`
}

// Input carries an owner-accepted write from the HTTP boundary.
type Input struct {
	ProjectID      string
	ActorID        string
	ReportID       string
	Environment    string
	Definition     Definition
	IdempotencyKey string
	RequestHash    string
}

// Store is the persistence contract implemented by PostgreSQL.
type Store interface {
	Create(ctx context.Context, in Input) (Report, error)
	List(ctx context.Context, actor, project, environment, after string, limit int) ([]Report, error)
	Get(ctx context.Context, actor, project, id string) (Report, error)
	Update(ctx context.Context, in Input, expectedRevision int64) (Report, error)
	Delete(ctx context.Context, actor, project, id string) error
}

// Service holds domain validation independent of transport and storage.
type Service struct {
	store Store
}

func NewService(store Store) *Service { return &Service{store: store} }

func validEnvironment(environment string) bool {
	return environment == "development" || environment == "staging" || environment == "production"
}

// Validate enforces the bounded report shape and unique reading positions.
func Validate(in Input, expectedRevision int64) error {
	if !uuidPattern.MatchString(in.ProjectID) || !uuidPattern.MatchString(in.ActorID) {
		return fault.New(fault.Invalid, "INVALID_REPORT_INPUT", "项目或操作主体标识格式不正确")
	}
	if !validEnvironment(in.Environment) {
		return fault.New(fault.Invalid, "INVALID_REPORT_INPUT", "环境标识无效")
	}
	if title := in.Definition.Title; len(title) < 1 || len(title) > MaxTitleLength || title != trimSpace(title) {
		return fault.New(fault.Invalid, "INVALID_REPORT_INPUT", "标题必须是 1 到 128 个字符且不含首尾空白")
	}
	if len(in.Definition.Description) > MaxDescriptionLength {
		return fault.New(fault.Invalid, "INVALID_REPORT_INPUT", "描述不能超过 2000 个字符")
	}
	if expectedRevision < CreateRevision || expectedRevision > 9007199254740991 {
		return fault.New(fault.Invalid, "REVISION_REQUIRED", "必须提供有效修订")
	}
	if len(in.IdempotencyKey) < 1 || len(in.IdempotencyKey) > 128 {
		return fault.New(fault.Invalid, "INVALID_REPORT_INPUT", "幂等键长度无效")
	}
	if len(in.RequestHash) != 64 {
		return fault.New(fault.Invalid, "INVALID_REPORT_INPUT", "请求摘要无效")
	}
	sections := in.Definition.Sections
	if len(sections) < 1 || len(sections) > MaxSections {
		return fault.New(fault.Invalid, "INVALID_REPORT_INPUT", "报告必须包含 1 到 50 个章节")
	}
	orders := map[int]bool{}
	analysisKeys := map[string]bool{}
	for _, section := range sections {
		if section.ReadingOrder < 1 || section.ReadingOrder > MaxSections {
			return fault.New(fault.Invalid, "INVALID_REPORT_INPUT", "章节顺序必须在 1 到 50 之间")
		}
		if orders[section.ReadingOrder] {
			return fault.New(fault.Invalid, "INVALID_REPORT_INPUT", "章节顺序不能重复")
		}
		orders[section.ReadingOrder] = true
		if len(section.SectionTitle) < 1 || len(section.SectionTitle) > MaxSectionTitle {
			return fault.New(fault.Invalid, "INVALID_REPORT_INPUT", "章节标题必须是 1 到 128 个字符")
		}
		if !uuidPattern.MatchString(section.AnalysisID) {
			return fault.New(fault.Invalid, "INVALID_REPORT_INPUT", "引用分析标识格式不正确")
		}
		if section.AnalysisVersion < 1 {
			return fault.New(fault.Invalid, "INVALID_REPORT_INPUT", "引用分析版本必须为已发布版本")
		}
		key := section.AnalysisID + "#" + intToStr(section.AnalysisVersion)
		if analysisKeys[key] {
			return fault.New(fault.Invalid, "INVALID_REPORT_INPUT", "同一报告不能重复引用同一分析版本")
		}
		analysisKeys[key] = true
	}
	return nil
}

func trimSpace(s string) string {
	start := 0
	for start < len(s) && (s[start] == ' ' || s[start] == '\t') {
		start++
	}
	end := len(s)
	for end > start && (s[end-1] == ' ' || s[end-1] == '\t') {
		end--
	}
	return s[start:end]
}

func intToStr(n int64) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}

func (s *Service) caller(ctx context.Context, actor, project string) error {
	if _, machine := access.IdentityFromContext(ctx); machine {
		return fault.New(fault.Forbidden, "MACHINE_FORBIDDEN", "机器主体无权管理报告")
	}
	if !uuidPattern.MatchString(actor) || !uuidPattern.MatchString(project) {
		return fault.New(fault.Unauthenticated, "SESSION_REQUIRED", "请先登录")
	}
	return nil
}

func (s *Service) Create(ctx context.Context, in Input, expectedRevision int64) (Report, error) {
	if e := s.caller(ctx, in.ActorID, in.ProjectID); e != nil {
		return Report{}, e
	}
	if e := Validate(in, expectedRevision); e != nil {
		return Report{}, e
	}
	return s.store.Create(ctx, in)
}

func (s *Service) List(ctx context.Context, actor, project, environment, after string, limit int) ([]Report, error) {
	if e := s.caller(ctx, actor, project); e != nil {
		return nil, e
	}
	if !validEnvironment(environment) {
		return nil, fault.New(fault.Invalid, "INVALID_REPORT_INPUT", "环境标识无效")
	}
	if limit < 1 || limit > 200 {
		return nil, fault.New(fault.Invalid, "INVALID_REPORT_INPUT", "分页大小超出范围")
	}
	return s.store.List(ctx, actor, project, environment, after, limit)
}

func (s *Service) Get(ctx context.Context, actor, project, id string) (Report, error) {
	if e := s.caller(ctx, actor, project); e != nil {
		return Report{}, e
	}
	if !uuidPattern.MatchString(id) {
		return Report{}, fault.New(fault.Invalid, "INVALID_REPORT_ID", "报告标识格式不正确")
	}
	return s.store.Get(ctx, actor, project, id)
}

func (s *Service) Update(ctx context.Context, in Input, expectedRevision int64) (Report, error) {
	if e := s.caller(ctx, in.ActorID, in.ProjectID); e != nil {
		return Report{}, e
	}
	if e := Validate(in, expectedRevision); e != nil {
		return Report{}, e
	}
	return s.store.Update(ctx, in, expectedRevision)
}

func (s *Service) Delete(ctx context.Context, actor, project, id string) error {
	if e := s.caller(ctx, actor, project); e != nil {
		return e
	}
	if !uuidPattern.MatchString(id) {
		return fault.New(fault.Invalid, "INVALID_REPORT_ID", "报告标识格式不正确")
	}
	return s.store.Delete(ctx, actor, project, id)
}
