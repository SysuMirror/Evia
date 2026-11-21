package access

import (
	"context"
	"errors"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/howtomakeaname/Evia/backend/internal/platform/canonical"
	"github.com/howtomakeaname/Evia/backend/internal/platform/env"
	"github.com/howtomakeaname/Evia/backend/internal/platform/fault"
	"github.com/howtomakeaname/Evia/backend/internal/platform/ids"
)

const (
	PermissionProjectRead             = "project.read"
	PermissionCatalogDraftsRead       = "catalog.drafts.read"
	PermissionCatalogDraftsWrite      = "catalog.drafts.write"
	PermissionCatalogReleasesRead     = "catalog.releases.read"
	PermissionCatalogReleasesPublish  = "catalog.releases.publish"
	PermissionCatalogReleasesActivate = "catalog.releases.activate"
	PermissionSourcesRead             = "sources.read"
	PermissionSourcesWrite            = "sources.write"
	PermissionSourcesRotate           = "sources.rotate"
	PermissionQueriesRead             = "queries.read"
	PermissionQueriesRun              = "queries.run"
	PermissionQueriesRawRead          = "queries.raw.read"
	PermissionAnalysesRead            = "analyses.read"
	PermissionAnalysesWrite           = "analyses.write"
	PermissionAnalysesPublish         = "analyses.publish"
	PermissionAnalysesRun             = "analyses.run"
	PermissionMetricsRead             = "metrics.read"
	PermissionMetricsWrite            = "metrics.write"
	PermissionMetricsPublish          = "metrics.publish"
	PermissionMetricsRun              = "metrics.run"
	PermissionDashboardsRead          = "dashboards.read"
	PermissionDashboardsWrite         = "dashboards.write"
	PermissionDashboardsPublish       = "dashboards.publish"
	PermissionDashboardsRun           = "dashboards.run"
	PermissionDataErasure             = "data.erasure"
	PermissionDataExport              = "data.export"
	PermissionContentRead             = "content.read"
)

var machinePermissions = map[string]bool{
	PermissionProjectRead: true, PermissionCatalogDraftsRead: true, PermissionCatalogDraftsWrite: true,
	PermissionCatalogReleasesRead: true, PermissionCatalogReleasesPublish: true, PermissionCatalogReleasesActivate: true,
	PermissionSourcesRead: true, PermissionSourcesWrite: true, PermissionSourcesRotate: true,
	PermissionQueriesRead: true, PermissionQueriesRun: true, PermissionQueriesRawRead: true,
	PermissionAnalysesRead: true, PermissionAnalysesWrite: true, PermissionAnalysesPublish: true, PermissionAnalysesRun: true,
	PermissionMetricsRead: true, PermissionMetricsWrite: true, PermissionMetricsPublish: true, PermissionMetricsRun: true,
	PermissionDashboardsRead: true, PermissionDashboardsWrite: true, PermissionDashboardsPublish: true, PermissionDashboardsRun: true,
	PermissionDataErasure: true,
	PermissionDataExport:  true,
	PermissionContentRead: true,
}

func KnownMachinePermission(permission string) bool { return machinePermissions[permission] }

type MachinePrincipal struct {
	ID, ProjectID, Environment, Name string
	Permissions                      []string
	Revision                         int64
	CreatedAt                        time.Time
	RevokedAt                        *time.Time
}
type MachineCredential struct {
	ID, PrincipalID      string
	CreatedAt, ExpiresAt time.Time
	RevokedAt            *time.Time
}
type MachinePrincipalDetail struct {
	Principal   MachinePrincipal
	Credentials []MachineCredential
}
type MachineIssued struct {
	Principal  MachinePrincipal
	Credential MachineCredential
	SecretOnce string
}
type MachineCreate struct {
	Name, Environment string
	Permissions       []string
	ExpiresAt         time.Time
}
type MachineRotate struct {
	ExpiresAt      time.Time
	OverlapSeconds int
}
type MachineStore interface {
	ListMachinePrincipals(context.Context, string, string, string, int) ([]MachinePrincipal, error)
	GetMachinePrincipal(context.Context, string, string, string) (MachinePrincipalDetail, error)
	CreateMachinePrincipal(context.Context, string, string, string, string, MachinePrincipal, MachineCredential, string) (MachineIssued, bool, error)
	RotateMachineCredential(context.Context, string, string, string, int64, string, string, MachineCredential, string, int) (MachineIssued, bool, error)
	RevokeMachinePrincipal(context.Context, string, string, string, int64) (MachinePrincipal, error)
	RevokeMachineCredential(context.Context, string, string, string, string, int64) (MachinePrincipal, error)
	FindMachineIdentity(context.Context, string) (Identity, error)
}
type Machines struct{ store MachineStore }

func NewMachines(store MachineStore) *Machines  { return &Machines{store: store} }
func machineInvalid(code, message string) error { return fault.New(fault.Invalid, code, message) }
func machineKey(key string) error {
	if len(key) < 1 || len(key) > 128 {
		return machineInvalid("INVALID_IDEMPOTENCY_KEY", "幂等键无效")
	}
	for _, char := range key {
		if char < 33 || char > 126 {
			return machineInvalid("INVALID_IDEMPOTENCY_KEY", "幂等键无效")
		}
	}
	return nil
}

