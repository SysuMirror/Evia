package exporting

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/howtomakeaname/Evia/backend/internal/analysis"
	"github.com/howtomakeaname/Evia/backend/internal/platform/fault"
)

type stubStore struct {
	claim             *Claim
	failErr           error
	done              chan struct{}
	completedArtifact Artifact
	completedClaim    Claim
	failedCode        string
	failedClaim       Claim
}

func (s *stubStore) CreateExportJob(context.Context, Input) (Job, bool, error) {
	return Job{}, false, nil
}
func (s *stubStore) GetExportJob(context.Context, string, string, string) (Job, error) {
	return Job{}, nil
}
func (s *stubStore) ListExportJobs(context.Context, string, string, ListFilter) (Page, error) {
	return Page{}, nil
}
func (s *stubStore) CancelExportJob(context.Context, string, string, string) (Job, error) {
	return Job{}, nil
}
func (s *stubStore) ClaimExportJob(context.Context, string, time.Duration) (*Claim, error) {
	return s.claim, nil
}
func (s *stubStore) CompleteExportJob(_ context.Context, claim Claim, artifact Artifact, _ *time.Time) error {
	s.completedArtifact = artifact
	s.completedClaim = claim
	close(s.done)
	return nil
}
func (s *stubStore) FailExportJob(_ context.Context, claim Claim, code string) error {
	s.failedCode = code
	s.failedClaim = claim
	close(s.done)
	return s.failErr
}
func (s *stubStore) CreateExportGrant(context.Context, string, string, string, string, time.Time) (Grant, error) {
	return Grant{}, nil
}
func (s *stubStore) RedeemExportGrant(context.Context, string) (*Grant, error) { return nil, nil }
func (s *stubStore) GetSystemExportJob(context.Context, string, string) (Job, error) {
	return Job{}, nil
}

type stubEngine struct {
	result analysis.Result
	err    error
}

func (e stubEngine) Execute(context.Context, analysis.Scope, analysis.Request) (analysis.Result, error) {
	return e.result, e.err
}

func discardLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func TestWorkerCompletesJobWithArtifact(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	artifacts, err := NewFileArtifactStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	claim := &Claim{Job: Job{JobID: "11111111-1111-4111-8111-111111111111", ProjectID: "p", Environment: "development", State: StateQueued}}
	store := &stubStore{claim: claim, done: make(chan struct{})}
	engine := stubEngine{result: analysis.Result{
		Columns: []analysis.Column{{Key: "n", Type: "integer"}},
		Rows:    []map[string]any{{"n": 1}, {"n": 2}},
	}}
	worker := &Worker{Store: store, Engine: engine, Artifacts: artifacts, ID: "export-worker-test", Lease: time.Minute, Logger: discardLogger()}
	if worked, e := worker.RunOnce(context.Background()); e != nil || !worked {
		t.Fatalf("RunOnce worked=%v err=%v", worked, e)
	}
	select {
	case <-store.done:
	case <-time.After(time.Second):
		t.Fatal("completion not recorded")
	}
	if store.completedArtifact.Rows != 2 || store.completedArtifact.SHA256 == "" || store.completedClaim.JobID != claim.JobID {
		t.Fatalf("bad completion: %+v", store.completedArtifact)
	}
	handle, e := artifacts.Open(context.Background(), store.completedArtifact.Path)
	if e != nil {
		t.Fatal(e)
	}
	defer handle.Close()
}

func TestWorkerFailsJobOnEngineError(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	artifacts, err := NewFileArtifactStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	claim := &Claim{Job: Job{JobID: "22222222-2222-4222-8222-222222222222", State: StateQueued}}
	store := &stubStore{claim: claim, done: make(chan struct{})}
	engine := stubEngine{err: fault.New(fault.Unavailable, "QUERY_STORAGE_UNAVAILABLE", "down")}
	worker := &Worker{Store: store, Engine: engine, Artifacts: artifacts, ID: "export-worker-test", Logger: discardLogger()}
	if _, e := worker.RunOnce(context.Background()); e != nil {
		t.Fatalf("engine failure must be recorded, not raised: %v", e)
	}
	<-store.done
	if store.failedCode != "QUERY_STORAGE_UNAVAILABLE" {
		t.Fatalf("failed code = %q", store.failedCode)
	}
}

func TestWorkerEmptyQueueDoesNotWork(t *testing.T) {
	t.Parallel()
	artifacts, _ := NewFileArtifactStore(t.TempDir())
	worker := &Worker{Store: &stubStore{claim: nil, done: make(chan struct{})}, Engine: stubEngine{},
		Artifacts: artifacts, ID: "export-worker-test", Logger: discardLogger()}
	worked, err := worker.RunOnce(context.Background())
	if err != nil || worked {
		t.Fatalf("empty queue worked=%v err=%v", worked, err)
	}
}

func TestWorkerRequiresDependencies(t *testing.T) {
	t.Parallel()
	if err := (&Worker{Store: &stubStore{}}).Run(context.Background()); err == nil {
		t.Fatal("worker without dependencies must fail fast")
	}
}

