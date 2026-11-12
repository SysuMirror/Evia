package archiving

import (
	"context"
	"errors"
	"io"
	"testing"
	"time"
)

func testScope() Scope {
	return Scope{
		ProjectID:     "11111111-1111-4111-8111-111111111111",
		Environment:   "development",
		RetentionDays: 90,
		Cutoff:        time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		SweepID:       "22222222-2222-4222-8222-222222222222",
	}
}

type fakeReader struct {
	count       int64
	countErr    error
	streamErr   error
	streamRows  int64
	payload     string
	drift       bool
	streamCalls int
}

func (f *fakeReader) CountExpired(context.Context, string, string, time.Time) (int64, error) {
	return f.count, f.countErr
}

func (f *fakeReader) StreamExpired(_ context.Context, sink io.Writer, _ Scope) (FactWindow, error) {
	f.streamCalls++
	if f.streamErr != nil {
		return FactWindow{}, f.streamErr
	}
	if _, err := io.WriteString(sink, f.payload); err != nil {
		return FactWindow{}, err
	}
	rows := f.streamRows
	if f.drift {
		rows = f.count + 1
	}
	return FactWindow{
		Rows:        rows,
		WindowStart: time.Date(2025, 12, 1, 0, 0, 0, 0, time.UTC),
		WindowEnd:   time.Date(2025, 12, 31, 0, 0, 0, 0, time.UTC),
	}, nil
}

type fakeStore struct {
	created     []Manifest
	terminal    map[string]Manifest
	failCreate  bool
	failSucceed bool
}

func newFakeStore() *fakeStore {
	return &fakeStore{terminal: map[string]Manifest{}}
}

func (s *fakeStore) CreateManifest(_ context.Context, m Manifest) error {
	if s.failCreate {
		return errors.New("create unavailable")
	}
	s.created = append(s.created, m)
	return nil
}

func (s *fakeStore) MarkSucceeded(_ context.Context, id string, artifact Artifact, window FactWindow, rows int64, at time.Time) error {
	if s.failSucceed {
		return errors.New("succeed unavailable")
	}
	s.terminal[id] = Manifest{Status: StatusMaterialized, ArtifactPath: &artifact.Path, RowCount: rows,
		WindowStart: &window.WindowStart, WindowEnd: &window.WindowEnd, CompletedAt: &at}
	return nil
}

func (s *fakeStore) MarkFailed(_ context.Context, id, code string, at time.Time) error {
	s.terminal[id] = Manifest{Status: StatusFailed, ErrorCode: &code, CompletedAt: &at}
	return nil
}

func (s *fakeStore) ListManifests(context.Context, string, string, ListFilter) (Page, error) {
	return Page{}, nil
}

func (s *fakeStore) GetManifest(context.Context, string, string, string) (Manifest, error) {
	return Manifest{}, nil
}

func TestValidateScopeRejectsBadInputs(t *testing.T) {
	t.Parallel()
	cases := []Scope{
		{ProjectID: "not-uuid", Environment: "development", RetentionDays: 90, Cutoff: time.Now()},
		{ProjectID: testScope().ProjectID, Environment: "bogus", RetentionDays: 90, Cutoff: time.Now()},
		{ProjectID: testScope().ProjectID, Environment: "development", RetentionDays: 7, Cutoff: time.Now()},
		{ProjectID: testScope().ProjectID, Environment: "development", RetentionDays: 90},
	}
	for i, scope := range cases {
		if err := ValidateScope(scope); err == nil {
			t.Fatalf("case %d must be rejected", i)
		}
	}
	if err := ValidateScope(testScope()); err != nil {
		t.Fatalf("valid scope rejected: %v", err)
	}
}

func TestArchiveScopeEmptyScopeCreatesNoManifest(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	svc := NewService(store, &fakeReader{count: 0}, mustStore(t))
	manifest, err := svc.ArchiveScope(context.Background(), testScope())
	if err != nil || manifest.ManifestID != "" {
		t.Fatalf("empty scope must be a no-op: %+v %v", manifest, err)
	}
	if len(store.created) != 0 {
		t.Fatal("empty scope must not persist a manifest")
	}
}

func TestArchiveScopeOverCapFailsBeforeManifest(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	svc := NewService(store, &fakeReader{count: MaxArchiveRows + 1}, mustStore(t))
	_, err := svc.ArchiveScope(context.Background(), testScope())
	if err == nil {
		t.Fatal("over-cap scope must fail closed")
	}
	if len(store.created) != 0 {
		t.Fatal("over-cap scope must not create a manifest")
	}
}

func TestArchiveScopeHappyPathMaterializesManifest(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	reader := &fakeReader{count: 3, streamRows: 3, payload: "{\"a\":1}\n{\"a\":2}\n{\"a\":3}\n"}
	artifacts := mustStore(t)
	svc := NewService(store, reader, artifacts)
	manifest, err := svc.ArchiveScope(context.Background(), testScope())
	if err != nil {
		t.Fatal(err)
	}
	if manifest.Status != StatusMaterialized || manifest.RowCount != 3 || manifest.ArtifactPath == nil {
		t.Fatalf("manifest wrong: %+v", manifest)
	}
	if manifest.SweepID == nil || *manifest.SweepID != testScope().SweepID {
		t.Fatal("sweep linkage lost")
	}
	terminal := store.terminal[manifest.ManifestID]
	if terminal.Status != StatusMaterialized || terminal.WindowStart == nil {
		t.Fatalf("terminal state wrong: %+v", terminal)
	}
}

func TestArchiveScopeStreamFailureIsRecordedDurably(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	reader := &fakeReader{count: 2, streamRows: 2, streamErr: errors.New("ch reset")}
	svc := NewService(store, reader, mustStore(t))
	manifest, err := svc.ArchiveScope(context.Background(), testScope())
	if err == nil {
		t.Fatal("stream failure must surface")
	}
	if manifest.Status != StatusFailed || manifest.ErrorCode == nil || *manifest.ErrorCode != "ARCHIVE_ARTIFACT_WRITE_FAILED" {
		t.Fatalf("failed manifest wrong: %+v", manifest)
	}
	terminal := store.terminal[manifest.ManifestID]
	if terminal.Status != StatusFailed {
		t.Fatalf("failure not durable: %+v", terminal)
	}
}

func TestArchiveScopeRowCountDriftFailsClosed(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	reader := &fakeReader{count: 2, streamRows: 2, payload: "{\"a\":1}\n", drift: true}
	svc := NewService(store, reader, mustStore(t))
	manifest, err := svc.ArchiveScope(context.Background(), testScope())
	if err == nil || manifest.Status != StatusFailed {
		t.Fatalf("drift must fail closed: %+v %v", manifest, err)
	}
	if reader.streamCalls != 1 {
		t.Fatalf("stream invoked %d times", reader.streamCalls)
	}
}

func TestArchiveScopeMarkSucceededFailureRecordsFailed(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	store.failSucceed = true
	reader := &fakeReader{count: 1, streamRows: 1, payload: "{\"a\":1}\n"}
	svc := NewService(store, reader, mustStore(t))
	manifest, err := svc.ArchiveScope(context.Background(), testScope())
	if err == nil || manifest.Status != StatusFailed {
		t.Fatalf("persist failure must be recorded: %+v %v", manifest, err)
	}
}

func mustStore(t *testing.T) *FileArchiveStore {
	t.Helper()
	store, err := NewFileArchiveStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return store
}
