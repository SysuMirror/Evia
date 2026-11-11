package retention

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/howtomakeaname/Evia/backend/internal/platform/fault"
)

const validUUID = "11111111-1111-4111-8111-111111111111"

type fakeStore struct {
	policy     Policy
	configured bool
	putCalls   int
}

func (f *fakeStore) GetPolicy(context.Context, string, string, string) (Policy, bool, error) {
	return f.policy, f.configured, nil
}

func (f *fakeStore) PutPolicy(_ context.Context, in Input, expected int64) (Policy, error) {
	f.putCalls++
	return Policy{ProjectID: in.ProjectID, Environment: in.Environment, RetentionDays: in.RetentionDays, Revision: expected + 1}, nil
}

func validInput() Input {
	return Input{
		ProjectID:      validUUID,
		ActorID:        validUUID,
		Environment:    "production",
		RetentionDays:  90,
		IdempotencyKey: "key-1",
		RequestHash:    strings.Repeat("a", 64),
	}
}

func faultCode(t *testing.T, err error) string {
	t.Helper()
	var known *fault.Error
	if err == nil {
		t.Fatal("expected a fault error, got nil")
	}
	if !errors.As(err, &known) {
		t.Fatalf("error is not a fault.Error: %v", err)
	}
	return known.Code
}

func TestRetentionInputValidation(t *testing.T) {
	svc := NewService(&fakeStore{})
	ctx := context.Background()
	cases := []struct {
		name     string
		revision int64
		mutate   func(*Input)
		wantCode string
	}{
		{"valid", 0, func(i *Input) {}, ""},
		{"bad environment", 0, func(i *Input) { i.Environment = "edge" }, "INVALID_RETENTION_INPUT"},
		{"days below floor", 0, func(i *Input) { i.RetentionDays = MinRetentionDays - 1 }, "INVALID_RETENTION_INPUT"},
		{"days above ceiling", 0, func(i *Input) { i.RetentionDays = MaxRetentionDays + 1 }, "INVALID_RETENTION_INPUT"},
		{"missing idempotency key", 0, func(i *Input) { i.IdempotencyKey = "" }, "INVALID_RETENTION_INPUT"},
		{"bad request hash", 0, func(i *Input) { i.RequestHash = "short" }, "INVALID_RETENTION_INPUT"},
		{"negative revision", -1, func(i *Input) {}, "REVISION_REQUIRED"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			in := validInput()
			tc.mutate(&in)
			_, err := svc.Put(ctx, in, tc.revision)
			if tc.wantCode == "" {
				if err != nil {
					t.Fatalf("expected acceptance, got %v", err)
				}
				return
			}
			if code := faultCode(t, err); code != tc.wantCode {
				t.Fatalf("want code %s, got %s", tc.wantCode, code)
			}
		})
	}
}

func TestRetentionGetRejectsInvalidEnvironment(t *testing.T) {
	svc := NewService(&fakeStore{})
	if _, _, err := svc.Get(context.Background(), validUUID, validUUID, "edge"); err == nil {
		t.Fatal("invalid environment must be rejected")
	}
}

func TestRetentionBoundsAreDocumented(t *testing.T) {
	if MinRetentionDays != 30 || MaxRetentionDays != 3650 || CreateRevision != 0 {
		t.Fatal("retention bounds drifted from the migration CHECK constraints")
	}
}
