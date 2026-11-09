package access

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"
)

type machineGrantStore struct {
	MachineStore
	principal MachinePrincipal
}

func (s *machineGrantStore) CreateMachinePrincipal(_ context.Context, _, _, _, _ string, principal MachinePrincipal, credential MachineCredential, _ string) (MachineIssued, bool, error) {
	s.principal = principal
	return MachineIssued{Principal: principal, Credential: credential}, true, nil
}

func TestMachineMetricPermissionsAreExplicitlyGrantable(t *testing.T) {
	metricPermissions := []string{PermissionMetricsRead, PermissionMetricsWrite, PermissionMetricsPublish, PermissionMetricsRun}
	all := make([]string, 0, len(machinePermissions))
	for permission := range machinePermissions {
		all = append(all, permission)
	}
	if len(all) != 24 {
		t.Fatalf("expected 24 explicit grants, got %d", len(all))
	}
	for _, permissions := range append([][]string{all}, []string{PermissionMetricsRead}, []string{PermissionMetricsWrite}, []string{PermissionMetricsPublish}, []string{PermissionMetricsRun}) {
		store := &machineGrantStore{}
		out, e := NewMachines(store).CreatePrincipal(context.Background(), "owner", "project", "create", MachineCreate{Name: "Metric automation", Environment: "development", Permissions: permissions, ExpiresAt: time.Now().Add(time.Hour)})
		if e != nil {
			t.Fatal(e)
		}
		expected := slices.Clone(permissions)
		slices.Sort(expected)
		if !slices.Equal(out.Principal.Permissions, expected) || !slices.Equal(store.principal.Permissions, expected) {
			t.Fatal("grant set changed during issuance")
		}
	}
	for _, permission := range metricPermissions {
		if !KnownMachinePermission(permission) {
			t.Fatalf("metric grant %q is unknown", permission)
		}
	}
	for _, permission := range []string{"metrics.*", "metrics", "metrics.delete"} {
		if KnownMachinePermission(permission) {
			t.Fatalf("unexpected implicit metric grant %q", permission)
		}
	}
}

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
