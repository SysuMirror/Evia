//go:build integration

package postgres

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/howtomakeaname/Evia/backend/internal/access"
	"github.com/howtomakeaname/Evia/backend/internal/content"
	"github.com/howtomakeaname/Evia/backend/internal/platform/fault"
	"github.com/howtomakeaname/Evia/backend/internal/platform/ids"
)

func TestContentDiscoveryFiltersCompletenessScopesAndPaginates(t *testing.T) {
	store := database(t)
	ctx := context.Background()
	if err := store.Bootstrap(ctx, "content-owner", "Content Owner", "synthetic-password-42"); err != nil {
		t.Fatal(err)
	}
	owner, err := store.FindUser(ctx, "content-owner")
	if err != nil {
		t.Fatal(err)
	}
	project := access.Project{ID: ids.New(), Name: "Content Discovery", Timezone: "UTC"}
	if err = store.CreateProject(ctx, owner.ID, project); err != nil {
		t.Fatal(err)
	}
	base := time.Date(2026, 11, 22, 2, 0, 0, 0, time.UTC)
	insert := func(streamID, itemID, contentType string, receivedAt time.Time, chunkIndex, chunkCount int) {
		t.Helper()
		objectKey := fmt.Sprintf("%064x.chunk", chunkIndex+1)
		hash := fmt.Sprintf("%064x", chunkIndex+101)
		_, err = store.Pool.Exec(ctx, `INSERT INTO content_item_chunks
		 (project_id,environment,event_id,received_at,content_type,stream_id,item_id,actor_type,chunk_index,chunk_count,
		  object_key,sha256,key_id,content_length,truncated,status,duration_ms)
		 VALUES($1,'development',$2,$3,$4,$5,$6,'browser',$7,$8,$9,$10,'key',100,false,'captured',500)`,
			project.ID, ids.New(), receivedAt, contentType, streamID, itemID, chunkIndex, chunkCount, objectKey, hash)
		if err != nil {
			t.Fatal(err)
		}
	}
	for index := 0; index < 2; index++ {
		insert("stream-a", "snapshot-a", "session_replay_snapshot", base.Add(2*time.Minute), index, 2)
	}
	insert("stream-a", "mutations-a", "session_replay_mutations", base.Add(3*time.Minute), 0, 1)
	insert("stream-a", "incomplete-a", "session_replay_snapshot", base.Add(4*time.Minute), 0, 2)
	insert("stream-a", "inconsistent-a", "session_replay_snapshot", base.Add(4*time.Minute), 0, 1)
	insert("stream-a", "inconsistent-a", "session_replay_snapshot", base.Add(4*time.Minute), 1, 2)
	insert("stream-b", "snapshot-b", "session_replay_snapshot", base, 0, 1)
	insert("stream-hidden-type", "chat", "ai_chat_message", base.Add(5*time.Minute), 0, 1)

	filter := content.DiscoveryFilter{ContentTypes: []string{"session_replay_snapshot", "session_replay_mutations"}, AsOf: time.Now().UTC(), Limit: 1}
	first, err := store.ListContentStreams(ctx, owner.ID, project.ID, "development", filter)
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Items) != 1 || first.Items[0].StreamID != "stream-a" || first.Items[0].ItemCount != 2 || first.Next == nil {
		t.Fatalf("unexpected first stream page: %+v", first)
	}
	// A stream completed after the first page must not move into this cursor's
	// frozen listing, even when its event time sorts inside the remaining page.
	insert("stream-late", "snapshot-late", "session_replay_snapshot", base.Add(time.Minute), 0, 1)
	filter.After = first.Next
	second, err := store.ListContentStreams(ctx, owner.ID, project.ID, "development", filter)
	if err != nil {
		t.Fatal(err)
	}
	if len(second.Items) != 1 || second.Items[0].StreamID != "stream-b" || second.Next != nil {
		t.Fatalf("unexpected second stream page: %+v", second)
	}

	items, err := store.ListContentItems(ctx, owner.ID, project.ID, "development", "stream-a", content.DiscoveryFilter{
		ContentTypes: filter.ContentTypes, AsOf: filter.AsOf, Limit: 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(items.Items) != 2 || items.Items[0].ItemID != "snapshot-a" || items.Items[1].ItemID != "mutations-a" {
		t.Fatalf("incomplete or ordered item filtering failed: %+v", items.Items)
	}

	editorID := ids.New()
	if _, err = store.Pool.Exec(ctx, `INSERT INTO accounts(id,username,display_name,password_hash)
		VALUES($1,'content-editor','Editor',$2)`, editorID, owner.PasswordHash); err != nil {
		t.Fatal(err)
	}
	if _, err = store.Pool.Exec(ctx, `INSERT INTO project_members(project_id,user_id,role) VALUES($1,$2,'editor')`, project.ID, editorID); err != nil {
		t.Fatal(err)
	}
	if _, err = store.ListContentStreams(ctx, editorID, project.ID, "development", content.DiscoveryFilter{
		ContentTypes: filter.ContentTypes, AsOf: filter.AsOf, Limit: 10,
	}); err == nil || fault.Public(err).Kind != fault.Forbidden {
		t.Fatalf("editor content discovery must be denied: %v", err)
	}
}
