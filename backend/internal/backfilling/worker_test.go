package backfilling

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/howtomakeaname/Evia/backend/internal/archiving"
)

func sampleInput() Input {
	return Input{
		ProjectID:      "11111111-1111-4111-8111-111111111111",
		Environment:    "development",
		ActorID:        "22222222-2222-4222-8222-222222222222",
		ManifestID:     "33333333-3333-4333-8333-333333333333",
		Name:           "restore november",
		IdempotencyKey: "bf-key-1",
		RequestHash:    strings.Repeat("a", 64),
	}
}

func TestValidateInputGuards(t *testing.T) {
	t.Parallel()
	good := sampleInput()
	if err := ValidateInput(good); err != nil {
		t.Fatalf("valid input rejected: %v", err)
	}
	bad := []Input{
		func() Input { in := sampleInput(); in.ProjectID = "nope"; return in }(),
		func() Input { in := sampleInput(); in.ManifestID = "nope"; return in }(),
		func() Input { in := sampleInput(); in.Environment = "bogus"; return in }(),
		func() Input { in := sampleInput(); in.Name = ""; return in }(),
		func() Input { in := sampleInput(); in.IdempotencyKey = ""; return in }(),
		func() Input { in := sampleInput(); in.RequestHash = "short"; return in }(),
	}
	for i, in := range bad {
		if ValidateInput(in) == nil {
			t.Fatalf("bad case %d accepted", i)
		}
	}
}

type fakeStore struct {
	claim       *Claim
	completed   int64
	failed      string
	completeN   int
	failN       int
	completeErr error
}

func (s *fakeStore) CreateBackfillJob(context.Context, Input) (Job, bool, error) {
	return Job{}, false, nil
}
func (s *fakeStore) GetBackfillJob(context.Context, string, string, string) (Job, error) {
	return Job{}, nil
}
func (s *fakeStore) ListBackfillJobs(context.Context, string, string, ListFilter) (Page, error) {
	return Page{}, nil
}
func (s *fakeStore) CancelBackfillJob(context.Context, string, string, string) (Job, error) {
	return Job{}, nil
}
func (s *fakeStore) ClaimBackfillJob(context.Context, string, time.Duration) (*Claim, error) {
	return s.claim, nil
}
func (s *fakeStore) CompleteBackfillJob(_ context.Context, _ Claim, rows int64) error {
	s.completeN++
	s.completed = rows
	return s.completeErr
}
func (s *fakeStore) FailBackfillJob(_ context.Context, _ Claim, code string) error {
	s.failN++
	s.failed = code
	return nil
}

type fakeManifests struct {
	manifest archiving.Manifest
	err      error
}

func (f *fakeManifests) GetSystemManifest(context.Context, string, string) (archiving.Manifest, error) {
	return f.manifest, f.err
}

type fakeArchives struct {
	err error
}

func (f *fakeArchives) OpenArtifact(context.Context, string) (io.ReadCloser, error) {
	if f.err != nil {
		return nil, f.err
	}
	return io.NopCloser(strings.NewReader("{\"event_id\":\"x\"}\n")), nil
}

type fakeSink struct {
	rows int64
	err  error
}

func (f *fakeSink) InsertFacts(context.Context, string, string, io.Reader) (int64, error) {
	return f.rows, f.err
}

func materializedClaim() *Claim {
	return &Claim{
		Job: Job{
			JobID:        "44444444-4444-4444-8444-444444444444",
			ProjectID:    "11111111-1111-4111-8111-111111111111",
			Environment:  "development",
			ManifestID:   "33333333-3333-4333-8333-333333333333",
			State:        StateRunning,
			RowsExpected: 2,
		},
		Worker:     "backfill-worker-1",
		Generation: 1,
	}
}

func materializedManifest() archiving.Manifest {
	path := "/var/lib/evia/archives/33333333-3333-4333-8333-333333333333.jsonl"
	return archiving.Manifest{
		ManifestID:   "33333333-3333-4333-8333-333333333333",
		ProjectID:    "11111111-1111-4111-8111-111111111111",
		Environment:  "development",
		RowCount:     2,
		Status:       archiving.StatusMaterialized,
		ArtifactPath: &path,
	}
}

func newWorker(store Store, manifests ManifestReader, archives ArchiveSource, sink FactSink) *Worker {
	return &Worker{ID: "backfill-worker-1", Store: store, Manifests: manifests, Archives: archives, Sink: sink}
}

func TestRunOnceNoClaimIsNoop(t *testing.T) {
	t.Parallel()
	store := &fakeStore{}
	worked, err := newWorker(store, &fakeManifests{}, &fakeArchives{}, &fakeSink{}).RunOnce(context.Background())
	if err != nil || worked {
		t.Fatalf("empty queue must be no-op: worked=%v err=%v", worked, err)
	}
}

func TestRunOnceHappyPathCompletesWithRows(t *testing.T) {
	t.Parallel()
	store := &fakeStore{claim: materializedClaim()}
	worker := newWorker(store, &fakeManifests{manifest: materializedManifest()}, &fakeArchives{}, &fakeSink{rows: 2})
	worked, err := worker.RunOnce(context.Background())
	if err != nil || !worked || store.completeN != 1 || store.completed != 2 || store.failN != 0 {
		t.Fatalf("happy path wrong: worked=%v err=%v store=%+v", worked, err, store)
	}
}

func TestRunOnceFailureBranchesRecordCodes(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name      string
		manifests ManifestReader
		archives  ArchiveSource
		sink      FactSink
		wantCode  string
	}{
		{"manifest unavailable", &fakeManifests{err: errors.New("pg down")}, &fakeArchives{}, &fakeSink{}, "BACKFILL_MANIFEST_UNAVAILABLE"},
		{"manifest not materialized", &fakeManifests{manifest: func() archiving.Manifest {
			m := materializedManifest()
			m.Status = archiving.StatusFailed
			m.ArtifactPath = nil
			return m
		}()}, &fakeArchives{}, &fakeSink{}, "BACKFILL_MANIFEST_NOT_MATERIALIZED"},
		{"scope mismatch", &fakeManifests{manifest: func() archiving.Manifest {
			m := materializedManifest()
			m.Environment = "production"
			return m
		}()}, &fakeArchives{}, &fakeSink{}, "BACKFILL_SCOPE_MISMATCH"},
		{"artifact unreadable", &fakeManifests{manifest: materializedManifest()}, &fakeArchives{err: errors.New("denied")}, &fakeSink{}, "BACKFILL_ARTIFACT_UNREADABLE"},
		{"insert failed", &fakeManifests{manifest: materializedManifest()}, &fakeArchives{}, &fakeSink{err: errors.New("ch error")}, "BACKFILL_INSERT_FAILED"},
		{"row drift", &fakeManifests{manifest: materializedManifest()}, &fakeArchives{}, &fakeSink{rows: 1}, "BACKFILL_ROW_COUNT_DRIFT"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := &fakeStore{claim: materializedClaim()}
			_, err := newWorker(store, tc.manifests, tc.archives, tc.sink).RunOnce(context.Background())
			if err != nil || store.failN != 1 || store.failed != tc.wantCode || store.completeN != 0 {
				t.Fatalf("%s: err=%v failed=%s want=%s", tc.name, err, store.failed, tc.wantCode)
			}
		})
	}
}