// ValidateMachineCredentialExpiry evaluates a new issuance against the durable
// store's clock. Existing idempotency records are replayed before this check.
func ValidateMachineCredentialExpiry(expires, now time.Time) error {
	if expires.Before(now.Add(time.Minute)) || expires.After(now.Add(90*24*time.Hour)) {
		return machineInvalid("INVALID_CREDENTIAL_EXPIRY", "凭据有效期必须为 1 分钟到 90 天")
	}
	return nil
}
func (s *Machines) ListPrincipals(ctx context.Context, actor, project, after string, limit int) ([]MachinePrincipal, error) {
	if limit < 1 || limit > 200 {
		return nil, machineInvalid("INVALID_LIMIT", "分页大小超出范围")
	}
	return s.store.ListMachinePrincipals(ctx, actor, project, after, limit+1)
}
func (s *Machines) GetPrincipal(ctx context.Context, actor, project, id string) (MachinePrincipalDetail, error) {
	return s.store.GetMachinePrincipal(ctx, actor, project, id)
}
func (s *Machines) CreatePrincipal(ctx context.Context, actor, project, key string, in MachineCreate) (MachineIssued, error) {
	in.Name = strings.TrimSpace(in.Name)
	if !utf8.ValidString(in.Name) || strings.ContainsRune(in.Name, 0) || utf8.RuneCountInString(in.Name) < 1 || utf8.RuneCountInString(in.Name) > 128 {
		return MachineIssued{}, machineInvalid("INVALID_MACHINE_NAME", "机器主体名称必须为 1 到 128 个字符")
	}
	if !env.Valid(in.Environment) {
		return MachineIssued{}, machineInvalid("INVALID_ENVIRONMENT", "环境无效")
	}
	if len(in.Permissions) < 1 || len(in.Permissions) > len(machinePermissions) {
		return MachineIssued{}, machineInvalid("INVALID_MACHINE_PERMISSIONS", "机器权限无效")
	}
	in.Permissions = append([]string(nil), in.Permissions...)
	sort.Strings(in.Permissions)
	for i, p := range in.Permissions {
		if !KnownMachinePermission(p) || i > 0 && p == in.Permissions[i-1] {
			return MachineIssued{}, machineInvalid("INVALID_MACHINE_PERMISSIONS", "机器权限必须已知且不重复")
		}
	}
	if e := machineKey(key); e != nil {
		return MachineIssued{}, e
	}
	// Replay validation belongs to storage; expiry is evaluated only for a new issuance.
	in.ExpiresAt = in.ExpiresAt.UTC()
	hash, e := canonical.Hash(in)
	if e != nil {
		return MachineIssued{}, e
	}
	now := time.Now().UTC()
	principal := MachinePrincipal{ID: ids.New(), ProjectID: project, Environment: in.Environment, Name: in.Name, Permissions: in.Permissions, Revision: 1, CreatedAt: now}
	credential := MachineCredential{ID: ids.New(), PrincipalID: principal.ID, CreatedAt: now, ExpiresAt: in.ExpiresAt}
	secret := "evia_m_" + ids.Secret()
	out, created, e := s.store.CreateMachinePrincipal(ctx, actor, project, key, hash, principal, credential, Digest(secret))
	if e != nil {
		return MachineIssued{}, e
	}
	if created {
		out.SecretOnce = secret
	}
	return out, nil
}
func (s *Machines) RotateCredential(ctx context.Context, actor, project, id string, revision int64, key string, in MachineRotate) (MachineIssued, error) {
	if revision < 1 {
		return MachineIssued{}, machineInvalid("REVISION_REQUIRED", "必须提供有效修订")
	}
	if in.OverlapSeconds < 0 || in.OverlapSeconds > 300 {
		return MachineIssued{}, machineInvalid("INVALID_CREDENTIAL_OVERLAP", "轮换交叠期必须为 0 到 300 秒")
	}
	if e := machineKey(key); e != nil {
		return MachineIssued{}, e
	}
	in.ExpiresAt = in.ExpiresAt.UTC()
	hash, e := canonical.Hash(struct {
		Revision int64
		Input    MachineRotate
	}{revision, in})
	if e != nil {
		return MachineIssued{}, e
	}
	credential := MachineCredential{ID: ids.New(), PrincipalID: id, CreatedAt: time.Now().UTC(), ExpiresAt: in.ExpiresAt}
	secret := "evia_m_" + ids.Secret()
	out, created, e := s.store.RotateMachineCredential(ctx, actor, project, id, revision, key, hash, credential, Digest(secret), in.OverlapSeconds)
	if e != nil {
		return MachineIssued{}, e
	}
	if created {
		out.SecretOnce = secret
	}
	return out, nil
}
func (s *Machines) RevokePrincipal(ctx context.Context, actor, project, id string, revision int64) (MachinePrincipal, error) {
	if revision < 1 {
		return MachinePrincipal{}, machineInvalid("REVISION_REQUIRED", "必须提供有效修订")
	}
	return s.store.RevokeMachinePrincipal(ctx, actor, project, id, revision)
}
func (s *Machines) RevokeCredential(ctx context.Context, actor, project, id, credential string, revision int64) (MachinePrincipal, error) {
	if revision < 1 {
		return MachinePrincipal{}, machineInvalid("REVISION_REQUIRED", "必须提供有效修订")
	}
	return s.store.RevokeMachineCredential(ctx, actor, project, id, credential, revision)
}
func (s *Machines) AuthenticateMachine(ctx context.Context, secret string) (Identity, error) {
	denied := fault.New(fault.Unauthenticated, "MACHINE_CREDENTIAL_REQUIRED", "机器凭据无效或已失效")
	if len(secret) != 50 || !strings.HasPrefix(secret, "evia_m_") {
		return Identity{}, denied
	}
	identity, e := s.store.FindMachineIdentity(ctx, Digest(secret))
	if errors.Is(e, ErrMissing) {
		return Identity{}, denied
	}
	return identity, e
}
