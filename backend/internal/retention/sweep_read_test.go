package retention

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/howtomakeaname/Evia/backend/internal/access"
	"github.com/howtomakeaname/Evia/backend/internal/platform/fault"
)

type fakeSweepReader struct {
	gotLimit  int
	gotFilter SweepFilter
	views     []SweepView
	calls     int
}

func (f *fakeSweepReader) ListSweeps(_ context.Context, _, _ string, filter SweepFilter, limit int) ([]SweepView, error) {
	f.calls++
	f.gotLimit = limit
	f.gotFilter = filter
	return f.views, nil
}

func sampleViews() []SweepView {
	cutoff := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	return []SweepView{
		{
			ID: "11111111-1111-4111-8111-111111111111", ProjectID: validUUID, Environment: "production",
			RetentionDays: 90, Cutoff: cutoff, DryRun: true, RowsCandidates: 5,
			Status: SweepSucceeded, CreatedAt: time.Now().UTC(),
		},
	}
}

func TestListSweepsDefaultsLimitAndPassesFilter(t *testing.T) {
	reader := &fakeSweepReader{views: sampleViews()}
	svc := NewServiceWithSweepReader(&fakeStore{}, reader)

	views, err := svc.ListSweeps(context.Background(), validUUID, validUUID, SweepFilter{Environment: "staging"}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(views) != 1 || reader.gotLimit != DefaultSweepLimit || reader.gotFilter.Environment != "staging" {
		t.Fatalf("default limit/filter wrong: limit=%d filter=%+v views=%d", reader.gotLimit, reader.gotFilter, len(views))
	}
}

func TestListSweepsRejectsInvalidLimitAndEnvironment(t *testing.T) {
	svc := NewServiceWithSweepReader(&fakeStore{}, &fakeSweepReader{})
	for _, limit := range []int{-1, MaxSweepLimit + 1} {
		if _, err := svc.ListSweeps(context.Background(), validUUID, validUUID, SweepFilter{}, limit); err == nil {
			t.Fatalf("limit %d must be rejected", limit)
		}
	}
	if _, err := svc.ListSweeps(context.Background(), validUUID, validUUID, SweepFilter{Environment: "edge"}, 10); err == nil {
		t.Fatal("invalid environment must be rejected")
	}
}

func TestListSweepsRejectsMachineAndBadCaller(t *testing.T) {
	svc := NewServiceWithSweepReader(&fakeStore{}, &fakeSweepReader{})
	machineCtx := access.WithIdentity(context.Background(), access.Identity{PrincipalID: validUUID, CredentialID: "cred"})
	if _, err := svc.ListSweeps(machineCtx, validUUID, validUUID, SweepFilter{}, 10); err == nil {
		t.Fatal("machine reader must be forbidden")
	}
	if _, err := svc.ListSweeps(context.Background(), "not-a-uuid", validUUID, SweepFilter{}, 10); err == nil {
		t.Fatal("malformed actor must be rejected")
	}
}

func TestListSweepsWithoutReaderReportsUnavailable(t *testing.T) {
	svc := NewService(&fakeStore{})
	_, err := svc.ListSweeps(context.Background(), validUUID, validUUID, SweepFilter{}, 10)
	var known *fault.Error
	if !errors.As(err, &known) || known.Kind != fault.Unavailable {
		t.Fatalf("expected unavailable fault, got %v", err)
	}
}

func TestSweepViewConstantsStable(t *testing.T) {
	if DefaultSweepLimit != 20 || MaxSweepLimit != 100 {
		t.Fatal("sweep list bounds drifted")
	}
}
