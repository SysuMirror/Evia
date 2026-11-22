package overview

import (
	"context"
	"errors"
	"testing"
	"time"
)

type authorityStub struct {
	calls int
	err   error
}

func (s *authorityStub) AuthorizeOverview(context.Context, string, string, string) error {
	s.calls++
	return s.err
}

type readerStub struct {
	calls   int
	request Request
	result  Result
	err     error
}

func (s *readerStub) ReadOverview(_ context.Context, request Request) (Result, error) {
	s.calls++
	s.request = request
	return s.result, s.err
}

func validRequest() Request {
	return Request{ProjectID: "11111111-1111-4111-8111-111111111111", Environment: "development", From: time.Date(2026, 11, 15, 0, 0, 0, 0, time.UTC), To: time.Date(2026, 11, 22, 0, 0, 0, 0, time.UTC), Interval: IntervalHour}
}

func TestServiceAuthorizesBeforeFacts(t *testing.T) {
	authority := &authorityStub{err: errors.New("denied")}
	reader := &readerStub{}
	_, err := NewService(authority, reader).Get(context.Background(), "actor", validRequest())
	if err == nil || authority.calls != 1 || reader.calls != 0 {
		t.Fatalf("authorization boundary not preserved: %v %d %d", err, authority.calls, reader.calls)
	}
}

func TestServiceValidatesBoundsBeforeDependencies(t *testing.T) {
	for name, change := range map[string]func(*Request){
		"environment": func(r *Request) { r.Environment = "other" },
		"ordering":    func(r *Request) { r.To = r.From },
		"range":       func(r *Request) { r.To = r.From.Add(MaxRange + time.Nanosecond) },
		"interval":    func(r *Request) { r.Interval = "minute" },
	} {
		t.Run(name, func(t *testing.T) {
			authority, reader := &authorityStub{}, &readerStub{}
			r := validRequest()
			change(&r)
			if _, err := NewService(authority, reader).Get(context.Background(), "actor", r); err == nil || authority.calls != 0 || reader.calls != 0 {
				t.Fatalf("invalid request reached dependencies: %v", err)
			}
		})
	}
}

func TestServiceReturnsHonestEmptyAggregateMetadata(t *testing.T) {
	authority, reader := &authorityStub{}, &readerStub{result: Result{}}
	service := NewService(authority, reader)
	service.now = func() time.Time { return time.Date(2026, 11, 22, 1, 2, 3, 0, time.FixedZone("offset", 3600)) }
	r := validRequest()
	r.Interval = ""
	result, err := service.Get(context.Background(), "actor", r)
	if err != nil {
		t.Fatal(err)
	}
	if reader.request.Interval != IntervalHour || result.Metadata.Accuracy != "exact" || result.Metadata.Coverage != "provisional" || len(result.Metadata.KnownGaps) != 1 {
		t.Fatalf("metadata/default mismatch: %+v %+v", reader.request, result.Metadata)
	}
	if result.Trend == nil || result.TopEvents == nil || result.TotalEvents != 0 || result.DataWatermark != nil {
		t.Fatalf("empty aggregate fabricated data: %+v", result)
	}
	if result.Metadata.GeneratedAt.Location() != time.UTC {
		t.Fatal("generated_at not UTC")
	}
}
