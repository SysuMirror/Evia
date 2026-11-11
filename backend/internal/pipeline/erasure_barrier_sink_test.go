package pipeline

import (
	"context"
	"errors"
	"testing"

	"github.com/howtomakeaname/Evia/backend/internal/erasure"
	"github.com/howtomakeaname/Evia/backend/internal/ingestion"
)

type fakeBarrier struct {
	set erasure.ErasedSet
	err error
}

func (b *fakeBarrier) ErasedSubjects(_ context.Context, _, _ string, _, _ []string) (erasure.ErasedSet, error) {
	return b.set, b.err
}

type recordingSink struct {
	written []ingestion.Record
	calls   int
}

func (s *recordingSink) Write(_ context.Context, records []ingestion.Record) error {
	s.calls++
	s.written = append(s.written, records...)
	return nil
}

func barrierRecord(project, environment, uid, anonymous string) ingestion.Record {
	return ingestion.Record{ProjectID: project, Environment: environment, UID: uid, AnonymousID: anonymous}
}

func TestBarrierSinkDropsErasedIdentitiesAndKeepsSurvivors(t *testing.T) {
	barrier := &fakeBarrier{set: erasure.ErasedSet{
		UIDs:         map[string]struct{}{"erased-user": {}},
		AnonymousIDs: map[string]struct{}{"11111111-1111-4111-8111-111111111111": {}},
	}}
	next := &recordingSink{}
	sink := NewBarrierSink(next, barrier)
	records := []ingestion.Record{
		barrierRecord("p1", "development", "erased-user", ""),
		barrierRecord("p1", "development", "survivor", ""),
		barrierRecord("p1", "development", "", "11111111-1111-4111-8111-111111111111"),
		barrierRecord("p1", "development", "", "22222222-2222-4222-8222-222222222222"),
	}
	if err := sink.Write(context.Background(), records); err != nil {
		t.Fatal(err)
	}
	if next.calls != 1 || len(next.written) != 2 {
		t.Fatalf("unexpected forward: %+v", next)
	}
	for _, record := range next.written {
		if record.UID == "erased-user" || record.AnonymousID == "11111111-1111-4111-8111-111111111111" {
			t.Fatal("erased identity was forwarded", record)
		}
	}
}

func TestBarrierSinkAllDroppedSkipsNextSink(t *testing.T) {
	barrier := &fakeBarrier{set: erasure.ErasedSet{UIDs: map[string]struct{}{"erased-user": {}}}}
	next := &recordingSink{}
	sink := NewBarrierSink(next, barrier)
	err := sink.Write(context.Background(), []ingestion.Record{barrierRecord("p1", "development", "erased-user", "")})
	if err != nil || next.calls != 0 {
		t.Fatal(err, next.calls)
	}
}

func TestBarrierSinkLookupErrorFailsClosed(t *testing.T) {
	barrier := &fakeBarrier{err: errors.New("postgres unavailable")}
	next := &recordingSink{}
	sink := NewBarrierSink(next, barrier)
	err := sink.Write(context.Background(), []ingestion.Record{barrierRecord("p1", "development", "user", "")})
	if err == nil || next.calls != 0 {
		t.Fatal("barrier error did not fail closed", err)
	}
}

func TestBarrierSinkGroupsScopesForSeparateLookups(t *testing.T) {
	barrier := &fakeBarrier{set: erasure.ErasedSet{UIDs: map[string]struct{}{"erased-a": {}}}}
	next := &recordingSink{}
	sink := NewBarrierSink(next, barrier)
	records := []ingestion.Record{
		barrierRecord("p1", "development", "erased-a", ""),
		barrierRecord("p2", "production", "kept-b", ""),
	}
	if err := sink.Write(context.Background(), records); err != nil {
		t.Fatal(err)
	}
	if len(next.written) != 1 || next.written[0].ProjectID != "p2" {
		t.Fatalf("cross-scope filtering wrong: %+v", next.written)
	}
}
