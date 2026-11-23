package content

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"sort"
	"time"
)

const accessAuditTimeout = 3 * time.Second

type Service struct {
	store     Store
	artifacts ArtifactStore
}

func NewService(store Store, artifacts ArtifactStore) (*Service, error) {
	if store == nil || artifacts == nil {
		return nil, errors.New("content service requires index, audit and artifact stores")
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
	audit := AccessAudit{ActorID: actor, ProjectID: project, Environment: environment, ItemID: itemID}
	records, err := s.store.GetContentItem(ctx, actor, project, environment, itemID)
	if err != nil {
		return s.accessFailure(ctx, audit, err)
	}
	sort.Slice(records, func(i, j int) bool { return records[i].ChunkIndex < records[j].ChunkIndex })
	if len(records) == 0 {
		return s.accessFailure(ctx, audit, errors.New("content item is incomplete"))
	}
	first := records[0]
	if first.ContentType == "" || first.StreamID == "" || first.ItemID != itemID {
		return s.accessFailure(ctx, audit, errors.New("content item metadata is invalid"))
	}
	audit.ContentType, audit.StreamID = first.ContentType, first.StreamID
	if first.ChunkCount != len(records) {
		return s.accessFailure(ctx, audit, errors.New("content item is incomplete"))
	}
	var plaintext []byte
	for index, record := range records {
		if record.ChunkIndex != index || record.ChunkCount != len(records) || record.ContentType != first.ContentType ||
			record.StreamID != first.StreamID || record.ItemID != first.ItemID {
			return s.accessFailure(ctx, audit, errors.New("content item chunk order or metadata is invalid"))
		}
		data, readErr := s.artifacts.Read(ctx, record.ObjectKey)
		if readErr != nil {
			return s.accessFailure(ctx, audit, readErr)
		}
		hash := sha256.Sum256(data)
		if hex.EncodeToString(hash[:]) != record.SHA256 {
			return s.accessFailure(ctx, audit, errors.New("content artifact hash mismatch"))
		}
		plaintext = append(plaintext, data...)
	}
	audit.Outcome = AccessOutcomeSuccess
	if err = s.recordAccess(ctx, audit); err != nil {
		// The plaintext has not crossed the service boundary. An unavailable or
		// revoked audit path therefore fails closed rather than returning content.
		return Item{}, err
	}
	return Item{
		ProjectID: project, Environment: environment,
		ContentType: first.ContentType,
		StreamID:    first.StreamID, ItemID: first.ItemID, ActorType: first.ActorType,
		Content: string(plaintext), Truncated: first.Truncated, ContentLength: first.ContentLength,
		KeyID: first.KeyID, ReceivedAt: first.ReceivedAt,
		Status: first.Status, DurationMS: first.DurationMS,
	}, nil
}

func (s *Service) recordAccess(ctx context.Context, audit AccessAudit) error {
	// Audit attribution values survive a client disconnect, but the detached
	// write remains strictly bounded and reauthorizes in persistence.
	auditCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), accessAuditTimeout)
	defer cancel()
	return s.store.RecordContentAccess(auditCtx, audit)
}

func (s *Service) accessFailure(ctx context.Context, audit AccessAudit, cause error) (Item, error) {
	audit.Outcome = AccessOutcomeFailure
	if err := s.recordAccess(ctx, audit); err != nil {
		return Item{}, err
	}
	return Item{}, cause
}
