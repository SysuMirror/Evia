// Package overview owns the bounded aggregate shown on a project's overview.
package overview

import (
	"context"
	"time"

	"github.com/howtomakeaname/Evia/backend/internal/platform/env"
	"github.com/howtomakeaname/Evia/backend/internal/platform/fault"
)

const (
	IntervalHour = "hour"
	IntervalDay  = "day"
	MaxRange     = 7 * 24 * time.Hour
)

type Request struct {
	ProjectID   string
	Environment string
	From        time.Time
	To          time.Time
	Interval    string
}

type TrendPoint struct {
	Bucket   time.Time
	Events   int64
	Sessions int64
}

type TopEvent struct {
	EventKey string
	Count    int64
}

type Metadata struct {
	Accuracy    string
	Coverage    string
	KnownGaps   []string
	GeneratedAt time.Time
}

type Result struct {
	ProjectID       string
	Environment     string
	From            time.Time
	To              time.Time
	Interval        string
	TotalEvents     int64
	UniqueSessions  int64
	IdentifiedUsers int64
	AnonymousIDs    int64
	DataWatermark   *time.Time
	Trend           []TrendPoint
	TopEvents       []TopEvent
	Metadata        Metadata
}

// Authority is the PostgreSQL authorization boundary owned by this consumer.
type Authority interface {
	AuthorizeOverview(context.Context, string, string, string) error
}

// Reader is the aggregate-only ClickHouse boundary. It must never return raw rows.
type Reader interface {
	ReadOverview(context.Context, Request) (Result, error)
}

type Service struct {
	authority Authority
	reader    Reader
	now       func() time.Time
}

func NewService(authority Authority, reader Reader) *Service {
	return &Service{authority: authority, reader: reader, now: time.Now}
}

func invalid(code, message string) error { return fault.New(fault.Invalid, code, message) }

func Validate(request Request) error {
	if request.ProjectID == "" {
		return invalid("INVALID_PROJECT_ID", "项目标识格式不正确")
	}
	if !env.Valid(request.Environment) {
		return invalid("INVALID_ENVIRONMENT", "环境无效")
	}
	if request.Interval == "" {
		request.Interval = IntervalHour
	}
	if request.Interval != IntervalHour && request.Interval != IntervalDay {
		return invalid("INVALID_OVERVIEW_INTERVAL", "概览趋势粒度无效")
	}
	if request.From.IsZero() || request.To.IsZero() || !request.From.Before(request.To) {
		return invalid("INVALID_OVERVIEW_TIME_RANGE", "概览时间范围无效")
	}
	if request.To.Sub(request.From) > MaxRange {
		return invalid("OVERVIEW_RANGE_EXCEEDED", "概览时间范围最多为 7 天")
	}
	return nil
}

func (s *Service) Get(ctx context.Context, actor string, request Request) (Result, error) {
	if actor == "" {
		return Result{}, fault.New(fault.Unauthenticated, "SESSION_REQUIRED", "请先登录")
	}
	if request.Interval == "" {
		request.Interval = IntervalHour
	}
	request.From = request.From.UTC()
	request.To = request.To.UTC()
	if err := Validate(request); err != nil {
		return Result{}, err
	}
	// PostgreSQL authorization always completes before any ClickHouse fact read.
	if err := s.authority.AuthorizeOverview(ctx, actor, request.ProjectID, request.Environment); err != nil {
		return Result{}, err
	}
	result, err := s.reader.ReadOverview(ctx, request)
	if err != nil {
		return Result{}, err
	}
	result.ProjectID = request.ProjectID
	result.Environment = request.Environment
	result.From = request.From
	result.To = request.To
	result.Interval = request.Interval
	result.Metadata = Metadata{Accuracy: "exact", Coverage: "provisional", KnownGaps: []string{"pipeline_completeness_not_established"}, GeneratedAt: s.now().UTC()}
	if result.Trend == nil {
		result.Trend = []TrendPoint{}
	}
	if result.TopEvents == nil {
		result.TopEvents = []TopEvent{}
	}
	return result, nil
}
