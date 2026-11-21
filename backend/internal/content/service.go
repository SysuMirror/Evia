package content

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"sort"
)

type Service struct {
	store     Store
	artifacts ArtifactStore
}

func NewService(store Store, artifacts ArtifactStore) (*Service, error) {
	if store == nil || artifacts == nil {
		return nil, errors.New("content service requires index and artifact store")
	}
	return &Service{store: store, artifacts: artifacts}, nil
}

func (s *Service) GetMessage(ctx context.Context, actor, project, environment, messageID string) (Message, error) {
	records, err := s.store.GetContentMessage(ctx, actor, project, environment, messageID)
	if err != nil {
		return Message{}, err
	}
	sort.Slice(records, func(i, j int) bool { return records[i].ChunkIndex < records[j].ChunkIndex })
	if len(records) == 0 || records[0].ChunkCount != len(records) {
		return Message{}, errors.New("content message is incomplete")
	}
	var content []byte
	for index, record := range records {
		if record.ChunkIndex != index || record.ChunkCount != len(records) {
			return Message{}, errors.New("content message chunk order is invalid")
		}
		data, readErr := s.artifacts.Read(ctx, record.ObjectKey)
		if readErr != nil {
			return Message{}, readErr
		}
		hash := sha256.Sum256(data)
		if hex.EncodeToString(hash[:]) != record.SHA256 {
			return Message{}, errors.New("content artifact hash mismatch")
		}
		content = append(content, data...)
	}
	first := records[0]
	return Message{
		ProjectID: project, Environment: environment,
		ConversationID: first.ConversationID, MessageID: first.MessageID, Role: first.Role,
		Content: string(content), Truncated: first.Truncated, ContentLength: first.ContentLength,
		KeyID: first.KeyID, ReceivedAt: first.ReceivedAt,
		ResponseStatus: first.ResponseStatus, DurationMS: first.DurationMS,
	}, nil
}
