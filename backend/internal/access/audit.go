package access

import (
	"context"
	"regexp"
	"strconv"
	"time"

	"github.com/howtomakeaname/Evia/backend/internal/platform/fault"
)

// AuditEntry exposes operational metadata only. Credentials are identifiers,
// never credential material; actor display names are intentionally excluded.
type AuditEntry struct {
	ID, ProjectID, ActorID, ActorKind, Action, ResourceKey string
	CredentialID                                           *string
	OccurredAt                                             time.Time
}

type AuditPosition struct {
	OccurredAt time.Time
	ID         string
}

type AuditFilter struct {
	From, To        time.Time
	Action, ActorID string
}

type AuditStore interface {
	ListAuditLogs(context.Context, string, string, AuditFilter, *AuditPosition, int) ([]AuditEntry, error)
}

type AuditLogs struct{ store AuditStore }

func NewAuditLogs(store AuditStore) *AuditLogs { return &AuditLogs{store: store} }

var auditActionPattern = regexp.MustCompile(`^[a-z][a-z0-9_.-]*$`)

// ValidateAuditQuery also protects direct persistence calls. Input times retain
// nanoseconds; the adapter reconciles them with PostgreSQL's stored precision.
func ValidateAuditQuery(reader, project string, filter AuditFilter, after *AuditPosition) error {
	if !projectIDPattern.MatchString(project) {
		return fault.New(fault.Invalid, "INVALID_PROJECT_ID", "项目标识格式不正确")
	}
	if !projectIDPattern.MatchString(reader) {
		return fault.New(fault.Invalid, "INVALID_ACTOR_ID", "主体标识格式不正确")
	}
	if filter.From.IsZero() || filter.To.IsZero() || filter.From.Year() < 1 || filter.To.Year() > 9999 || !filter.To.After(filter.From) || filter.To.Sub(filter.From) > 31*24*time.Hour {
		return fault.New(fault.Invalid, "INVALID_AUDIT_RANGE", "审计时间范围必须有效且不超过 31 天")
	}
	if filter.Action != "" && (len(filter.Action) > 128 || !auditActionPattern.MatchString(filter.Action)) {
		return fault.New(fault.Invalid, "INVALID_AUDIT_ACTION", "审计操作筛选无效")
	}
	if filter.ActorID != "" && !projectIDPattern.MatchString(filter.ActorID) {
		return fault.New(fault.Invalid, "INVALID_ACTOR_ID", "主体标识格式不正确")
	}
	if after != nil {
		id, err := strconv.ParseInt(after.ID, 10, 64)
		if err != nil || id < 1 || strconv.FormatInt(id, 10) != after.ID || after.OccurredAt.Before(filter.From) || !after.OccurredAt.Before(filter.To) || after.OccurredAt.Nanosecond()%1000 != 0 {
			return fault.New(fault.Invalid, "INVALID_CURSOR", "分页游标无效")
		}
	}
	return nil
}

func (s *AuditLogs) List(ctx context.Context, reader, project string, filter AuditFilter, after *AuditPosition, limit int) ([]AuditEntry, error) {
	if _, machine := IdentityFromContext(ctx); machine {
		return nil, fault.New(fault.Forbidden, "MACHINE_FORBIDDEN", "机器主体无权执行此操作")
	}
	if limit < 1 || limit > 100 {
		return nil, fault.New(fault.Invalid, "INVALID_LIMIT", "分页大小超出范围")
	}
	if e := ValidateAuditQuery(reader, project, filter, after); e != nil {
		return nil, e
	}
	filter.From, filter.To = filter.From.UTC(), filter.To.UTC()
	if after != nil {
		position := *after
		position.OccurredAt = position.OccurredAt.UTC()
		after = &position
	}
	return s.store.ListAuditLogs(ctx, reader, project, filter, after, limit+1)
}
