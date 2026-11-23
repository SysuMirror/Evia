package control

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/howtomakeaname/Evia/backend/internal/content"
	"github.com/howtomakeaname/Evia/backend/internal/gen/wire"
	"github.com/howtomakeaname/Evia/backend/internal/platform/contract"
	"github.com/howtomakeaname/Evia/backend/internal/platform/cursor"
	"github.com/howtomakeaname/Evia/backend/internal/platform/httpapi"
)

type fakeContentService struct {
	streamPage content.StreamPage
	itemPage   content.ItemPage
	item       content.Item
	filter     content.DiscoveryFilter
	streamID   string
	err        error
}

func (s *fakeContentService) ListStreams(_ context.Context, _, _, _ string, filter content.DiscoveryFilter) (content.StreamPage, error) {
	s.filter = filter
	return s.streamPage, s.err
}
func (s *fakeContentService) ListItems(_ context.Context, _, _, _, streamID string, filter content.DiscoveryFilter) (content.ItemPage, error) {
	s.streamID, s.filter = streamID, filter
	return s.itemPage, s.err
}
func (s *fakeContentService) GetItem(context.Context, string, string, string, string) (content.Item, error) {
	return s.item, s.err
}

func contentAPI(t *testing.T, service ContentService) http.Handler {
	t.Helper()
	validator, err := contract.New()
	if err != nil {
		t.Fatal(err)
	}
	codec, err := cursor.New([]byte(strings.Repeat("c", 32)))
	if err != nil {
		t.Fatal(err)
	}
	a := &API{
		Boundary: &httpapi.Boundary{Validator: validator, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))},
		Access:   &fakeAccess{}, Cursor: codec, Origin: "https://evia.example", Content: service,
	}
	return a.Handler()
}

func contentRequest(handler http.Handler, path string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodGet, path, nil)
	r.AddCookie(&http.Cookie{Name: "evia_session", Value: "valid"})
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	return w
}

func TestContentStreamDiscoveryMapsAndScopesCursor(t *testing.T) {
	now := time.Now().UTC().Add(-time.Minute).Truncate(time.Microsecond)
	service := &fakeContentService{streamPage: content.StreamPage{
		Items: []content.StreamSummary{{
			StreamID: "replay-1", ContentTypes: []string{"session_replay_mutations", "session_replay_snapshot"},
			ActorTypes: []string{"browser"}, FirstReceivedAt: now.Add(-time.Minute), LastReceivedAt: now, ItemCount: 6,
		}},
		Next: &content.DiscoveryPosition{ReceivedAt: now, ID: "replay-1"},
	}}
	handler := contentAPI(t, service)
	base := "/api/v1/projects/" + projectID + "/content/streams?environment=development&content_types=session_replay_snapshot,session_replay_mutations&limit=1"
	frozenAt := now.Add(30 * time.Second)
	w := contentRequest(handler, base+"&as_of="+url.QueryEscape(frozenAt.Format(time.RFC3339Nano)))
	governanceStatus(t, w, http.StatusOK)
	var out wire.ContentStreamList
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil || len(out.Items) != 1 || out.NextCursor == nil || out.FrozenAt != frozenAt.Format(time.RFC3339Nano) {
		t.Fatalf("bad stream list: %s", w.Body.String())
	}
	if out.Items[0].StreamID != "replay-1" || out.Items[0].ItemCount != 6 || len(out.Items[0].ContentTypes) != 2 {
		t.Fatalf("bad stream mapping: %+v", out.Items[0])
	}
	if service.filter.Limit != 1 || !service.filter.AsOf.Equal(frozenAt) || strings.Join(service.filter.ContentTypes, ",") != "session_replay_mutations,session_replay_snapshot" {
		t.Fatalf("bad stream filter: %+v", service.filter)
	}
	nextCursor := *out.NextCursor
	w = contentRequest(handler, base+"&cursor="+nextCursor)
	governanceStatus(t, w, http.StatusOK)
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil || out.FrozenAt != frozenAt.Format(time.RFC3339Nano) {
		t.Fatalf("cursor did not retain freeze: %s", w.Body.String())
	}
	if service.filter.After == nil || service.filter.After.ID != "replay-1" || !service.filter.After.ReceivedAt.Equal(now) {
		t.Fatalf("cursor not restored: %+v", service.filter.After)
	}
	w = contentRequest(handler, base+"&as_of="+url.QueryEscape(frozenAt.Format(time.RFC3339Nano))+"&cursor="+nextCursor)
	governanceStatus(t, w, http.StatusBadRequest)
	w = contentRequest(handler, base+"&cursor="+nextCursor+"x")
	governanceStatus(t, w, http.StatusBadRequest)
	w = contentRequest(handler, strings.Replace(base, "environment=development", "environment=staging", 1)+"&cursor="+*out.NextCursor)
	governanceStatus(t, w, http.StatusBadRequest)
}

func TestContentItemDiscoveryMapsMetadata(t *testing.T) {
	now := time.Date(2026, 11, 22, 2, 1, 0, 0, time.UTC)
	service := &fakeContentService{itemPage: content.ItemPage{Items: []content.ItemSummary{{
		ContentType: "session_replay_snapshot", StreamID: "replay-1", ItemID: "item-1", ActorType: "browser",
		ReceivedAt: now, ChunkCount: 28, ContentLength: 16159, Status: "done", DurationMS: 900, Truncated: false,
	}}}}
	handler := contentAPI(t, service)
	path := "/api/v1/projects/" + projectID + "/content/streams/replay-1/items?environment=development&content_types=session_replay_snapshot,session_replay_mutations"
	w := contentRequest(handler, path)
	governanceStatus(t, w, http.StatusOK)
	var out wire.ContentItemList
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil || len(out.Items) != 1 || out.FrozenAt == "" {
		t.Fatalf("bad item list: %s", w.Body.String())
	}
	item := out.Items[0]
	if service.streamID != "replay-1" || item.ItemID != "item-1" || item.ChunkCount != 28 || item.Status == nil || item.DurationMs == nil {
		t.Fatalf("bad item mapping: %+v", item)
	}
}

func TestContentDiscoveryRejectsInvalidInputAndAuthentication(t *testing.T) {
	handler := contentAPI(t, &fakeContentService{})
	base := "/api/v1/projects/" + projectID + "/content/streams"
	for _, path := range []string{
		base + "?environment=development",
		base + "?environment=development&content_types=bad%20type",
		base + "?environment=development&content_types=one,one",
		base + "?environment=development&content_types=one&unknown=1",
		base + "?environment=development&content_types=one&as_of=not-a-time",
		base + "?environment=development&content_types=one&as_of=" + url.QueryEscape(time.Now().UTC().Add(time.Minute).Format(time.RFC3339Nano)),
	} {
		governanceStatus(t, contentRequest(handler, path), http.StatusBadRequest)
	}
	r := httptest.NewRequest(http.MethodGet, base+"?environment=development&content_types=one", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	governanceStatus(t, w, http.StatusUnauthorized)
}
