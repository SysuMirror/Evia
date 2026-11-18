package exporting

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/howtomakeaname/Evia/backend/internal/analysis"
)

func validInput() Input {
	return Input{
		ProjectID:      "11111111-1111-4111-8111-111111111111",
		Environment:    "production",
		ActorID:        "22222222-2222-4222-8222-222222222222",
		Name:           "weekly export",
		IdempotencyKey: "idem-1",
		RequestHash:    strings.Repeat("a", 64),
		Request: analysis.Request{
			Kind:        "events",
			Environment: "production",
			TimeRange:   analysis.TimeRange{From: time.Now().Add(-time.Hour), To: time.Now()},
		},
	}
}

func TestValidateInputAcceptsCompleteInput(t *testing.T) {
	t.Parallel()
	if err := ValidateInput(validInput()); err != nil {
		t.Fatalf("valid input rejected: %v", err)
	}
}

func TestValidateInputRejectsBadShapes(t *testing.T) {
	t.Parallel()
	cases := map[string]func(Input) Input{
		"bad project uuid":     func(in Input) Input { in.ProjectID = "nope"; return in },
		"bad environment":      func(in Input) Input { in.Environment = "stagingx"; return in },
		"empty name":           func(in Input) Input { in.Name = ""; return in },
		"overlong name":        func(in Input) Input { in.Name = string(make([]byte, MaxNameLength+1)); return in },
		"empty idempotency":    func(in Input) Input { in.IdempotencyKey = ""; return in },
		"short hash":           func(in Input) Input { in.RequestHash = "abc"; return in },
		"missing request kind": func(in Input) Input { in.Request.Kind = ""; return in },
		"environment mismatch": func(in Input) Input { in.Request.Environment = "development"; return in },
		"expiry in the past":   func(in Input) Input { past := time.Now().Add(-time.Minute); in.ArtifactExpiry = &past; return in },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if err := ValidateInput(mutate(validInput())); err == nil {
				t.Fatalf("%s must be rejected", name)
			}
		})
	}
}

type fakeStore struct {
	job     Job
	grant   Grant
	redeem  *Grant
	created GrantInput
}

type GrantInput struct {
	Actor, Project, JobID, TokenHash string
	ExpiresAt                        time.Time
}

func (f *fakeStore) CreateExportJob(context.Context, Input) (Job, bool, error) {
	return f.job, true, nil
}
func (f *fakeStore) CreateSavedAnalysisExportJob(context.Context, string, string, string, int64, SavedAnalysisExportInput) (Job, bool, error) {
	return f.job, true, nil
}
func (f *fakeStore) GetExportJob(context.Context, string, string, string) (Job, error) {
	return f.job, nil
}
func (f *fakeStore) ListExportJobs(context.Context, string, string, ListFilter) (Page, error) {
	return Page{}, nil
}
func (f *fakeStore) CancelExportJob(context.Context, string, string, string) (Job, error) {
	return f.job, nil
}
func (f *fakeStore) ClaimExportJob(context.Context, string, time.Duration) (*Claim, error) {
	return nil, nil
}
func (f *fakeStore) BeginRunning(context.Context, Claim) error { return nil }
func (f *fakeStore) CompleteExportJob(context.Context, Claim, Artifact, *time.Time) error {
	return nil
}
func (f *fakeStore) FailExportJob(context.Context, Claim, string) error { return nil }
func (f *fakeStore) CreateExportGrant(_ context.Context, actor, project, jobID, tokenHash string, expiresAt time.Time) (Grant, error) {
	f.created = GrantInput{actor, project, jobID, tokenHash, expiresAt}
	return Grant{TokenHash: tokenHash, JobID: jobID, ProjectID: project, ActorID: actor, ExpiresAt: expiresAt}, nil
}
func (f *fakeStore) RedeemExportGrant(context.Context, string) (*Grant, error) { return f.redeem, nil }
func (f *fakeStore) GetSystemExportJob(context.Context, string, string) (Job, error) {
	return f.job, nil
}

func TestServiceCreateValidatesBeforeStore(t *testing.T) {
	t.Parallel()
	svc := NewService(&fakeStore{})
	in := validInput()
	in.Name = ""
	if _, _, err := svc.CreateJob(context.Background(), in); err == nil {
		t.Fatal("service must reject invalid input before touching the store")
	}
}

func TestMintGrantOnlyForCompletedJobs(t *testing.T) {
	t.Parallel()
	store := &fakeStore{job: Job{JobID: "j1", State: StateQueued}}
	svc := NewService(store)
	if _, _, err := svc.MintGrant(context.Background(), "actor", "project", "j1"); err == nil {
		t.Fatal("grant for a non-completed job must be rejected")
	}

	path := "/var/exports/j1.json"
	store.job = Job{JobID: "j1", ProjectID: "project", State: StateCompleted, ArtifactPath: &path}
	token, grant, err := svc.MintGrant(context.Background(), "actor", "project", "j1")
	if err != nil {
		t.Fatalf("mint grant: %v", err)
	}
	if len(token) != 64 {
		t.Fatalf("plaintext token length = %d, want 64 hex chars", len(token))
	}
	if grant.TokenHash == token || len(grant.TokenHash) != 64 {
		t.Fatal("stored grant must hold the token hash, not the plaintext token")
	}
	if store.created.TokenHash != grant.TokenHash {
		t.Fatal("store did not receive the hashed token")
	}
	if time.Until(store.created.ExpiresAt) > GrantTTL || time.Until(store.created.ExpiresAt) < GrantTTL-time.Minute {
		t.Fatalf("grant expiry outside TTL window: %s", store.created.ExpiresAt)
	}
}

func TestRedeemRejectsUnknownAndIncompleteGrants(t *testing.T) {
	t.Parallel()
	t.Run("unknown token", func(t *testing.T) {
		svc := NewService(&fakeStore{redeem: nil})
		if _, _, err := svc.Redeem(context.Background(), "deadbeef"); err == nil {
			t.Fatal("unknown grant must fail")
		}
	})
	t.Run("incomplete job", func(t *testing.T) {
		store := &fakeStore{
			redeem: &Grant{JobID: "j1", ProjectID: "p1"},
			job:    Job{JobID: "j1", State: StateRunning},
		}
		svc := NewService(store)
		if _, _, err := svc.Redeem(context.Background(), "token"); err == nil {
			t.Fatal("grant for a running job must not redeem")
		}
	})
	t.Run("expired artifact", func(t *testing.T) {
		past := time.Now().Add(-time.Minute)
		path := "/tmp/x.json"
		store := &fakeStore{
			redeem: &Grant{JobID: "j1", ProjectID: "p1"},
			job:    Job{JobID: "j1", State: StateCompleted, ArtifactPath: &path, ExpiresAt: &past},
		}
		svc := NewService(store)
		if _, _, err := svc.Redeem(context.Background(), "token"); err == nil {
			t.Fatal("expired artifact must not redeem")
		}
	})
	t.Run("success returns job", func(t *testing.T) {
		path := "/tmp/x.json"
		store := &fakeStore{
			redeem: &Grant{JobID: "j1", ProjectID: "p1"},
			job:    Job{JobID: "j1", ProjectID: "p1", State: StateCompleted, ArtifactPath: &path},
		}
		svc := NewService(store)
		job, _, err := svc.Redeem(context.Background(), "token")
		if err != nil || job.JobID != "j1" {
			t.Fatalf("redemption failed: %v", err)
		}
	})
}

func TestErrNotCancellableSentinel(t *testing.T) {
	t.Parallel()
	if errors.Is(nil, ErrNotCancellable) {
		t.Fatal("nil must not match the sentinel")
	}
}
