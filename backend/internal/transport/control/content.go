package control

import (
	"context"
	"net/http"
	"time"

	"github.com/howtomakeaname/Evia/backend/internal/access"
	"github.com/howtomakeaname/Evia/backend/internal/content"
	"github.com/howtomakeaname/Evia/backend/internal/gen/wire"
	"github.com/howtomakeaname/Evia/backend/internal/platform/fault"
)

type ContentService interface {
	GetItem(context.Context, string, string, string, string) (content.Item, error)
}

type ContentAPI struct {
	API     *API
	Service ContentService
}

func (a *ContentAPI) Register(mux *http.ServeMux) {
	if a.Service == nil {
		return
	}
	mux.HandleFunc("GET /api/v1/projects/{project_id}/content/items/{item_id}",
		a.API.projectPermissionAuth(false, access.PermissionContentRead, a.get))
}

func (a *ContentAPI) get(w http.ResponseWriter, r *http.Request, actor authorizedActor) error {
	project, err := governanceProject(r)
	if err != nil {
		return err
	}
	query, err := governanceQuery(r, "environment")
	if err != nil {
		return err
	}
	environment := query.Get("environment")
	if environment == "" {
		return fault.New(fault.Invalid, "INVALID_CONTENT_ITEM", "必须指定环境")
	}
	itemID := r.PathValue("item_id")
	item, err := a.Service.GetItem(r.Context(), actor.ID, project, environment, itemID)
	if err != nil {
		return err
	}
	out := wire.ContentItem{
		ContentType:   item.ContentType,
		StreamID:      item.StreamID,
		ItemID:        item.ItemID,
		ActorType:     item.ActorType,
		Content:       item.Content,
		Truncated:     item.Truncated,
		ContentLength: int64(item.ContentLength),
		KeyID:         item.KeyID,
		ReceivedAt:    item.ReceivedAt.UTC().Format(time.RFC3339Nano),
	}
	if item.Status != "" {
		out.Status = &item.Status
	}
	if item.DurationMS > 0 {
		duration := int64(item.DurationMS)
		out.DurationMs = &duration
	}
	return a.API.Boundary.JSON(w, http.StatusOK, "ContentItem", out)
}
