package control

import (
	"context"
	"encoding/json"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/howtomakeaname/Evia/backend/internal/access"
	"github.com/howtomakeaname/Evia/backend/internal/content"
	"github.com/howtomakeaname/Evia/backend/internal/gen/wire"
	"github.com/howtomakeaname/Evia/backend/internal/platform/fault"
)

type ContentService interface {
	ListStreams(context.Context, string, string, string, content.DiscoveryFilter) (content.StreamPage, error)
	ListItems(context.Context, string, string, string, string, content.DiscoveryFilter) (content.ItemPage, error)
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
	const base = "/api/v1/projects/{project_id}/content"
	mux.HandleFunc("GET "+base+"/streams",
		a.API.projectPermissionAuth(false, access.PermissionContentRead, a.listStreams))
	mux.HandleFunc("GET "+base+"/streams/{stream_id}/items",
		a.API.projectPermissionAuth(false, access.PermissionContentRead, a.listItems))
	mux.HandleFunc("GET "+base+"/items/{item_id}",
		a.API.projectPermissionAuth(false, access.PermissionContentRead, a.get))
}

var contentTypeName = regexp.MustCompile(`^[a-z][a-z0-9_.-]{0,63}$`)

type contentPosition struct {
	AsOf       string `json:"as_of"`
	ReceivedAt string `json:"received_at"`
	ID         string `json:"id"`
}

func contentListInput(r *http.Request, actor, project, streamID string, codec interface {
	Decode(string, string) (string, error)
}) (string, content.DiscoveryFilter, string, error) {
	q, err := governanceQuery(r, "environment", "content_types", "limit", "as_of", "cursor")
	if err != nil {
		return "", content.DiscoveryFilter{}, "", err
	}
	environment := q.Get("environment")
	if environment == "" {
		return "", content.DiscoveryFilter{}, "", fault.New(fault.Invalid, "INVALID_CONTENT_DISCOVERY", "必须指定环境")
	}
	rawTypes := strings.Split(q.Get("content_types"), ",")
	if len(rawTypes) < 1 || len(rawTypes) > 8 {
		return "", content.DiscoveryFilter{}, "", fault.New(fault.Invalid, "INVALID_CONTENT_DISCOVERY", "内容类型数量无效")
	}
	seen := make(map[string]bool, len(rawTypes))
	contentTypes := make([]string, 0, len(rawTypes))
	for _, value := range rawTypes {
		if !contentTypeName.MatchString(value) || seen[value] {
			return "", content.DiscoveryFilter{}, "", fault.New(fault.Invalid, "INVALID_CONTENT_DISCOVERY", "内容类型无效")
		}
		seen[value] = true
		contentTypes = append(contentTypes, value)
	}
	sort.Strings(contentTypes)
	limit, err := governanceLimit(q)
	if err != nil {
		return "", content.DiscoveryFilter{}, "", err
	}
	scope := "content_discovery:" + actor + ":" + project + ":" + environment + ":" + streamID + ":" + strings.Join(contentTypes, ",")
	now := time.Now().UTC()
	filter := content.DiscoveryFilter{ContentTypes: contentTypes, AsOf: now, Limit: limit}
	if raw := q.Get("cursor"); raw != "" {
		if q.Get("as_of") != "" {
			return "", content.DiscoveryFilter{}, "", fault.New(fault.Invalid, "INVALID_CONTENT_DISCOVERY", "冻结时间不能与游标同时提供")
		}
		decoded, decodeErr := codec.Decode(scope, raw)
		if decodeErr != nil {
			return "", content.DiscoveryFilter{}, "", governanceCursorError()
		}
		var position contentPosition
		if governanceDecodePosition(decoded, &position) != nil || len(position.ID) < 1 || len(position.ID) > 128 {
			return "", content.DiscoveryFilter{}, "", governanceCursorError()
		}
		asOf, parseErr := time.Parse(time.RFC3339Nano, position.AsOf)
		if parseErr != nil || asOf.After(time.Now().Add(time.Minute)) {
			return "", content.DiscoveryFilter{}, "", governanceCursorError()
		}
		receivedAt, parseErr := time.Parse(time.RFC3339Nano, position.ReceivedAt)
		if parseErr != nil || receivedAt.After(asOf) {
			return "", content.DiscoveryFilter{}, "", governanceCursorError()
		}
		filter.AsOf = asOf
		filter.After = &content.DiscoveryPosition{ReceivedAt: receivedAt, ID: position.ID}
	} else if raw := q.Get("as_of"); raw != "" {
		asOf, parseErr := time.Parse(time.RFC3339Nano, raw)
		if parseErr != nil || asOf.Before(time.Unix(0, 0)) || asOf.After(now) {
			return "", content.DiscoveryFilter{}, "", fault.New(fault.Invalid, "INVALID_CONTENT_DISCOVERY", "冻结时间无效")
		}
		filter.AsOf = asOf.UTC()
	}
	return environment, filter, scope, nil
}

