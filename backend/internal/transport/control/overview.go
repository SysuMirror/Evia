package control

import (
	"context"
	"net/http"
	"time"

	"github.com/howtomakeaname/Evia/backend/internal/access"
	"github.com/howtomakeaname/Evia/backend/internal/gen/wire"
	"github.com/howtomakeaname/Evia/backend/internal/overview"
	"github.com/howtomakeaname/Evia/backend/internal/platform/fault"
)

type OverviewService interface {
	Get(context.Context, string, overview.Request) (overview.Result, error)
}

type OverviewAPI struct {
	API     *API
	Service OverviewService
}

func (a *OverviewAPI) Register(mux *http.ServeMux) {
	if a.Service == nil {
		return
	}
	mux.HandleFunc("GET /api/v1/projects/{project_id}/overview", a.API.projectPermissionAuth(false, access.PermissionQueriesRead, a.get))
}

func overviewTime(raw, name string) (time.Time, error) {
	value, err := time.Parse(time.RFC3339Nano, raw)
	if err != nil {
		return time.Time{}, fault.New(fault.Invalid, "INVALID_OVERVIEW_TIME_RANGE", name+" 必须是 RFC3339 时间")
	}
	return value.UTC(), nil
}

func overviewWire(result overview.Result) wire.ProjectOverview {
	out := wire.ProjectOverview{ProjectID: result.ProjectID, Environment: result.Environment, From: result.From.Format(time.RFC3339Nano), To: result.To.Format(time.RFC3339Nano), Interval: result.Interval, TotalEvents: result.TotalEvents, UniqueSessions: result.UniqueSessions, IdentifiedUsers: result.IdentifiedUsers, AnonymousIDs: result.AnonymousIDs, Trend: make([]wire.ProjectOverviewTrendPoint, 0, len(result.Trend)), TopEvents: make([]wire.ProjectOverviewTopEvent, 0, len(result.TopEvents)), Metadata: wire.ProjectOverviewMetadata{Accuracy: result.Metadata.Accuracy, Coverage: result.Metadata.Coverage, KnownGaps: append([]string{}, result.Metadata.KnownGaps...), GeneratedAt: result.Metadata.GeneratedAt.Format(time.RFC3339Nano)}}
	if result.DataWatermark != nil {
		value := result.DataWatermark.UTC().Format(time.RFC3339Nano)
		out.DataWatermark = &value
	}
	for _, point := range result.Trend {
		out.Trend = append(out.Trend, wire.ProjectOverviewTrendPoint{Bucket: point.Bucket.UTC().Format(time.RFC3339Nano), Events: point.Events, Sessions: point.Sessions})
	}
	for _, item := range result.TopEvents {
		out.TopEvents = append(out.TopEvents, wire.ProjectOverviewTopEvent{EventKey: item.EventKey, Count: item.Count})
	}
	return out
}

func (a *OverviewAPI) get(w http.ResponseWriter, r *http.Request, actor authorizedActor) error {
	project, err := governanceProject(r)
	if err != nil {
		return err
	}
	query, err := governanceQuery(r, "environment", "from", "to", "interval")
	if err != nil {
		return err
	}
	for _, required := range []string{"environment", "from", "to"} {
		if query.Get(required) == "" {
			return fault.New(fault.Invalid, "INVALID_QUERY", "必须指定 environment、from 和 to")
		}
	}
	from, err := overviewTime(query.Get("from"), "from")
	if err != nil {
		return err
	}
	to, err := overviewTime(query.Get("to"), "to")
	if err != nil {
		return err
	}
	request := overview.Request{ProjectID: project, Environment: query.Get("environment"), From: from, To: to, Interval: query.Get("interval")}
	result, err := a.Service.Get(r.Context(), actor.ID, request)
	if err != nil {
		return err
	}
	return a.API.Boundary.JSON(w, http.StatusOK, "ProjectOverview", overviewWire(result))
}
