package experiments

import (
	"context"
	"testing"
)

type stubStore struct {
	created Experiment
	updated UpdateInput
	status  string
	deleted string
}

func (s *stubStore) ListExperiments(context.Context, string, string, string) ([]Experiment, error) {
	return nil, nil
}
func (s *stubStore) GetExperiment(context.Context, string, string, string) (Experiment, error) {
	return Experiment{}, nil
}
func (s *stubStore) CreateExperiment(_ context.Context, _, _ string, item Experiment) (Experiment, error) {
	s.created = item
	return item, nil
}
func (s *stubStore) UpdateExperiment(_ context.Context, _, _, _ string, in UpdateInput) (Experiment, error) {
	s.updated = in
	return Experiment{}, nil
}
func (s *stubStore) ChangeExperimentStatus(_ context.Context, _, _, _, status string) (Experiment, error) {
	s.status = status
	return Experiment{Status: status}, nil
}
func (s *stubStore) DeleteExperiment(_ context.Context, _, _, id string) error {
	s.deleted = id
	return nil
}

func TestCreateValidationAndDefaults(t *testing.T) {
	store := &stubStore{}
	svc := New(store)
	pid := "00000000-0000-4000-8000-000000000001"
	actor := "00000000-0000-4000-8000-000000000002"

	exp, err := svc.Create(context.Background(), actor, pid, CreateInput{
		Environment:    "development",
		ExperimentKey:  "home_feed_rank",
		Name:           "Home Feed Ranking",
		ControlVariant: "time",
		Variants: []Variant{
			{Key: "time", Name: "Chronological", WeightBps: 5000},
			{Key: "recommend", Name: "Personalized", WeightBps: 5000},
		},
		Overrides: map[string]string{"u-1": "recommend"},
	})
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}
	if exp.Status != StatusDraft || exp.ExposureEventKey != DefaultExposureEventKey || exp.Salt == "" {
		t.Fatalf("unexpected defaults: %+v", exp)
	}

	// Reject weights exceeding 10000 bps
	if _, err := svc.Create(context.Background(), actor, pid, CreateInput{
		Environment:    "development",
		ExperimentKey:  "bad_weights",
		Name:           "Bad",
		ControlVariant: "a",
		Variants: []Variant{
			{Key: "a", Name: "A", WeightBps: 6000},
			{Key: "b", Name: "B", WeightBps: 5000},
		},
	}); err == nil {
		t.Fatal("expected error for weights > 10000")
	}

	// Reject control variant not in variants
	if _, err := svc.Create(context.Background(), actor, pid, CreateInput{
		Environment:    "development",
		ExperimentKey:  "bad_control",
		Name:           "Bad",
		ControlVariant: "missing",
		Variants: []Variant{
			{Key: "a", Name: "A", WeightBps: 5000},
			{Key: "b", Name: "B", WeightBps: 5000},
		},
	}); err == nil {
		t.Fatal("expected error for missing control variant")
	}

	// Reject override targeting unknown variant
	if _, err := svc.Create(context.Background(), actor, pid, CreateInput{
		Environment:    "development",
		ExperimentKey:  "bad_override",
		Name:           "Bad",
		ControlVariant: "a",
		Variants: []Variant{
			{Key: "a", Name: "A", WeightBps: 5000},
			{Key: "b", Name: "B", WeightBps: 5000},
		},
		Overrides: map[string]string{"u-1": "ghost"},
	}); err == nil {
		t.Fatal("expected error for override targeting unknown variant")
	}
}

func TestCanTransition(t *testing.T) {
	if !CanTransition(StatusDraft, StatusRunning) {
		t.Fatal("draft -> running must be allowed")
	}
	if CanTransition(StatusDraft, StatusCompleted) {
		t.Fatal("draft -> completed must be rejected")
	}
	if !CanTransition(StatusRunning, StatusPaused) || !CanTransition(StatusRunning, StatusCompleted) {
		t.Fatal("running -> paused/completed must be allowed")
	}
	if !CanTransition(StatusPaused, StatusRunning) || !CanTransition(StatusPaused, StatusCompleted) {
		t.Fatal("paused -> running/completed must be allowed")
	}
	if CanTransition(StatusCompleted, StatusRunning) {
		t.Fatal("completed -> running must be rejected")
	}
}
