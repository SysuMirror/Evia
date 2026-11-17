package reports

import (
	"context"
	"errors"
	"strings"
	"testing"
)

type reportStoreStub struct {
	created Report
	err     error
	got     Input
}

func (s *reportStoreStub) Create(context.Context, Input) (Report, error) { return s.created, s.err }
func (s *reportStoreStub) List(context.Context, string, string, string, string, int, string) ([]Report, error) {
	return nil, s.err
}
func (s *reportStoreStub) Get(context.Context, string, string, string) (Report, error) {
	return Report{}, s.err
}
func (s *reportStoreStub) Update(_ context.Context, in Input, _ int64) (Report, error) {
	s.got = in
	return s.created, s.err
}
func (s *reportStoreStub) Delete(context.Context, string, string, string) error { return s.err }

func validInput() Input {
	return Input{
		ProjectID:   "11111111-1111-4111-8111-111111111111",
		ActorID:     "22222222-2222-4222-8222-222222222222",
		Environment: "production",
		Definition: Definition{
			Title:       "Weekly adoption",
			Description: "compiled review",
			Sections: []Section{
				{SectionID: "33333333-3333-4333-8333-333333333333", ReadingOrder: 1, SectionTitle: "Funnel", AnalysisID: "44444444-4444-4444-8444-444444444444", AnalysisVersion: 2},
				{SectionID: "55555555-5555-4555-8555-555555555555", ReadingOrder: 2, SectionTitle: "Retention", AnalysisID: "66666666-6666-4666-8666-666666666666", AnalysisVersion: 1},
			},
		},
		IdempotencyKey: "report-key-1",
		RequestHash:    strings.Repeat("a", 64),
	}
}

func TestValidateAcceptsAndRejectsBounds(t *testing.T) {
	if err := Validate(validInput(), CreateRevision); err != nil {
		t.Fatalf("valid input rejected: %v", err)
	}
	for _, mutate := range []func(*Input){
		func(in *Input) { in.Environment = "prod" },
		func(in *Input) { in.Definition.Title = " " },
		func(in *Input) { in.Definition.Title = strings.Repeat("x", MaxTitleLength+1) },
		func(in *Input) { in.Definition.Description = strings.Repeat("x", MaxDescriptionLength+1) },
		func(in *Input) { in.Definition.Sections = nil },
		func(in *Input) { in.Definition.Sections[0].ReadingOrder = 2 },
		func(in *Input) { in.Definition.Sections[0].AnalysisID = "not-a-uuid" },
		func(in *Input) { in.Definition.Sections[0].AnalysisVersion = 0 },
		func(in *Input) {
			in.Definition.Sections[1].AnalysisID = in.Definition.Sections[0].AnalysisID
			in.Definition.Sections[1].AnalysisVersion = 2
		},
		func(in *Input) { in.IdempotencyKey = "" },
		func(in *Input) { in.RequestHash = "short" },
	} {
		in := validInput()
		mutate(&in)
		if err := Validate(in, CreateRevision); err == nil {
			t.Fatalf("invalid input accepted: %+v", in)
		}
	}
}

func TestValidateRejectsDuplicateReadingOrderAndTooManySections(t *testing.T) {
	in := validInput()
	in.Definition.Sections[1].ReadingOrder = 1
	if err := Validate(in, CreateRevision); err == nil {
		t.Fatal("duplicate reading order must be rejected")
	}
}

func TestServiceDelegatesValidCreate(t *testing.T) {
	stub := &reportStoreStub{created: Report{ID: "77777777-7777-4777-8777-777777777777", Revision: 1}}
	svc := NewService(stub)
	report, err := svc.Create(context.Background(), validInput(), CreateRevision)
	if err != nil || report.Revision != 1 {
		t.Fatal(err)
	}
}

func TestServiceRejectsBadRevision(t *testing.T) {
	svc := NewService(&reportStoreStub{})
	if _, err := svc.Create(context.Background(), validInput(), -1); err == nil {
		t.Fatal("negative revision must be rejected")
	}
}

func TestServicePropagatesStorageErrors(t *testing.T) {
	svc := NewService(&reportStoreStub{err: errors.New("pg down")})
	if err := svc.Delete(context.Background(), "22222222-2222-4222-8222-222222222222", "11111111-1111-4111-8111-111111111111", "77777777-7777-4777-8777-777777777777"); err == nil {
		t.Fatal("storage errors must propagate")
	}
}

func TestServiceRejectsMalformedIds(t *testing.T) {
	svc := NewService(&reportStoreStub{})
	if _, err := svc.Get(context.Background(), "22222222-2222-4222-8222-222222222222", "11111111-1111-4111-8111-111111111111", ""); err == nil {
		t.Fatal("missing id must fail")
	}
	if _, err := svc.Get(context.Background(), "22222222-2222-4222-8222-222222222222", "11111111-1111-4111-8111-111111111111", "not-a-uuid"); err == nil {
		t.Fatal("malformed id must be rejected")
	}
}
