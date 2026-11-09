package ingestion

import (
	"bytes"
	"context"
	"errors"
	"reflect"
	"testing"
)

type sourceOperationStore struct {
	calls          []string
	source         Source
	preflightError error
}

func (s *sourceOperationStore) CreateSource(context.Context, string, string, Source, Credential) (Source, error) {
	return Source{}, errors.New("unexpected create")
}
func (s *sourceOperationStore) ListSources(context.Context, string, string, string, string, int) ([]Source, error) {
	return nil, errors.New("unexpected list")
}
func (s *sourceOperationStore) GetSource(context.Context, string, string, string) (Source, error) {
	s.calls = append(s.calls, "read")
	return Source{}, errors.New("caller has no read grant")
}
func (s *sourceOperationStore) GetSourceForUpdate(context.Context, string, string, string) (Source, error) {
	s.calls = append(s.calls, "update-preflight")
	return s.source, s.preflightError
}
func (s *sourceOperationStore) GetSourceForRotation(context.Context, string, string, string) (Source, error) {
	s.calls = append(s.calls, "rotate-preflight")
	return s.source, s.preflightError
}
func (s *sourceOperationStore) UpdateSource(context.Context, string, string, string, int64, SourceUpdate) (Source, error) {
	s.calls = append(s.calls, "update")
	return s.source, nil
}
func (s *sourceOperationStore) RotateSource(_ context.Context, _, _, _, _ string, _ int64, c Credential) (Source, Credential, error) {
	s.calls = append(s.calls, "rotate")
	return s.source, c, nil
}

func TestSourceMutationsUseOperationAuthorityForPreflight(t *testing.T) {
	denied := errors.New("current operation denied")
	for _, operation := range []string{"update", "rotate"} {
		for _, preflightError := range []error{nil, denied} {
			t.Run(operation+"/"+map[bool]string{true: "denied", false: "allowed"}[preflightError != nil], func(t *testing.T) {
				store := &sourceOperationStore{source: Source{ID: "source", Kind: "server"}, preflightError: preflightError}
				service, err := NewSourceService(store, bytes.Repeat([]byte{7}, 32), nil)
				if err != nil {
					t.Fatal(err)
				}
				if operation == "update" {
					_, err = service.UpdateSource(context.Background(), "actor", "project", "source", 1, SourceUpdate{AllowedOrigins: &[]string{}})
				} else {
					_, err = service.RotateSource(context.Background(), "actor", "project", "source", "rotate", 0)
				}
				if !errors.Is(err, preflightError) {
					t.Fatalf("preflight error = %v, want %v", err, preflightError)
				}
				want := []string{operation + "-preflight"}
				if preflightError == nil {
					want = append(want, operation)
				}
				if !reflect.DeepEqual(store.calls, want) {
					t.Fatalf("calls %v, want %v", store.calls, want)
				}
			})
		}
	}
}
