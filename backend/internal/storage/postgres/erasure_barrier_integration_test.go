//go:build integration

package postgres

import (
	"context"
	"testing"
	"time"

	"github.com/howtomakeaname/Evia/backend/internal/erasure"
	"github.com/howtomakeaname/Evia/backend/internal/ingestion"
	"github.com/howtomakeaname/Evia/backend/internal/pipeline"
	"github.com/howtomakeaname/Evia/backend/internal/platform/ids"
)

type barrierRecordingSink struct {
	written []ingestion.Record
}

func (s *barrierRecordingSink) Write(_ context.Context, records []ingestion.Record) error {
	s.written = append(s.written, records...)
	return nil
}

func (s *barrierRecordingSink) identities() map[string]struct{} {
	out := map[string]struct{}{}
	for _, record := range s.written {
		if record.UID != "" {
			out["uid:"+record.UID] = struct{}{}
		}
		if record.AnonymousID != "" {
			out["anon:"+record.AnonymousID] = struct{}{}
		}
	}
	return out
}

// activateTombstone drives a job far enough to insert its tombstone without
// involving ClickHouse.
func activateTombstone(t *testing.T, f catalogFixture, subjectKind, subject, key string) {
	t.Helper()
	job, created, e := f.s.CreateJob(context.Background(), erasureInput(f, key, subjectKind, subject))
	if e != nil || !created {
		t.Fatal(e, created)
	}
	claim, e := f.s.ClaimJob(context.Background(), "barrier-test", time.Minute)
	if e != nil || claim == nil || claim.JobID != job.JobID {
		t.Fatal(claim, e)
	}
	if e = f.s.BeginHiding(context.Background(), *claim); e != nil {
		t.Fatal(e)
	}
}

func TestBarrierSinkWithRealPostgresTombstones(t *testing.T) {
	f := catalogDB(t)
	erasedUID := "erased-customer"
	survivorUID := "retained-customer"
	erasedAnon := ids.New()
	survivorAnon := ids.New()
	activateTombstone(t, f, erasure.SubjectUID, erasedUID, "tomb-uid")
	activateTombstone(t, f, erasure.SubjectAnonymousID, erasedAnon, "tomb-anon")

	next := &barrierRecordingSink{}
	sink := pipeline.NewBarrierSink(next, f.s)
	records := []ingestion.Record{
		{ProjectID: f.project, Environment: "development", UID: erasedUID},
		{ProjectID: f.project, Environment: "development", UID: survivorUID},
		{ProjectID: f.project, Environment: "development", AnonymousID: erasedAnon},
		{ProjectID: f.project, Environment: "development", AnonymousID: survivorAnon},
	}
	if e := sink.Write(context.Background(), records); e != nil {
		t.Fatal(e)
	}
	got := next.identities()
	if len(got) != 2 {
		t.Fatalf("expected exactly two survivors, got %v", got)
	}
	if _, ok := got["uid:"+survivorUID]; !ok {
		t.Fatal("surviving uid was dropped")
	}
	if _, ok := got["anon:"+survivorAnon]; !ok {
		t.Fatal("surviving anonymous id was dropped")
	}
	if _, ok := got["uid:"+erasedUID]; ok {
		t.Fatal("tombstoned uid reached the sink")
	}
	if _, ok := got["anon:"+erasedAnon]; ok {
		t.Fatal("tombstoned anonymous id reached the sink")
	}
	// Replays of the same batch keep being filtered.
	replay := &barrierRecordingSink{}
	if e := pipeline.NewBarrierSink(replay, f.s).Write(context.Background(), records); e != nil {
		t.Fatal(e)
	}
	if len(replay.written) != 2 {
		t.Fatal("replayed erased facts were resurrected", len(replay.written))
	}
}
