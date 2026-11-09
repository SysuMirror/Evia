package access

import (
	"context"
	"errors"
	"testing"

	"github.com/howtomakeaname/Evia/backend/internal/platform/fault"
)

type membershipStub struct {
	filter MemberListFilter
	in     MemberInput
	hash   string
	calls  int
	err    error
}

func (s *membershipStub) ListProjectMembers(_ context.Context, _, _ string, in MemberListFilter) (MemberPage, error) {
	s.calls++
	s.filter = in
	return MemberPage{Revision: 7}, s.err
}
func (s *membershipStub) MutateProjectMember(_ context.Context, _, _, _, _ string, in MemberInput, hash string) (MemberResult, error) {
	s.calls++
	s.in = in
	s.hash = hash
	return MemberResult{Revision: 8}, s.err
}

const membershipProject = "00000000-0000-4000-8000-000000000001"
const membershipUser = "00000000-0000-4000-8000-000000000002"

func membershipCode(t *testing.T, e error, code string) {
	t.Helper()
	var f *fault.Error
	if !errors.As(e, &f) || f.Code != code {
		t.Fatalf("want %s, got %v", code, e)
	}
}
func TestMembershipValidationRejectsBeforePersistence(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   MemberInput
		code string
	}{
		{"missing revision", MemberInput{Username: "existing", Role: "viewer", IdempotencyKey: "key"}, "REVISION_REQUIRED"},
		{"unsafe revision", MemberInput{Username: "existing", Role: "viewer", Revision: MaxMembershipRevision + 1, IdempotencyKey: "key"}, "REVISION_REQUIRED"},
		{"missing key", MemberInput{Username: "existing", Role: "viewer", Revision: 1}, "INVALID_IDEMPOTENCY_KEY"},
		{"key control", MemberInput{Username: "existing", Role: "viewer", Revision: 1, IdempotencyKey: "bad\nkey"}, "INVALID_IDEMPOTENCY_KEY"},
		{"unknown role", MemberInput{Username: "existing", Role: "admin", Revision: 1, IdempotencyKey: "key"}, "INVALID_MEMBER_ROLE"},
		{"username invalid", MemberInput{Username: "bad user", Role: "viewer", Revision: 1, IdempotencyKey: "key"}, "INVALID_USERNAME"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st := &membershipStub{}
			_, e := NewMemberships(st).Add(context.Background(), membershipUser, membershipProject, tc.in)
			membershipCode(t, e, tc.code)
			if st.calls != 0 {
				t.Fatal("invalid input reached persistence")
			}
		})
	}
}
func TestMembershipHumanIdentityCannotBeSpoofed(t *testing.T) {
	for _, identity := range []Identity{{}, {PrincipalID: membershipUser}, {CredentialID: membershipUser}} {
		st := &membershipStub{}
		svc := NewMemberships(st)
		ctx := WithIdentity(context.Background(), identity)
		_, e := svc.List(ctx, membershipUser, membershipProject, MemberListFilter{Limit: 10})
		membershipCode(t, e, "MACHINE_FORBIDDEN")
		_, e = svc.Add(ctx, membershipUser, membershipProject, MemberInput{Username: "existing", Role: "owner", Revision: 1, IdempotencyKey: "key"})
		membershipCode(t, e, "MACHINE_FORBIDDEN")
		_, e = svc.Update(ctx, membershipUser, membershipProject, membershipUser, MemberInput{Role: "owner", Revision: 1, IdempotencyKey: "key"})
		membershipCode(t, e, "MACHINE_FORBIDDEN")
		_, e = svc.Remove(ctx, membershipUser, membershipProject, membershipUser, MemberInput{Revision: 1, IdempotencyKey: "key"})
		membershipCode(t, e, "MACHINE_FORBIDDEN")
		if st.calls != 0 {
			t.Fatal("machine identity reached human persistence")
		}
	}
}
func TestMembershipCanonicalReplayAndBoundedPagination(t *testing.T) {
	st := &membershipStub{}
	svc := NewMemberships(st)
	ctx := context.Background()
	in := MemberInput{Username: "  Existing.User  ", Role: "editor", Revision: 3, IdempotencyKey: "key"}
	if _, e := svc.Add(ctx, membershipUser, membershipProject, in); e != nil {
		t.Fatal(e)
	}
	hash := st.hash
	if st.in.Username != "existing.user" {
		t.Fatal("account was not normalized")
	}
	in.Username = "existing.user"
	in.IdempotencyKey = "different-key"
	if _, e := svc.Add(ctx, membershipUser, membershipProject, in); e != nil || st.hash != hash {
		t.Fatal("normalized replay changed request content", e)
	}
	in.Revision++
	if _, e := svc.Add(ctx, membershipUser, membershipProject, in); e != nil || st.hash == hash {
		t.Fatal("revision omitted from request binding", e)
	}
	if _, e := svc.List(ctx, membershipUser, membershipProject, MemberListFilter{Limit: 100}); e != nil || st.filter.Limit != 101 {
		t.Fatal("missing one-row pagination lookahead", e)
	}
	_, e := svc.List(ctx, membershipUser, membershipProject, MemberListFilter{After: membershipUser, Limit: 1})
	membershipCode(t, e, "INVALID_CURSOR")
	_, e = svc.List(ctx, membershipUser, membershipProject, MemberListFilter{Limit: 101})
	membershipCode(t, e, "INVALID_LIMIT")
	st.err = errors.New("dependency unavailable")
	_, e = svc.List(ctx, membershipUser, membershipProject, MemberListFilter{Limit: 1})
	if !errors.Is(e, st.err) {
		t.Fatal("dependency error lost")
	}
}
func TestMembershipRejectsAmbiguousMutationFields(t *testing.T) {
	st := &membershipStub{}
	svc := NewMemberships(st)
	ctx := context.Background()
	in := MemberInput{Username: "unwanted", Role: "editor", Revision: 1, IdempotencyKey: "key"}
	_, e := svc.Update(ctx, membershipUser, membershipProject, membershipUser, in)
	membershipCode(t, e, "INVALID_MEMBER_INPUT")
	in.Username = ""
	_, e = svc.Remove(ctx, membershipUser, membershipProject, membershipUser, in)
	membershipCode(t, e, "INVALID_MEMBER_INPUT")
	in.Role = ""
	_, e = svc.Remove(ctx, membershipUser, membershipProject, "invalid", in)
	membershipCode(t, e, "INVALID_MEMBER_ID")
	if st.calls != 0 {
		t.Fatal("ambiguous mutation reached persistence")
	}
}
