package content

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"testing"
)

type serviceReadStore struct {
	records    []Record
	streamPage StreamPage
	itemPage   ItemPage
	err        error
}

func (s serviceReadStore) ListContentStreams(context.Context, string, string, string, DiscoveryFilter) (StreamPage, error) {
	return s.streamPage, s.err
}
func (s serviceReadStore) ListContentItems(context.Context, string, string, string, string, DiscoveryFilter) (ItemPage, error) {
	return s.itemPage, s.err
}
func (s serviceReadStore) GetContentItem(context.Context, string, string, string, string) ([]Record, error) {
	return s.records, s.err
}

type serviceArtifacts struct {
	values map[string][]byte
	err    error
}

func (serviceArtifacts) Put(context.Context, DecryptedChunk) (string, string, error) {
	return "", "", nil
}
func (s serviceArtifacts) Read(_ context.Context, key string) ([]byte, error) {
	if s.err != nil {
		return nil, s.err
	}
	return s.values[key], nil
}

func contentHash(value []byte) string {
	hash := sha256.Sum256(value)
	return hex.EncodeToString(hash[:])
}

func TestServiceAssemblesAndAuthenticatesContentArtifacts(t *testing.T) {
	first, second := []byte("hello "), []byte("replay")
	records := []Record{
		{ContentType: "session_replay_snapshot", StreamID: "stream", ItemID: "item", ActorType: "browser", ChunkIndex: 1, ChunkCount: 2, ObjectKey: "two", SHA256: contentHash(second), ContentLength: 12},
		{ContentType: "session_replay_snapshot", StreamID: "stream", ItemID: "item", ActorType: "browser", ChunkIndex: 0, ChunkCount: 2, ObjectKey: "one", SHA256: contentHash(first), ContentLength: 12},
	}
	service, err := NewService(serviceReadStore{records: records}, serviceArtifacts{values: map[string][]byte{"one": first, "two": second}})
	if err != nil {
		t.Fatal(err)
	}
	item, err := service.GetItem(context.Background(), "actor", "project", "development", "item")
	if err != nil {
		t.Fatal(err)
	}
	if item.Content != "hello replay" || item.ContentType != "session_replay_snapshot" || item.StreamID != "stream" {
		t.Fatalf("bad assembled item: %+v", item)
	}
}

func TestServiceRejectsIncompleteOrTamperedContent(t *testing.T) {
	data := []byte("plain")
	for _, tc := range []struct {
		name      string
		records   []Record
		artifacts serviceArtifacts
	}{
		{name: "incomplete", records: []Record{{ChunkIndex: 0, ChunkCount: 2, ObjectKey: "one", SHA256: contentHash(data)}}, artifacts: serviceArtifacts{values: map[string][]byte{"one": data}}},
		{name: "hash mismatch", records: []Record{{ChunkIndex: 0, ChunkCount: 1, ObjectKey: "one", SHA256: contentHash([]byte("other"))}}, artifacts: serviceArtifacts{values: map[string][]byte{"one": data}}},
		{name: "read failure", records: []Record{{ChunkIndex: 0, ChunkCount: 1, ObjectKey: "one", SHA256: contentHash(data)}}, artifacts: serviceArtifacts{err: errors.New("unavailable")}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			service, err := NewService(serviceReadStore{records: tc.records}, tc.artifacts)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = service.GetItem(context.Background(), "actor", "project", "development", "item"); err == nil {
				t.Fatal("invalid content item must fail")
			}
		})
	}
}
