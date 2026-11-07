// Package catalog owns event definitions, validation and immutable metadata releases.
package catalog

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/howtomakeaname/Evia/backend/internal/platform/fault"
)

const MaxInteger int64 = 9007199254740991

var keyPattern = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)

type EventInput struct{ Key, DisplayName, Description string }
type Event struct {
	TypeID            int64
	Key, DisplayName  string
	PublishedVersions []int64
}
type DraftInput struct {
	DisplayName        *string        `json:"display_name,omitempty"`
	Schema             map[string]any `json:"schema"`
	FieldMetadata      map[string]any `json:"field_metadata,omitzero"`
	Trigger            *string        `json:"trigger,omitempty"`
	AllowedSourceKinds []string       `json:"allowed_source_kinds,omitzero"`
	FactKind           *string        `json:"fact_kind,omitempty"`
	ExampleProperties  map[string]any `json:"example_properties,omitzero"`
}

func text(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

type Draft struct {
	Revision   int64      `json:"revision"`
	Definition DraftInput `json:"definition"`
}
type Issue struct{ Code, Path, Message string }
type Validation struct {
	Valid         bool
	Issues        []Issue
	Compatibility string
}
type ReleaseEvent struct {
	Key           string `json:"event_key"`
	DraftRevision int64  `json:"draft_revision"`
}
type ReleaseInput struct {
	Environment            string         `json:"environment"`
	ExpectedActiveRevision int64          `json:"expected_active_revision"`
	Events                 []ReleaseEvent `json:"events"`
}
type Release struct {
	ID                string `json:"release_id"`
	Revision          int64  `json:"revision"`
	Environment       string `json:"environment"`
	State             string `json:"state"`
	IngestionReady    bool   `json:"ingestion_ready"`
	AccelerationState string `json:"acceleration_state"`
	ManifestHash      string `json:"manifest_hash"`
}

// Store authorizes actor membership inside each persistence transaction. No caller
// authorization result can replace this check. Pagination uses exclusive keys.
type Store interface {
	CreateEvent(context.Context, string, string, EventInput) (Event, error)
	ListEvents(context.Context, string, string, string, string, int) ([]Event, error)
	GetDraft(context.Context, string, string, string) (Draft, error)
	SaveDraft(context.Context, string, string, string, int64, DraftInput) (Draft, error)
	ValidationDraft(context.Context, string, string, string, int64) (Draft, *DraftInput, error)
	CreateRelease(context.Context, string, string, string, ReleaseInput) (Release, error)
	GetRelease(context.Context, string, string, string) (Release, error)
	ListReleases(context.Context, string, string, string, string, int) ([]Release, error)
}
type Service struct{ store Store }

func New(store Store) *Service { return &Service{store: store} }
func Invalid(code, message string, paths ...string) error {
	return &fault.Error{Kind: fault.Invalid, Code: code, Message: message, Fields: paths}
}
func RevisionConflict() error {
	return fault.New(fault.Conflict, "DRAFT_REVISION_CONFLICT", "草稿已更新，请重新加载后重试")
}
func ValidEnvironment(e string) bool {
	return e == "development" || e == "staging" || e == "production"
}
func (s *Service) CreateEvent(ctx context.Context, actor, project string, in EventInput) (Event, error) {
	in.DisplayName = strings.TrimSpace(in.DisplayName)
	if !keyPattern.MatchString(in.Key) {
		return Event{}, Invalid("INVALID_EVENT_KEY", "事件标识格式不正确", "/event_key")
	}
	if in.DisplayName == "" || utf8.RuneCountInString(in.DisplayName) > 128 {
		return Event{}, Invalid("INVALID_DISPLAY_NAME", "显示名称必须为 1 到 128 个字符", "/display_name")
	}
	if utf8.RuneCountInString(in.Description) > 2000 {
		return Event{}, Invalid("INVALID_DESCRIPTION", "描述过长", "/description")
	}
	return s.store.CreateEvent(ctx, actor, project, in)
}
func page(environment string, limit int) error {
	if environment != "" && !ValidEnvironment(environment) {
		return Invalid("INVALID_ENVIRONMENT", "环境无效", "/environment")
	}
	if limit < 1 || limit > 200 {
		return Invalid("INVALID_LIMIT", "分页大小超出范围", "/limit")
	}
	return nil
}
func (s *Service) ListEvents(ctx context.Context, a, p, env, after string, limit int) ([]Event, error) {
	if e := page(env, limit); e != nil {
		return nil, e
	}
	return s.store.ListEvents(ctx, a, p, env, after, limit+1)
}
func (s *Service) GetDraft(ctx context.Context, a, p, k string) (Draft, error) {
	return s.store.GetDraft(ctx, a, p, k)
}
func (s *Service) SaveDraft(ctx context.Context, a, p, k string, revision int64, in DraftInput) (Draft, error) {
	if revision < 1 || revision >= MaxInteger {
		return Draft{}, Invalid("INVALID_REVISION", "草稿版本无效", "/revision")
	}
	if issues := CheckDefinition(in, false); len(issues) > 0 {
		return Draft{}, IssuesError(issues)
	}
	return s.store.SaveDraft(ctx, a, p, k, revision, in)
}
func IssuesError(issues []Issue) error {
	paths := make([]string, 0, len(issues))
	for _, i := range issues {
		paths = append(paths, i.Path)
	}
	return Invalid("INVALID_EVENT_DEFINITION", "事件定义无效", paths...)
}
func (s *Service) ValidateSample(ctx context.Context, a, p, k string, revision int64, sample map[string]any) (Validation, error) {
	if revision < 1 || revision > MaxInteger {
		return Validation{}, Invalid("INVALID_REVISION", "草稿版本无效", "/draft_revision")
	}
	draft, previous, e := s.store.ValidationDraft(ctx, a, p, k, revision)
	if e != nil {
		return Validation{}, e
	}
	issues := CheckSample(draft.Definition.Schema, sample)
	return Validation{Valid: len(issues) == 0, Issues: issues, Compatibility: Compatibility(previous, draft.Definition)}, nil
}
func (s *Service) CreateRelease(ctx context.Context, a, p, key string, in ReleaseInput) (Release, error) {
	if len(key) < 1 || len(key) > 128 || strings.TrimSpace(key) != key {
		return Release{}, Invalid("INVALID_IDEMPOTENCY_KEY", "幂等键必须为 1 到 128 个非空白字符")
	}
	for _, r := range key {
		if r < 33 || r > 126 {
			return Release{}, Invalid("INVALID_IDEMPOTENCY_KEY", "幂等键必须为可见 ASCII 字符")
		}
	}
	if !ValidEnvironment(in.Environment) {
		return Release{}, Invalid("INVALID_ENVIRONMENT", "环境无效", "/environment")
	}
	if in.ExpectedActiveRevision < 0 || in.ExpectedActiveRevision >= MaxInteger {
		return Release{}, Invalid("INVALID_REVISION", "发布版本无效", "/expected_active_revision")
	}
	if len(in.Events) < 1 || len(in.Events) > 200 {
		return Release{}, Invalid("INVALID_RELEASE_EVENTS", "每次发布必须包含 1 到 200 个事件", "/events")
	}
	seen := map[string]bool{}
	for _, v := range in.Events {
		if !keyPattern.MatchString(v.Key) || v.DraftRevision < 1 || v.DraftRevision > MaxInteger || seen[v.Key] {
			return Release{}, Invalid("INVALID_RELEASE_EVENTS", "事件或草稿版本无效，且事件不能重复", "/events")
		}
		seen[v.Key] = true
	}
	return s.store.CreateRelease(ctx, a, p, key, in)
}
func (s *Service) GetRelease(ctx context.Context, a, p, id string) (Release, error) {
	return s.store.GetRelease(ctx, a, p, id)
}
func (s *Service) ListReleases(ctx context.Context, a, p, env, after string, limit int) ([]Release, error) {
	if e := page(env, limit); e != nil {
		return nil, e
	}
	return s.store.ListReleases(ctx, a, p, env, after, limit+1)
}

// CanonicalJSON uses deterministic sorted object keys and rejects unsupported JSON
// numbers. It is used for both request identity and immutable manifest hashes.
func CanonicalJSON(v any) ([]byte, error) {
	data, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	var value any
	if err = decoder.Decode(&value); err != nil {
		return nil, err
	}
	value, err = canonicalValue(value)
	if err != nil {
		return nil, err
	}
	return json.Marshal(value)
}
func canonicalValue(v any) (any, error) {
	switch value := v.(type) {
	case json.Number:
		n, err := value.Float64()
		if err != nil || math.IsNaN(n) || math.IsInf(n, 0) || math.Abs(n) > float64(MaxInteger) {
			return nil, fmt.Errorf("number outside supported JSON range")
		}
		if n == 0 {
			n = 0
		}
		return n, nil
	case map[string]any:
		for k, item := range value {
			normalized, err := canonicalValue(item)
			if err != nil {
				return nil, err
			}
			value[k] = normalized
		}
		return value, nil
	case []any:
		for i, item := range value {
			normalized, err := canonicalValue(item)
			if err != nil {
				return nil, err
			}
			value[i] = normalized
		}
		return value, nil
	default:
		return value, nil
	}
}
func Hash(v any) (string, error) {
	b, e := CanonicalJSON(v)
	if e != nil {
		return "", e
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:]), nil
}