func encodeContentCursor(a *ContentAPI, scope string, asOf time.Time, position *content.DiscoveryPosition) (*string, error) {
	if position == nil {
		return nil, nil
	}
	raw, err := json.Marshal(contentPosition{AsOf: governanceTime(asOf), ReceivedAt: governanceTime(position.ReceivedAt), ID: position.ID})
	if err != nil {
		return nil, err
	}
	next := a.API.Cursor.Encode(scope, string(raw))
	return &next, nil
}

func (a *ContentAPI) listStreams(w http.ResponseWriter, r *http.Request, actor authorizedActor) error {
	project, err := governanceProject(r)
	if err != nil {
		return err
	}
	environment, filter, scope, err := contentListInput(r, actor.ID, project, "", a.API.Cursor)
	if err != nil {
		return err
	}
	page, err := a.Service.ListStreams(r.Context(), actor.ID, project, environment, filter)
	if err != nil {
		return err
	}
	out := wire.ContentStreamList{FrozenAt: governanceTime(filter.AsOf), Items: make([]wire.ContentStreamSummary, 0, len(page.Items))}
	for _, item := range page.Items {
		out.Items = append(out.Items, wire.ContentStreamSummary{
			StreamID: item.StreamID, ContentTypes: item.ContentTypes, ActorTypes: item.ActorTypes,
			FirstReceivedAt: governanceTime(item.FirstReceivedAt), LastReceivedAt: governanceTime(item.LastReceivedAt),
			ItemCount: int64(item.ItemCount),
		})
	}
	out.NextCursor, err = encodeContentCursor(a, scope, filter.AsOf, page.Next)
	if err != nil {
		return err
	}
	return a.API.Boundary.JSON(w, http.StatusOK, "ContentStreamList", out)
}

func (a *ContentAPI) listItems(w http.ResponseWriter, r *http.Request, actor authorizedActor) error {
	project, err := governanceProject(r)
	if err != nil {
		return err
	}
	streamID := r.PathValue("stream_id")
	if len(streamID) < 1 || len(streamID) > 128 {
		return fault.New(fault.Invalid, "INVALID_CONTENT_STREAM", "内容流标识无效")
	}
	environment, filter, scope, err := contentListInput(r, actor.ID, project, streamID, a.API.Cursor)
	if err != nil {
		return err
	}
	page, err := a.Service.ListItems(r.Context(), actor.ID, project, environment, streamID, filter)
	if err != nil {
		return err
	}
	out := wire.ContentItemList{FrozenAt: governanceTime(filter.AsOf), Items: make([]wire.ContentItemSummary, 0, len(page.Items))}
	for _, item := range page.Items {
		mapped := wire.ContentItemSummary{
			ContentType: item.ContentType, StreamID: item.StreamID, ItemID: item.ItemID, ActorType: item.ActorType,
			ReceivedAt: governanceTime(item.ReceivedAt), ChunkCount: int64(item.ChunkCount),
			ContentLength: int64(item.ContentLength), Truncated: item.Truncated,
		}
		if item.Status != "" {
			mapped.Status = &item.Status
		}
		if item.DurationMS > 0 {
			duration := int64(item.DurationMS)
			mapped.DurationMs = &duration
		}
		out.Items = append(out.Items, mapped)
	}
	out.NextCursor, err = encodeContentCursor(a, scope, filter.AsOf, page.Next)
	if err != nil {
		return err
	}
	return a.API.Boundary.JSON(w, http.StatusOK, "ContentItemList", out)
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
