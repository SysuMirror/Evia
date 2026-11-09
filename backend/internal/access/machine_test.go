package access

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestMachineIdentityContextOwnsPermissionMetadata(t *testing.T) {
	identity := Identity{PrincipalID: "principal", CredentialID: "credential", Permissions: []string{PermissionQueriesRead}}
	ctx := WithIdentity(context.Background(), identity)
	identity.Permissions[0] = PermissionQueriesRawRead
	first, ok := IdentityFromContext(ctx)
	if !ok || first.Permissions[0] != PermissionQueriesRead {
		t.Fatal("identity shared mutable input")
	}
	first.Permissions[0] = PermissionQueriesRawRead
	second, _ := IdentityFromContext(ctx)
	if second.Permissions[0] != PermissionQueriesRead {
		t.Fatal("identity shared mutable output")
	}
	cleared, _ := IdentityFromContext(WithIdentity(ctx, Identity{}))
	if cleared.PrincipalID != "" || cleared.CredentialID != "" {
		t.Fatal("worker could not clear incoming machine identity")
	}
}
func TestMachineInputsRejectUnknownAndAmbiguousAuthority(t *testing.T) {
	s := NewMachines(nil)
	valid := MachineCreate{Name: "Agent", Environment: "development", Permissions: []string{PermissionProjectRead}, ExpiresAt: time.Now().Add(time.Hour)}
	for _, mutate := range []func(*MachineCreate){
		func(in *MachineCreate) { in.Name = strings.Repeat("界", 129) },
		func(in *MachineCreate) { in.Name = "a\x00b" },
		func(in *MachineCreate) { in.Environment = "all" },
		func(in *MachineCreate) { in.Permissions = []string{"*"} },
		func(in *MachineCreate) { in.Permissions = []string{PermissionProjectRead, PermissionProjectRead} },
		func(in *MachineCreate) { in.Permissions = nil },
	} {
		in := valid
		mutate(&in)
		if _, e := s.CreatePrincipal(context.Background(), "actor", "project", "key", in); e == nil {
			t.Fatal("invalid authority accepted")
		}
	}
	if _, e := s.AuthenticateMachine(context.Background(), strings.Repeat("s", 43)); e == nil {
		t.Fatal("session or collection credential accepted")
	}
	if _, e := s.RotateCredential(context.Background(), "actor", "project", "principal", 1, "key", MachineRotate{OverlapSeconds: 301}); e == nil {
		t.Fatal("unbounded overlap accepted")
	}
	for _, key := range []string{"", "space key", "换", "line\nbreak", strings.Repeat("k", 129)} {
		if _, e := s.CreatePrincipal(context.Background(), "actor", "project", key, valid); e == nil {
			t.Fatal("invalid idempotency key accepted")
		}
	}
}

func TestMachineCredentialExpiryIsBoundedAtDurableIssuance(t *testing.T) {
	now := time.Date(2026, 11, 9, 0, 0, 0, 0, time.UTC)
	for _, duration := range []time.Duration{time.Minute, 90 * 24 * time.Hour} {
		if e := ValidateMachineCredentialExpiry(now.Add(duration), now); e != nil {
			t.Fatal(e)
		}
	}
	for _, duration := range []time.Duration{time.Minute - time.Nanosecond, 90*24*time.Hour + time.Nanosecond} {
		if e := ValidateMachineCredentialExpiry(now.Add(duration), now); e == nil {
			t.Fatal("invalid credential lifetime accepted")
		}
	}
}
