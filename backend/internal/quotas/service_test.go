package quotas

import (
	"context"
	"errors"
	"math"
	"strings"
	"testing"
)

type quotaStoreStub struct {
	policy Policy
	found  bool
	err    error
	got    Input
}

func (s *quotaStoreStub) GetQuota(context.Context, string, string, string) (Policy, bool, error) {
	return s.policy, s.found, s.err
}

func (s *quotaStoreStub) PutQuota(_ context.Context, in Input, _ int64) (Policy, bool, error) {
	s.got = in
	return s.policy, false, s.err
}

func (s *quotaStoreStub) ListQuotas(context.Context) ([]Policy, error) {
	return nil, s.err
}

func validInput() Input {
	return Input{
		ProjectID:      "11111111-1111-4111-8111-111111111111",
		ActorID:        "22222222-2222-4222-8222-222222222222",
		Environment:    "production",
		Rate:           12.5,
		Burst:          200,
		IdempotencyKey: "quota-key-1",
		RequestHash:    strings.Repeat("a", 64),
	}
}

func TestValidateAcceptsAndRejectsBounds(t *testing.T) {
	for _, mutate := range []func(*Input){
		func(in *Input) { in.Rate = 0 },
		func(in *Input) { in.Rate = MaxRate + 1 },
		func(in *Input) { in.Rate = math.NaN() },
		func(in *Input) { in.Burst = MinBurst - 1 },
		func(in *Input) { in.Burst = MaxBurst + 1 },
		func(in *Input) { in.Environment = "prod" },
		func(in *Input) { in.IdempotencyKey = "" },
		func(in *Input) { in.RequestHash = "short" },
		func(in *Input) { in.ProjectID = "not-a-uuid" },
	} {
		in := validInput()
		mutate(&in)
		if err := validate(in, CreateRevision); err == nil {
			t.Fatalf("invalid input accepted: %+v", in)
		}
	}
	if err := validate(validInput(), CreateRevision); err != nil {
		t.Fatalf("valid input rejected: %v", err)
	}
	if err := validate(validInput(), -1); err == nil {
		t.Fatal("negative revision must be rejected")
	}
}

func TestServicePutDelegatesValidInput(t *testing.T) {
	stub := &quotaStoreStub{policy: Policy{Revision: 1}}
	svc := NewService(stub)
	policy, replayed, err := svc.Put(context.Background(), validInput(), CreateRevision)
	if err != nil || policy.Revision != 1 || replayed {
		t.Fatal(err)
	}
	if stub.got.Rate != 12.5 || stub.got.Burst != 200 {
		t.Fatalf("store received wrong input: %+v", stub.got)
	}
}

func TestServiceGetRejectsBadEnvironment(t *testing.T) {
	svc := NewService(&quotaStoreStub{})
	if _, _, err := svc.Get(context.Background(), "22222222-2222-4222-8222-222222222222", "11111111-1111-4111-8111-111111111111", "prod"); err == nil {
		t.Fatal("bad environment must be rejected")
	}
}

func TestServicePropagatesStorageErrors(t *testing.T) {
	svc := NewService(&quotaStoreStub{err: errors.New("pg down")})
	if _, err := svc.List(context.Background()); err == nil {
		t.Fatal("storage errors must propagate")
	}
}
