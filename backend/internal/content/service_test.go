package content

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"testing"
)

type serviceReadStore struct {
	records      []Record
	streamPage   StreamPage
	itemPage     ItemPage
	err          error
	auditErr     error
	audits       *[]AccessAudit
	auditCtxErrs *[]error
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
func (s serviceReadStore) RecordContentAccess(ctx context.Context, audit AccessAudit) error {
	if s.audits != nil {
		*s.audits = append(*s.audits, audit)
	}
	if s.auditCtxErrs != nil {
		*s.auditCtxErrs = append(*s.auditCtxErrs, ctx.Err())
	}
	return s.auditErr
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
	var audits []AccessAudit
	service, err := NewService(serviceReadStore{records: records, audits: &audits}, serviceArtifacts{values: map[string][]byte{"one": first, "two": second}})
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
	if len(audits) != 1 || audits[0].Outcome != AccessOutcomeSuccess || audits[0].ContentType != "session_replay_snapshot" ||
		audits[0].StreamID != "stream" || audits[0].ItemID != "item" {
		t.Fatalf("safe success audit missing: %+v", audits)
	}
}

func TestServiceRecordsFailureAfterRequestCancellation(t *testing.T) {
	var audits []AccessAudit
	var contextErrors []error
	service, err := NewService(serviceReadStore{err: context.Canceled, audits: &audits, auditCtxErrs: &contextErrors}, serviceArtifacts{})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err = service.GetItem(ctx, "actor", "project", "development", "item"); !errors.Is(err, context.Canceled) {
		t.Fatalf("request cancellation lost: %v", err)
	}
	if len(audits) != 1 || audits[0].Outcome != AccessOutcomeFailure || len(contextErrors) != 1 || contextErrors[0] != nil {
		t.Fatalf("canceled request did not use detached bounded audit: audits=%+v context_errors=%v", audits, contextErrors)
	}
}

func TestServiceFailsClosedWhenAccessAuditCannotCommit(t *testing.T) {
	data := []byte("sensitive plaintext")
	auditErr := errors.New("audit unavailable")
	service, err := NewService(serviceReadStore{
		records:  []Record{{ContentType: "session_replay_snapshot", StreamID: "stream / 中文", ItemID: "item", ChunkIndex: 0, ChunkCount: 1, ObjectKey: "one", SHA256: contentHash(data)}},
		auditErr: auditErr,
	}, serviceArtifacts{values: map[string][]byte{"one": data}})
	if err != nil {
		t.Fatal(err)
	}
	item, err := service.GetItem(context.Background(), "actor", "project", "development", "item")
	if !errors.Is(err, auditErr) || item.Content != "" {
		t.Fatalf("content crossed failed audit boundary: item=%+v err=%v", item, err)
	}
	resource := AccessResourceKey("session_replay_snapshot", "stream / 中文", "item")
	if resource != "content.item?stream=c3RyZWFtIC8g5Lit5paH&type=c2Vzc2lvbl9yZXBsYXlfc25hcHNob3Q&item=aXRlbQ" {
		t.Fatalf("unexpected safe resource key: %q", resource)
	}
}

func TestServiceRejectsIncompleteOrTamperedContent(t *testing.T) {
	data := []byte("plain")
	for _, tc := range []struct {
		name      string
		records   []Record
		artifacts serviceArtifacts
	}{
		{name: "incomplete", records: []Record{{ContentType: "session_replay_snapshot", StreamID: "stream", ItemID: "item", ChunkIndex: 0, ChunkCount: 2, ObjectKey: "one", SHA256: contentHash(data)}}, artifacts: serviceArtifacts{values: map[string][]byte{"one": data}}},
		{name: "hash mismatch", records: []Record{{ContentType: "session_replay_snapshot", StreamID: "stream", ItemID: "item", ChunkIndex: 0, ChunkCount: 1, ObjectKey: "one", SHA256: contentHash([]byte("other"))}}, artifacts: serviceArtifacts{values: map[string][]byte{"one": data}}},
		{name: "read failure", records: []Record{{ContentType: "session_replay_snapshot", StreamID: "stream", ItemID: "item", ChunkIndex: 0, ChunkCount: 1, ObjectKey: "one", SHA256: contentHash(data)}}, artifacts: serviceArtifacts{err: errors.New("unavailable")}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var audits []AccessAudit
			service, err := NewService(serviceReadStore{records: tc.records, audits: &audits}, tc.artifacts)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = service.GetItem(context.Background(), "actor", "project", "development", "item"); err == nil {
				t.Fatal("invalid content item must fail")
			}
			if len(audits) != 1 || audits[0].Outcome != AccessOutcomeFailure || audits[0].ItemID != "item" ||
				audits[0].StreamID != "stream" || audits[0].ContentType != "session_replay_snapshot" {
				t.Fatalf("safe scoped failure audit missing: %+v", audits)
			}
		})
	}
}
