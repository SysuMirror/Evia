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
	GetMessage(context.Context, string, string, string, string) (content.Message, error)
}

type ContentAPI struct {
	API     *API
	Service ContentService
}

func (a *ContentAPI) Register(mux *http.ServeMux) {
	if a.Service == nil {
		return
	}
	mux.HandleFunc("GET /api/v1/projects/{project_id}/content/messages/{message_id}",
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
		return fault.New(fault.Invalid, "INVALID_CONTENT_MESSAGE", "必须指定环境")
	}
	messageID := r.PathValue("message_id")
	message, err := a.Service.GetMessage(r.Context(), actor.ID, project, environment, messageID)
	if err != nil {
		return err
	}
	out := wire.ContentMessage{
		ConversationID: message.ConversationID,
		MessageID:      message.MessageID,
		Role:           message.Role,
		Content:        message.Content,
		Truncated:      message.Truncated,
		ContentLength:  int64(message.ContentLength),
		KeyID:          message.KeyID,
		ReceivedAt:     message.ReceivedAt.UTC().Format(time.RFC3339Nano),
	}
	if message.ResponseStatus != "" {
		out.ResponseStatus = &message.ResponseStatus
	}
	if message.DurationMS > 0 {
		duration := int64(message.DurationMS)
		out.DurationMs = &duration
	}
	return a.API.Boundary.JSON(w, http.StatusOK, "ContentMessage", out)
}
