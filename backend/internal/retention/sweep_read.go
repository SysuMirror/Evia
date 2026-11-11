package retention

import (
	"context"
	"time"

	"github.com/howtomakeaname/Evia/backend/internal/platform/fault"
)

// DefaultSweepLimit and MaxSweepLimit bound the operational history read so a
// single request can never scan the whole retention_sweeps table.
const (
	DefaultSweepLimit = 20
	MaxSweepLimit     = 100
)

// SweepView is the read projection of a retention_sweeps audit row. It carries
// operational metadata only and never any event payload.
type SweepView struct {
	ID             string
	ProjectID      string
	Environment    string
	RetentionDays  int
	Cutoff         time.Time
	DryRun         bool
	RowsCandidates uint64
	Status         string
	Detail         string
	CreatedAt      time.Time
	CompletedAt    *time.Time
}

// SweepFilter narrows the history. An empty Environment returns every scope.
type SweepFilter struct {
	Environment string
}

// SweepReader is the human-facing persistence port for sweep history. The
// implementation re-authorizes the project owner inside its transaction.
type SweepReader interface {
	ListSweeps(ctx context.Context, actor, project string, filter SweepFilter, limit int) ([]SweepView, error)
}

// NewServiceWithSweepReader wires the same service with an audit-history reader.
// The PostgreSQL store satisfies both Store and SweepReader.
func NewServiceWithSweepReader(store Store, reader SweepReader) *Service {
	return &Service{store: store, reader: reader}
}

// ListSweeps returns the most recent retention evaluations for a project. It is
// owner-only human governance: machines are rejected and the adapter re-checks
// membership before reading any rows.
func (s *Service) ListSweeps(ctx context.Context, actor, project string, filter SweepFilter, limit int) ([]SweepView, error) {
	if e := caller(ctx, actor, project); e != nil {
		return nil, e
	}
	if filter.Environment != "" && !validEnvironment(filter.Environment) {
		return nil, fault.New(fault.Invalid, "INVALID_RETENTION_INPUT", "环境标识无效")
	}
	if limit == 0 {
		limit = DefaultSweepLimit
	}
	if limit < 1 || limit > MaxSweepLimit {
		return nil, fault.New(fault.Invalid, "INVALID_LIMIT", "分页大小必须在 1 到 100 之间")
	}
	if s.reader == nil {
		return nil, fault.New(fault.Unavailable, "RETENTION_SWEEP_UNAVAILABLE", "保留清理历史暂不可用")
	}
	return s.reader.ListSweeps(ctx, actor, project, filter, limit)
}
