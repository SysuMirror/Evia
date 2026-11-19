package exporting

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/howtomakeaname/Evia/backend/internal/analysis"
	"github.com/howtomakeaname/Evia/backend/internal/platform/fault"
)

func validSavedExportInput() SavedAnalysisExportInput {
	return SavedAnalysisExportInput{
		ProjectID:      "11111111-1111-4111-8111-111111111111",
		ActorID:        "22222222-2222-4222-8222-222222222222",
		Name:           "saved analysis export",
		Format:         FormatJSON,
		TimeRange:      analysis.TimeRange{From: time.Now().Add(-24 * time.Hour), To: time.Now()},
		Timezone:       "UTC",
		IdempotencyKey: "saved-export-1",
		RequestHash:    strings.Repeat("a", 64),
	}
}

func TestValidateSavedAnalysisExportInputAcceptsCompleteInput(t *testing.T) {
	t.Parallel()
	// An omitted format keeps the documented json default.
	in := validSavedExportInput()
	in.Format = ""
	if err := ValidateSavedAnalysisExportInput(in); err != nil {
		t.Fatalf("valid input rejected: %v", err)
	}
}

func TestValidateSavedAnalysisExportInputRejectsBadShapes(t *testing.T) {
	t.Parallel()
	cases := map[string]func(SavedAnalysisExportInput) SavedAnalysisExportInput{
		"bad project uuid": func(in SavedAnalysisExportInput) SavedAnalysisExportInput { in.ProjectID = "nope"; return in },
		"bad actor uuid":   func(in SavedAnalysisExportInput) SavedAnalysisExportInput { in.ActorID = "nope"; return in },
		"bad format":       func(in SavedAnalysisExportInput) SavedAnalysisExportInput { in.Format = "avro"; return in },
		"empty name":       func(in SavedAnalysisExportInput) SavedAnalysisExportInput { in.Name = ""; return in },
		"overlong name": func(in SavedAnalysisExportInput) SavedAnalysisExportInput {
			in.Name = strings.Repeat("x", MaxNameLength+1)
			return in
		},
		"empty idempotency": func(in SavedAnalysisExportInput) SavedAnalysisExportInput { in.IdempotencyKey = ""; return in },
		"short hash":        func(in SavedAnalysisExportInput) SavedAnalysisExportInput { in.RequestHash = "abc"; return in },
		"zero time range": func(in SavedAnalysisExportInput) SavedAnalysisExportInput {
			in.TimeRange = analysis.TimeRange{}
			return in
		},
		"inverted time range": func(in SavedAnalysisExportInput) SavedAnalysisExportInput {
			in.TimeRange.From, in.TimeRange.To = in.TimeRange.To, in.TimeRange.From
			return in
		},
		"empty timezone": func(in SavedAnalysisExportInput) SavedAnalysisExportInput { in.Timezone = ""; return in },
		"expiry in the past": func(in SavedAnalysisExportInput) SavedAnalysisExportInput {
			past := time.Now().Add(-time.Minute)
			in.ArtifactExpiry = &past
			return in
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			err := ValidateSavedAnalysisExportInput(mutate(validSavedExportInput()))
			if err == nil {
				t.Fatalf("%s must be rejected", name)
			}
			var known *fault.Error
			if !errors.As(err, &known) || known.Code != invalidCode {
				t.Fatalf("%s must fail with %s, got %v", name, invalidCode, err)
			}
		})
	}
}

func TestServiceCreateSavedAnalysisExportValidatesBeforeStore(t *testing.T) {
	t.Parallel()
	svc := NewService(&fakeStore{})
	in := validSavedExportInput()
	in.Name = ""
	if _, _, err := svc.CreateSavedAnalysisExportJob(context.Background(), in.ActorID, in.ProjectID,
		"33333333-3333-4333-8333-333333333333", 1, in); err == nil {
		t.Fatal("service must reject invalid input before touching the store")
	}
}
