package content

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"sort"
)

type Service struct {
	store     ReadStore
	artifacts ArtifactStore
}

func NewService(store ReadStore, artifacts ArtifactStore) (*Service, error) {
	if store == nil || artifacts == nil {
		return nil, errors.New("content service requires index and artifact store")
	}
	return &Service{store: store, artifacts: artifacts}, nil
}

func (s *Service) ListStreams(ctx context.Context, actor, project, environment string, filter DiscoveryFilter) (StreamPage, error) {
	return s.store.ListContentStreams(ctx, actor, project, environment, filter)
}

func (s *Service) ListItems(ctx context.Context, actor, project, environment, streamID string, filter DiscoveryFilter) (ItemPage, error) {
	return s.store.ListContentItems(ctx, actor, project, environment, streamID, filter)
}

func (s *Service) GetItem(ctx context.Context, actor, project, environment, itemID string) (Item, error) {
	records, err := s.store.GetContentItem(ctx, actor, project, environment, itemID)
	if err != nil {
		return Item{}, err
	}
	sort.Slice(records, func(i, j int) bool { return records[i].ChunkIndex < records[j].ChunkIndex })
	if len(records) == 0 || records[0].ChunkCount != len(records) {
		return Item{}, errors.New("content item is incomplete")
	}
	var content []byte
	for index, record := range records {
		if record.ChunkIndex != index || record.ChunkCount != len(records) {
			return Item{}, errors.New("content item chunk order is invalid")
		}
		data, readErr := s.artifacts.Read(ctx, record.ObjectKey)
		if readErr != nil {
			return Item{}, readErr
		}
		hash := sha256.Sum256(data)
		if hex.EncodeToString(hash[:]) != record.SHA256 {
			return Item{}, errors.New("content artifact hash mismatch")
		}
		content = append(content, data...)
	}
	first := records[0]
	return Item{
		ProjectID: project, Environment: environment,
		ContentType: first.ContentType,
		StreamID:    first.StreamID, ItemID: first.ItemID, ActorType: first.ActorType,
		Content: string(content), Truncated: first.Truncated, ContentLength: first.ContentLength,
		KeyID: first.KeyID, ReceivedAt: first.ReceivedAt,
		Status: first.Status, DurationMS: first.DurationMS,
	}, nil
}
