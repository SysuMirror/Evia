package access

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/howtomakeaname/Evia/backend/internal/platform/fault"
)

const auditTestReader = "11111111-1111-4111-8111-111111111111"
const auditTestProject = "22222222-2222-4222-8222-222222222222"

type auditTestStore struct {
	calls    int
	filter   AuditFilter
	position *AuditPosition
	limit    int
	err      error
}

func (s *auditTestStore) ListAuditLogs(_ context.Context, _, _ string, filter AuditFilter, position *AuditPosition, limit int) ([]AuditEntry, error) {
	s.calls++
	s.filter, s.position, s.limit = filter, position, limit
	return []AuditEntry{}, s.err
}

func auditTestFilter() AuditFilter {
	from := time.Date(2026, 11, 1, 0, 0, 0, 123456789, time.UTC)
	return AuditFilter{From: from, To: from.Add(time.Hour)}
}

func TestAuditLogValidation(t *testing.T) {
	for _, test := range []struct {
		name, code string
		change     func(*AuditFilter, **AuditPosition, *string, *string, *int)
	}{
		{"missing_from", "INVALID_AUDIT_RANGE", func(f *AuditFilter, _ **AuditPosition, _, _ *string, _ *int) { f.From = time.Time{} }},
		{"missing_to", "INVALID_AUDIT_RANGE", func(f *AuditFilter, _ **AuditPosition, _, _ *string, _ *int) { f.To = time.Time{} }},
		{"equal", "INVALID_AUDIT_RANGE", func(f *AuditFilter, _ **AuditPosition, _, _ *string, _ *int) { f.To = f.From }},
		{"reversed", "INVALID_AUDIT_RANGE", func(f *AuditFilter, _ **AuditPosition, _, _ *string, _ *int) { f.To = f.From.Add(-time.Nanosecond) }},
		{"too_long", "INVALID_AUDIT_RANGE", func(f *AuditFilter, _ **AuditPosition, _, _ *string, _ *int) {
			f.To = f.From.Add(31*24*time.Hour + time.Nanosecond)
		}},
		{"invalid_action", "INVALID_AUDIT_ACTION", func(f *AuditFilter, _ **AuditPosition, _, _ *string, _ *int) { f.Action = "project.*" }},
		{"unicode_action", "INVALID_AUDIT_ACTION", func(f *AuditFilter, _ **AuditPosition, _, _ *string, _ *int) { f.Action = "项目.read" }},
		{"uppercase_action", "INVALID_AUDIT_ACTION", func(f *AuditFilter, _ **AuditPosition, _, _ *string, _ *int) { f.Action = "Project.read" }},
		{"long_action", "INVALID_AUDIT_ACTION", func(f *AuditFilter, _ **AuditPosition, _, _ *string, _ *int) { f.Action = strings.Repeat("a", 129) }},
		{"actor", "INVALID_ACTOR_ID", func(f *AuditFilter, _ **AuditPosition, _, _ *string, _ *int) { f.ActorID = "bad" }},
		{"environment", "INVALID_AUDIT_ENVIRONMENT", func(f *AuditFilter, _ **AuditPosition, _, _ *string, _ *int) { f.Environment = "preview" }},
		{"outcome", "INVALID_AUDIT_OUTCOME", func(f *AuditFilter, _ **AuditPosition, _, _ *string, _ *int) { f.Outcome = "unknown" }},
		{"resource_prefix_long", "INVALID_AUDIT_RESOURCE", func(f *AuditFilter, _ **AuditPosition, _, _ *string, _ *int) {
			f.ResourceKeyPrefix = strings.Repeat("x", 513)
		}},
		{"reader", "INVALID_ACTOR_ID", func(_ *AuditFilter, _ **AuditPosition, reader, _ *string, _ *int) { *reader = "bad" }},
		{"project", "INVALID_PROJECT_ID", func(_ *AuditFilter, _ **AuditPosition, _, project *string, _ *int) { *project = "bad" }},
		{"zero_limit", "INVALID_LIMIT", func(_ *AuditFilter, _ **AuditPosition, _, _ *string, limit *int) { *limit = 0 }},
		{"big_limit", "INVALID_LIMIT", func(_ *AuditFilter, _ **AuditPosition, _, _ *string, limit *int) { *limit = 101 }},
	} {
		t.Run(test.name, func(t *testing.T) {
			store := &auditTestStore{}
			filter, reader, project, limit := auditTestFilter(), auditTestReader, auditTestProject, 10
			var after *AuditPosition
			test.change(&filter, &after, &reader, &project, &limit)
			_, err := NewAuditLogs(store).List(context.Background(), reader, project, filter, after, limit)
			var problem *fault.Error
			if !errors.As(err, &problem) || problem.Code != test.code || store.calls != 0 {
				t.Fatalf("validation=%v, store calls=%d", err, store.calls)
			}
		})
	}
}

