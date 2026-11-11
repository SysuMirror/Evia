package pipeline

import (
	"context"

	"github.com/howtomakeaname/Evia/backend/internal/erasure"
	"github.com/howtomakeaname/Evia/backend/internal/ingestion"
)

// IdentityBarrier resolves durable erasure tombstones for a write batch.
type IdentityBarrier interface {
	ErasedSubjects(ctx context.Context, project, environment string, uids, anonymousIDs []string) (erasure.ErasedSet, error)
}

// BarrierSink decorates the fact sink so records belonging to erased
// identities are dropped before insertion. A barrier lookup error fails
// closed: the whole batch is rejected and retried rather than risking the
// resurrection of erased facts.
type BarrierSink struct {
	next    Sink
	barrier IdentityBarrier
}

// NewBarrierSink wraps sink with the tombstone barrier.
func NewBarrierSink(sink Sink, barrier IdentityBarrier) *BarrierSink {
	return &BarrierSink{next: sink, barrier: barrier}
}

func distinctValues(records []ingestion.Record, pick func(ingestion.Record) string) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, len(records))
	for _, record := range records {
		value := pick(record)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out
}

// Write filters out every record whose uid or anonymous_id carries a
// tombstone, then forwards the remainder. Batches are grouped by
// project/environment because one Kafka batch may span scopes.
func (s *BarrierSink) Write(ctx context.Context, records []ingestion.Record) error {
	if len(records) == 0 {
		return s.next.Write(ctx, records)
	}
	groups := map[string][]ingestion.Record{}
	order := make([]string, 0, 1)
	for _, record := range records {
		key := record.ProjectID + "\x00" + record.Environment
		if _, ok := groups[key]; !ok {
			order = append(order, key)
		}
		groups[key] = append(groups[key], record)
	}
	kept := make([]ingestion.Record, 0, len(records))
	for _, key := range order {
		group := groups[key]
		first := group[0]
		uids := distinctValues(group, func(r ingestion.Record) string { return r.UID })
		anonymousIDs := distinctValues(group, func(r ingestion.Record) string { return r.AnonymousID })
		// A lookup error fails closed: reject the whole batch for retry instead
		// of letting an unknown barrier answer resurrect erased facts.
		set, err := s.barrier.ErasedSubjects(ctx, first.ProjectID, first.Environment, uids, anonymousIDs)
		if err != nil {
			return err
		}
		for _, record := range group {
			if record.UID != "" && set.Contains(erasure.SubjectUID, record.UID) {
				continue
			}
			if record.AnonymousID != "" && set.Contains(erasure.SubjectAnonymousID, record.AnonymousID) {
				continue
			}
			kept = append(kept, record)
		}
	}
	if len(kept) == 0 {
		return nil
	}
	return s.next.Write(ctx, kept)
}