func TestFileArtifactStoreRejectsOversizeResult(t *testing.T) {
	t.Parallel()
	store, err := NewFileArtifactStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	rows := make([]map[string]any, MaxResultRows+1)
	for i := range rows {
		rows[i] = map[string]any{"i": i}
	}
	_, err = store.Write(context.Background(), "33333333-3333-4333-8333-333333333333", FormatJSON, analysis.Result{Rows: rows})
	var known *fault.Error
	if err == nil || !errors.As(err, &known) || known.Code != "EXPORT_RESULT_TOO_LARGE" {
		t.Fatalf("oversize result must fail with EXPORT_RESULT_TOO_LARGE, got %v", err)
	}
}

func TestFileArtifactStorePathContainment(t *testing.T) {
	t.Parallel()
	store, err := NewFileArtifactStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.Open(context.Background(), "/etc/passwd"); err == nil {
		t.Fatal("path outside the export root must not open")
	}
	if _, err = NewFileArtifactStore("relative/path"); err == nil {
		t.Fatal("relative artifact root must be rejected")
	}
}

// nativeEvaluatorEngineResult mirrors the in-process evaluators that emit
// native int/float64 and concrete slice types without a JSON round-trip.
func nativeEvaluatorEngineResult() analysis.Result {
	return analysis.Result{
		Columns: []analysis.Column{
			{Key: "subjects", Type: "integer"},
			{Key: "share", Type: "number"},
			{Key: "path", Type: "array"},
			{Key: "retained", Type: "array"},
		},
		Rows: []map[string]any{
			{
				"subjects": 2,   // native int
				"share":    0.5, // native float64
				"path":     []string{"open", "view"},
				"retained": []int{1, 0, 1},
			},
		},
	}
}

// TestWorkerExportsNativeEvaluatorResultAsCSV drives the exact production
// chain (RunOnce -> Artifacts.Write -> renderCSV) with a CSV job whose engine
// returns native evaluator types. Before the csvCell native-type fix this job
// failed with EXPORT_CSV_UNSUPPORTED instead of producing an artifact.
func TestWorkerExportsNativeEvaluatorResultAsCSV(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	artifacts, err := NewFileArtifactStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	claim := &Claim{Job: Job{
		JobID:       "66666666-6666-4666-8666-666666666666",
		ProjectID:   "p",
		Environment: "development",
		State:       StateQueued,
		Format:      FormatCSV,
	}}
	store := &stubStore{claim: claim, done: make(chan struct{})}
	worker := &Worker{
		Store: store, Engine: stubEngine{result: nativeEvaluatorEngineResult()},
		Artifacts: artifacts, ID: "export-worker-test", Lease: time.Minute, Logger: discardLogger(),
	}
	if worked, e := worker.RunOnce(context.Background()); e != nil || !worked {
		t.Fatalf("RunOnce worked=%v err=%v", worked, e)
	}
	<-store.done
	if store.failedCode != "" {
		t.Fatalf("native evaluator CSV export must not fail, got code %q", store.failedCode)
	}
	if filepath.Ext(store.completedArtifact.Path) != ".csv" || store.completedArtifact.Rows != 1 {
		t.Fatalf("bad csv artifact: %+v", store.completedArtifact)
	}
	handle, e := artifacts.Open(context.Background(), store.completedArtifact.Path)
	if e != nil {
		t.Fatal(e)
	}
	defer handle.Close()
	payload, e := io.ReadAll(handle)
	if e != nil {
		t.Fatal(e)
	}
	text := string(payload)
	if !strings.HasPrefix(text, "subjects,share,path,retained\n") {
		t.Fatalf("csv header missing: %q", text)
	}
	if !strings.Contains(text, `2,0.5,"[""open"",""view""]","[1,0,1]"`) {
		t.Fatalf("native evaluator row not rendered through worker: %q", text)
	}
}

// TestWorkerExportsNativeEvaluatorResultAsJSON confirms the JSON path also
// materializes native evaluator output through the worker (json.Marshal handles
// the native types, so this guards against regressions in the shared chain).
func TestWorkerExportsNativeEvaluatorResultAsJSON(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	artifacts, err := NewFileArtifactStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	claim := &Claim{Job: Job{
		JobID:       "77777777-7777-4777-8777-777777777777",
		ProjectID:   "p",
		Environment: "development",
		State:       StateQueued,
		Format:      FormatJSON,
	}}
	store := &stubStore{claim: claim, done: make(chan struct{})}
	worker := &Worker{
		Store: store, Engine: stubEngine{result: nativeEvaluatorEngineResult()},
		Artifacts: artifacts, ID: "export-worker-test", Lease: time.Minute, Logger: discardLogger(),
	}
	if _, e := worker.RunOnce(context.Background()); e != nil {
		t.Fatalf("RunOnce error: %v", e)
	}
	<-store.done
	if store.failedCode != "" {
		t.Fatalf("native evaluator JSON export must not fail, got code %q", store.failedCode)
	}
	if filepath.Ext(store.completedArtifact.Path) != ".json" || store.completedArtifact.Rows != 1 {
		t.Fatalf("bad json artifact: %+v", store.completedArtifact)
	}
}
