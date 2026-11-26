// Package experiments owns project-scoped A/B experiment definitions and lifecycle policy.
package experiments

import (
	"context"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/howtomakeaname/Evia/backend/internal/platform/env"
	"github.com/howtomakeaname/Evia/backend/internal/platform/fault"
	"github.com/howtomakeaname/Evia/backend/internal/platform/ids"
)

const (
	StatusDraft     = "draft"
	StatusRunning   = "running"
	StatusPaused    = "paused"
	StatusCompleted = "completed"

	DefaultExposureEventKey = "experiment_exposed"
	MaxVariants             = 10
	MaxOverrides            = 100
	TotalWeightBps          = 10000
)

var (
	uuidPattern    = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
	keyPattern     = regexp.MustCompile(`^[a-z][a-z0-9_]{1,63}$`)
	variantPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,63}$`)
)

type Variant struct {
	Key       string `json:"key"`
	Name      string `json:"name"`
	WeightBps int    `json:"weight_bps"`
}

type Experiment struct {
	ID               string            `json:"experiment_id"`
	ProjectID        string            `json:"project_id"`
	Environment      string            `json:"environment"`
	ExperimentKey    string            `json:"experiment_key"`
	Name             string            `json:"name"`
	Description      string            `json:"description"`
	Status           string            `json:"status"`
	Salt             string            `json:"salt"`
	ControlVariant   string            `json:"control_variant"`
	ExposureEventKey string            `json:"exposure_event_key"`
	Variants         []Variant         `json:"variants"`
	Overrides        map[string]string `json:"overrides"`
	Version          int64             `json:"version"`
	StartedAt        *time.Time        `json:"started_at"`
	EndedAt          *time.Time        `json:"ended_at"`
	CreatedBy        string            `json:"created_by"`
	CreatedAt        time.Time         `json:"created_at"`
	UpdatedAt        time.Time         `json:"updated_at"`
}

type CreateInput struct {
	Environment      string
	ExperimentKey    string
	Name             string
	Description      string
	Salt             string
	ControlVariant   string
	ExposureEventKey string
	Variants         []Variant
	Overrides        map[string]string
}

type UpdateInput struct {
	Name             string
	Description      string
	Salt             string
	ControlVariant   string
	ExposureEventKey string
	Variants         []Variant
	Overrides        map[string]string
}

type Store interface {
	ListExperiments(ctx context.Context, actor, project, environment string) ([]Experiment, error)
	GetExperiment(ctx context.Context, actor, project, id string) (Experiment, error)
	CreateExperiment(ctx context.Context, actor, project string, item Experiment) (Experiment, error)
	UpdateExperiment(ctx context.Context, actor, project, id string, in UpdateInput) (Experiment, error)
	ChangeExperimentStatus(ctx context.Context, actor, project, id, status string) (Experiment, error)
	DeleteExperiment(ctx context.Context, actor, project, id string) error
}

type Service struct {
	store Store
}

func New(store Store) *Service {
	return &Service{store: store}
}

func Invalid(code, message string, fields ...string) error {
	return &fault.Error{Kind: fault.Invalid, Code: code, Message: message, Fields: fields}
}

func validSalt(salt string) bool {
	if len(salt) < 1 || len(salt) > 64 {
		return false
	}
	for _, c := range salt {
		if c < 33 || c > 126 {
			return false
		}
	}
	return true
}

func normalizeAndValidateConfig(name, desc, salt, control, exposureKey string, variants []Variant, overrides map[string]string) (string, string, string, string, []Variant, map[string]string, error) {
	name = strings.TrimSpace(name)
	if !utf8.ValidString(name) || name == "" || utf8.RuneCountInString(name) > 128 || strings.ContainsRune(name, 0) {
		return "", "", "", "", nil, nil, Invalid("INVALID_EXPERIMENT_NAME", "实验名称必须为 1 到 128 个字符", "/name")
	}
	if !utf8.ValidString(desc) || utf8.RuneCountInString(desc) > 2000 || strings.ContainsRune(desc, 0) {
		return "", "", "", "", nil, nil, Invalid("INVALID_EXPERIMENT_DESCRIPTION", "实验描述超过长度限制", "/description")
	}
	if salt != "" && !validSalt(salt) {
		return "", "", "", "", nil, nil, Invalid("INVALID_EXPERIMENT_SALT", "分桶盐值必须为 1 到 64 个可见 ASCII 字符", "/salt")
	}
	if exposureKey == "" {
		exposureKey = DefaultExposureEventKey
	}
	if !keyPattern.MatchString(exposureKey) {
		return "", "", "", "", nil, nil, Invalid("INVALID_EXPOSURE_EVENT_KEY", "曝光事件键格式无效", "/exposure_event_key")
	}
	if len(variants) < 2 || len(variants) > MaxVariants {
		return "", "", "", "", nil, nil, Invalid("INVALID_EXPERIMENT_VARIANTS", "实验必须包含 2 到 10 个分组", "/variants")
	}
	seen := make(map[string]bool, len(variants))
	cleanVariants := make([]Variant, 0, len(variants))
	sumWeights := 0
	for _, v := range variants {
		if !variantPattern.MatchString(v.Key) || seen[v.Key] {
			return "", "", "", "", nil, nil, Invalid("INVALID_EXPERIMENT_VARIANTS", "分组标识格式无效或重复", "/variants")
		}
		vName := strings.TrimSpace(v.Name)
		if !utf8.ValidString(vName) || vName == "" || utf8.RuneCountInString(vName) > 64 || strings.ContainsRune(vName, 0) {
			return "", "", "", "", nil, nil, Invalid("INVALID_EXPERIMENT_VARIANTS", "分组名称必须为 1 到 64 个字符", "/variants")
		}
		if v.WeightBps < 0 || v.WeightBps > TotalWeightBps {
			return "", "", "", "", nil, nil, Invalid("INVALID_EXPERIMENT_VARIANTS", "分组权重必须在 0 到 10000 bps 之间", "/variants")
		}
		seen[v.Key] = true
		sumWeights += v.WeightBps
		cleanVariants = append(cleanVariants, Variant{Key: v.Key, Name: vName, WeightBps: v.WeightBps})
	}
	if sumWeights < 1 || sumWeights > TotalWeightBps {
		return "", "", "", "", nil, nil, Invalid("INVALID_EXPERIMENT_WEIGHTS", "所有分组权重总和必须在 1 到 10000 bps 之间", "/variants")
	}
	if !seen[control] {
		return "", "", "", "", nil, nil, Invalid("INVALID_EXPERIMENT_CONTROL", "对照组必须属于已定义的分组列表", "/control_variant")
	}
	if len(overrides) > MaxOverrides {
		return "", "", "", "", nil, nil, Invalid("INVALID_EXPERIMENT_OVERRIDES", "白名单覆盖最多支持 100 条记录", "/overrides")
	}
	cleanOverrides := make(map[string]string, len(overrides))
	for subject, target := range overrides {
		s := strings.TrimSpace(subject)
		if s == "" || !utf8.ValidString(s) || utf8.RuneCountInString(s) > 128 || strings.ContainsRune(s, 0) {
			return "", "", "", "", nil, nil, Invalid("INVALID_EXPERIMENT_OVERRIDES", "白名单主体标识无效", "/overrides")
		}
		if !seen[target] {
			return "", "", "", "", nil, nil, Invalid("INVALID_EXPERIMENT_OVERRIDES", "白名单目标分组不存在", "/overrides")
		}
		cleanOverrides[s] = target
	}
	return name, desc, salt, exposureKey, cleanVariants, cleanOverrides, nil
}

// CanTransition enforces the experiment lifecycle state machine.
func CanTransition(from, to string) bool {
	if from == to {
		return false
	}
	switch from {
	case StatusDraft:
		return to == StatusRunning
	case StatusRunning:
		return to == StatusPaused || to == StatusCompleted
	case StatusPaused:
		return to == StatusRunning || to == StatusCompleted
	case StatusCompleted:
		return false
	default:
		return false
	}
}

func (s *Service) List(ctx context.Context, actor, project, environment string) ([]Experiment, error) {
	if !uuidPattern.MatchString(project) {
		return nil, Invalid("INVALID_RESOURCE_ID", "项目标识格式无效")
	}
	if environment != "" && !env.Valid(environment) {
		return nil, Invalid("INVALID_ENVIRONMENT", "环境参数无效", "/environment")
	}
	return s.store.ListExperiments(ctx, actor, project, environment)
}

func (s *Service) Get(ctx context.Context, actor, project, id string) (Experiment, error) {
	if !uuidPattern.MatchString(project) || !uuidPattern.MatchString(id) {
		return Experiment{}, Invalid("INVALID_RESOURCE_ID", "资源标识格式无效")
	}
	return s.store.GetExperiment(ctx, actor, project, id)
}

func (s *Service) Create(ctx context.Context, actor, project string, in CreateInput) (Experiment, error) {
	if !uuidPattern.MatchString(project) {
		return Experiment{}, Invalid("INVALID_RESOURCE_ID", "项目标识格式无效")
	}
	if !env.Valid(in.Environment) {
		return Experiment{}, Invalid("INVALID_ENVIRONMENT", "环境无效", "/environment")
	}
	if !keyPattern.MatchString(in.ExperimentKey) {
		return Experiment{}, Invalid("INVALID_EXPERIMENT_KEY", "实验键必须以小写字母开头，仅含小写字母、数字和下划线（2 到 64 字符）", "/experiment_key")
	}
	salt := in.Salt
	if salt == "" {
		salt = ids.New()
	}
	name, desc, salt, exposureKey, variants, overrides, err := normalizeAndValidateConfig(
		in.Name, in.Description, salt, in.ControlVariant, in.ExposureEventKey, in.Variants, in.Overrides,
	)
	if err != nil {
		return Experiment{}, err
	}
	item := Experiment{
		ID:               ids.New(),
		ProjectID:        project,
		Environment:      in.Environment,
		ExperimentKey:    in.ExperimentKey,
		Name:             name,
		Description:      desc,
		Status:           StatusDraft,
		Salt:             salt,
		ControlVariant:   in.ControlVariant,
		ExposureEventKey: exposureKey,
		Variants:         variants,
		Overrides:        overrides,
		Version:          1,
		CreatedBy:        actor,
	}
	return s.store.CreateExperiment(ctx, actor, project, item)
}

func (s *Service) Update(ctx context.Context, actor, project, id string, in UpdateInput) (Experiment, error) {
	if !uuidPattern.MatchString(project) || !uuidPattern.MatchString(id) {
		return Experiment{}, Invalid("INVALID_RESOURCE_ID", "资源标识格式无效")
	}
	name, desc, salt, exposureKey, variants, overrides, err := normalizeAndValidateConfig(
		in.Name, in.Description, in.Salt, in.ControlVariant, in.ExposureEventKey, in.Variants, in.Overrides,
	)
	if err != nil {
		return Experiment{}, err
	}
	return s.store.UpdateExperiment(ctx, actor, project, id, UpdateInput{
		Name:             name,
		Description:      desc,
		Salt:             salt,
		ControlVariant:   in.ControlVariant,
		ExposureEventKey: exposureKey,
		Variants:         variants,
		Overrides:        overrides,
	})
}

func (s *Service) ChangeStatus(ctx context.Context, actor, project, id, status string) (Experiment, error) {
	if !uuidPattern.MatchString(project) || !uuidPattern.MatchString(id) {
		return Experiment{}, Invalid("INVALID_RESOURCE_ID", "资源标识格式无效")
	}
	switch status {
	case StatusDraft, StatusRunning, StatusPaused, StatusCompleted:
	default:
		return Experiment{}, Invalid("INVALID_EXPERIMENT_STATUS", "实验状态无效", "/status")
	}
	return s.store.ChangeExperimentStatus(ctx, actor, project, id, status)
}

func (s *Service) Delete(ctx context.Context, actor, project, id string) error {
	if !uuidPattern.MatchString(project) || !uuidPattern.MatchString(id) {
		return Invalid("INVALID_RESOURCE_ID", "资源标识格式无效")
	}
	return s.store.DeleteExperiment(ctx, actor, project, id)
}