func TestAuditLogCursorValidation(t *testing.T) {
	filter := auditTestFilter()
	for _, id := range []string{"", "0", "-1", "+1", "01", "1.0", "9223372036854775808", "1 OR TRUE"} {
		after := &AuditPosition{OccurredAt: filter.From.Add(time.Second).Truncate(time.Microsecond), ID: id}
		if e := ValidateAuditQuery(auditTestReader, auditTestProject, filter, after); e == nil {
			t.Fatalf("accepted id %q", id)
		}
	}
	for _, occurred := range []time.Time{filter.From.Add(-time.Second), filter.To, filter.From.Add(time.Second)} {
		if e := ValidateAuditQuery(auditTestReader, auditTestProject, filter, &AuditPosition{OccurredAt: occurred, ID: "1"}); e == nil {
			t.Fatalf("accepted timestamp %v", occurred)
		}
	}
}

func TestAuditLogUTCPrecisionLimitAndDependencyFailure(t *testing.T) {
	store := &auditTestStore{}
	svc := NewAuditLogs(store)
	filter := auditTestFilter()
	filter.To = filter.From.Add(31 * 24 * time.Hour)
	filter.From, filter.To = filter.From.In(time.FixedZone("offset", 19800)), filter.To.In(time.FixedZone("offset", 19800))
	filter.Action, filter.ActorID = strings.Repeat("a", 128), auditTestReader
	filter.Environment, filter.Outcome, filter.ResourceKeyPrefix = "development", AuditOutcomeFailure, strings.Repeat("资", 512)
	position := &AuditPosition{OccurredAt: filter.From.Add(time.Second).Truncate(time.Microsecond), ID: "9223372036854775807"}
	if _, err := svc.List(context.Background(), auditTestReader, auditTestProject, filter, position, 100); err != nil {
		t.Fatal(err)
	}
	if store.limit != 101 || store.filter.From.Location() != time.UTC || store.filter.From.Nanosecond() != 123456789 || store.position.OccurredAt.Location() != time.UTC || position.OccurredAt.Location() == time.UTC {
		t.Fatal("lost precision, UTC normalization, cursor ownership, or lookahead")
	}
	store.err = errors.New("synthetic dependency failure")
	if _, err := svc.List(context.Background(), auditTestReader, auditTestProject, filter, nil, 1); !errors.Is(err, store.err) {
		t.Fatal("dependency error lost", err)
	}
}

func TestAuditLogRejectsEveryMachineContext(t *testing.T) {
	for _, identity := range []Identity{{}, {PrincipalID: auditTestReader}, {CredentialID: auditTestProject}, {PrincipalID: auditTestReader, ProjectID: auditTestProject, Permissions: []string{PermissionProjectRead}}} {
		store := &auditTestStore{}
		_, err := NewAuditLogs(store).List(WithIdentity(context.Background(), identity), auditTestReader, auditTestProject, auditTestFilter(), nil, 10)
		var problem *fault.Error
		if !errors.As(err, &problem) || problem.Code != "MACHINE_FORBIDDEN" || store.calls != 0 {
			t.Fatal("machine reached audit persistence", err)
		}
	}
}
