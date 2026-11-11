package control

import (
	"net/http"
	"strconv"
	"time"

	"github.com/howtomakeaname/Evia/backend/internal/gen/wire"
	"github.com/howtomakeaname/Evia/backend/internal/platform/fault"
	"github.com/howtomakeaname/Evia/backend/internal/retention"
)

func sweepTime(value time.Time) string {
	return value.UTC().Format(time.RFC3339Nano)
}

func sweepWire(view retention.SweepView) wire.RetentionSweep {
	out := wire.RetentionSweep{
		SweepID:        view.ID,
		ProjectID:      view.ProjectID,
		Environment:    view.Environment,
		RetentionDays:  int64(view.RetentionDays),
		CutoffAt:       sweepTime(view.Cutoff),
		DryRun:         view.DryRun,
		RowsCandidates: int64(view.RowsCandidates),
		Status:         view.Status,
		Detail:         view.Detail,
		CreatedAt:      sweepTime(view.CreatedAt),
		CompletedAt:    nil,
	}
	if view.CompletedAt != nil {
		completed := sweepTime(*view.CompletedAt)
		out.CompletedAt = &completed
	}
	return out
}

// sweepLimit parses an optional bounded limit, defaulting to the domain default.
func sweepLimit(raw string) (int, error) {
	if raw == "" {
		return retention.DefaultSweepLimit, nil
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 1 || n > retention.MaxSweepLimit || strconv.Itoa(n) != raw {
		return 0, fault.New(fault.Invalid, "INVALID_LIMIT", "分页数量必须在 1 到 100 之间")
	}
	return n, nil
}

func (a *RetentionAPI) listSweeps(w http.ResponseWriter, r *http.Request, actor authorizedActor) error {
	project, err := governanceProject(r)
	if err != nil {
		return err
	}
	q, err := governanceQuery(r, "environment", "limit")
	if err != nil {
		return err
	}
	limit, err := sweepLimit(q.Get("limit"))
	if err != nil {
		return err
	}
	filter := retention.SweepFilter{Environment: q.Get("environment")}
	views, err := a.Sweeps.ListSweeps(r.Context(), actor.ID, project, filter, limit)
	if err != nil {
		return err
	}
	items := make([]wire.RetentionSweep, 0, len(views))
	for _, view := range views {
		items = append(items, sweepWire(view))
	}
	return a.API.Boundary.JSON(w, http.StatusOK, "RetentionSweepList", wire.RetentionSweepList{Items: items})
}
