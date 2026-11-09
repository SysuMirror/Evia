package control

import (
	"context"
	"net/http"
	"strconv"
	"time"

	"github.com/howtomakeaname/Evia/backend/internal/access"
	"github.com/howtomakeaname/Evia/backend/internal/gen/wire"
	"github.com/howtomakeaname/Evia/backend/internal/platform/fault"
	"github.com/howtomakeaname/Evia/backend/internal/platform/httpapi"
)

type MachineService interface {
	ListPrincipals(context.Context, string, string, string, int) ([]access.MachinePrincipal, error)
	GetPrincipal(context.Context, string, string, string) (access.MachinePrincipalDetail, error)
	CreatePrincipal(context.Context, string, string, string, access.MachineCreate) (access.MachineIssued, error)
	RotateCredential(context.Context, string, string, string, int64, string, access.MachineRotate) (access.MachineIssued, error)
	RevokePrincipal(context.Context, string, string, string, int64) (access.MachinePrincipal, error)
	RevokeCredential(context.Context, string, string, string, string, int64) (access.MachinePrincipal, error)
	AuthenticateMachine(context.Context, string) (access.Identity, error)
}

func (a *API) registerMachines(mux *http.ServeMux) {
	if a.Machines == nil {
		return
	}
	const base = "/api/v1/projects/{project_id}/machine-principals"
	mux.HandleFunc("GET /api/v1/machine-identity", a.projectAuth(false, a.machineIdentity))
	mux.HandleFunc("GET "+base, a.auth(false, a.listMachinePrincipals))
	mux.HandleFunc("POST "+base, a.auth(true, a.createMachinePrincipal))
	mux.HandleFunc("GET "+base+"/{principal_id}", a.auth(false, a.getMachinePrincipal))
	mux.HandleFunc("DELETE "+base+"/{principal_id}", a.auth(true, a.revokeMachinePrincipal))
	mux.HandleFunc("POST "+base+"/{principal_id}/credential-rotations", a.auth(true, a.rotateMachineCredential))
	mux.HandleFunc("DELETE "+base+"/{principal_id}/credentials/{credential_id}", a.auth(true, a.revokeMachineCredential))
}

func (a *API) machineScope(r *http.Request, actor authorizedActor, list bool) (string, error) {
	for _, name := range []string{"principal_id", "credential_id"} {
		if id := r.PathValue(name); id != "" && !catalogUUID.MatchString(id) {
			return "", fault.New(fault.Invalid, "INVALID_MACHINE_ID", "机器主体或凭据标识无效")
		}
	}
	if _, e := savedQuery(r, list, false); e != nil {
		return "", e
	}
	// The store requires the current human owner for reads as well as writes.
	return a.catalogProject(r, actor, false)
}

func machineRevision(r *http.Request) (int64, error) {
	if len(r.Header.Values("If-Match")) != 1 || !catalogETag.MatchString(r.Header.Get("If-Match")) {
		return 0, fault.New(fault.Invalid, "REVISION_REQUIRED", "请提供单一带双引号的 If-Match 版本")
	}
	return httpapi.IfMatch(r)
}

func machineETag(w http.ResponseWriter, revision int64) {
	w.Header().Set("ETag", strconv.Quote(strconv.FormatInt(revision, 10)))
}

func (a *API) machineIdentity(w http.ResponseWriter, r *http.Request, actor authorizedActor) error {
	if _, e := savedQuery(r, false, false); e != nil {
		return e
	}
	identity, ok := access.IdentityFromContext(r.Context())
	if !ok || identity.PrincipalID != actor.ID {
		return fault.New(fault.Unauthenticated, "MACHINE_CREDENTIAL_REQUIRED", "此接口需要机器管理凭据")
	}
	out := wire.MachineIdentity{PrincipalID: identity.PrincipalID, CredentialID: identity.CredentialID, ProjectID: identity.ProjectID, Environment: identity.Environment, Name: identity.Name, Permissions: machinePermissions(identity.Permissions), ExpiresAt: identity.ExpiresAt.UTC().Format(time.RFC3339Nano)}
	return a.Boundary.JSON(w, 200, "MachineIdentity", out)
}

func (a *API) listMachinePrincipals(w http.ResponseWriter, r *http.Request, actor authorizedActor) error {
	project, e := a.machineScope(r, actor, true)
	if e != nil {
		return e
	}
	limit, e := httpapi.Limit(r)
	if e != nil {
		return e
	}
	scope := "machine_principals:" + actor.ID + ":" + project
	after, e := a.Cursor.Decode(scope, r.URL.Query().Get("cursor"))
	if e != nil || after != "" && !catalogUUID.MatchString(after) {
		return fault.New(fault.Invalid, "INVALID_CURSOR", "分页游标已失效，请重新加载")
	}
	items, e := a.Machines.ListPrincipals(r.Context(), actor.ID, project, after, limit)
	if e != nil {
		return e
	}
	out := wire.MachinePrincipalList{Items: []wire.MachinePrincipal{}}
	if len(items) > limit {
		next := a.Cursor.Encode(scope, items[limit-1].ID)
		out.NextCursor = &next
		items = items[:limit]
	}
	for _, item := range items {
		out.Items = append(out.Items, machinePrincipalWire(item))
	}
	return a.Boundary.JSON(w, 200, "MachinePrincipalList", out)
}

func (a *API) getMachinePrincipal(w http.ResponseWriter, r *http.Request, actor authorizedActor) error {
	project, e := a.machineScope(r, actor, false)
	if e != nil {
		return e
	}
	out, e := a.Machines.GetPrincipal(r.Context(), actor.ID, project, r.PathValue("principal_id"))
	if e != nil {
		return e
	}
	result := wire.MachinePrincipalDetail{Principal: machinePrincipalWire(out.Principal), Credentials: []wire.MachineCredential{}}
	for _, credential := range out.Credentials {
		result.Credentials = append(result.Credentials, machineCredentialWire(credential))
	}
	machineETag(w, out.Principal.Revision)
	return a.Boundary.JSON(w, 200, "MachinePrincipalDetail", result)
}

func (a *API) createMachinePrincipal(w http.ResponseWriter, r *http.Request, actor authorizedActor) error {
	project, e := a.machineScope(r, actor, false)
	if e != nil {
		return e
	}
	key, e := savedIdempotency(r)
	if e != nil {
		return e
	}
	var in wire.MachinePrincipalInput
	if e = a.Boundary.Decode(w, r, "MachinePrincipalInput", &in); e != nil {
		return e
	}
	expires, e := time.Parse(time.RFC3339Nano, in.ExpiresAt)
	if e != nil {
		return fault.New(fault.Invalid, "INVALID_EXPIRY", "凭据到期时间无效")
	}
	permissions := make([]string, len(in.Permissions))
	for i, permission := range in.Permissions {
		permissions[i] = string(permission)
	}
	out, e := a.Machines.CreatePrincipal(r.Context(), actor.ID, project, key, access.MachineCreate{Name: in.Name, Environment: in.Environment, Permissions: permissions, ExpiresAt: expires})
	if e != nil {
		return e
	}
	machineETag(w, out.Principal.Revision)
	return a.Boundary.JSON(w, 201, "MachineIssued", machineIssuedWire(out))
}

func (a *API) rotateMachineCredential(w http.ResponseWriter, r *http.Request, actor authorizedActor) error {
	project, e := a.machineScope(r, actor, false)
	if e != nil {
		return e
	}
	revision, e := machineRevision(r)
	if e != nil {
		return e
	}
	key, e := savedIdempotency(r)
	if e != nil {
		return e
	}
	var in wire.MachineCredentialRotation
	if e = a.Boundary.Decode(w, r, "MachineCredentialRotation", &in); e != nil {
		return e
	}
	expires, e := time.Parse(time.RFC3339Nano, in.ExpiresAt)
	if e != nil {
		return fault.New(fault.Invalid, "INVALID_EXPIRY", "凭据到期时间无效")
	}
	out, e := a.Machines.RotateCredential(r.Context(), actor.ID, project, r.PathValue("principal_id"), revision, key, access.MachineRotate{ExpiresAt: expires, OverlapSeconds: int(in.OverlapSeconds)})
	if e != nil {
		return e
	}
	machineETag(w, out.Principal.Revision)
	return a.Boundary.JSON(w, 201, "MachineIssued", machineIssuedWire(out))
}

func (a *API) revokeMachinePrincipal(w http.ResponseWriter, r *http.Request, actor authorizedActor) error {
	return a.revokeMachine(w, r, actor, false)
}

func (a *API) revokeMachineCredential(w http.ResponseWriter, r *http.Request, actor authorizedActor) error {
	return a.revokeMachine(w, r, actor, true)
}

func (a *API) revokeMachine(w http.ResponseWriter, r *http.Request, actor authorizedActor, credential bool) error {
	project, e := a.machineScope(r, actor, false)
	if e != nil {
		return e
	}
	revision, e := machineRevision(r)
	if e != nil {
		return e
	}
	var out access.MachinePrincipal
	if credential {
		out, e = a.Machines.RevokeCredential(r.Context(), actor.ID, project, r.PathValue("principal_id"), r.PathValue("credential_id"), revision)
	} else {
		out, e = a.Machines.RevokePrincipal(r.Context(), actor.ID, project, r.PathValue("principal_id"), revision)
	}
	if e != nil {
		return e
	}
	machineETag(w, out.Revision)
	return a.Boundary.JSON(w, 200, "MachinePrincipal", machinePrincipalWire(out))
}

func machinePermissions(permissions []string) []wire.MachinePermission {
	out := make([]wire.MachinePermission, len(permissions))
	for i, permission := range permissions {
		out[i] = wire.MachinePermission(permission)
	}
	return out
}

func machineTime(value *time.Time) *string {
	if value == nil {
		return nil
	}
	result := value.UTC().Format(time.RFC3339Nano)
	return &result
}

func machinePrincipalWire(p access.MachinePrincipal) wire.MachinePrincipal {
	return wire.MachinePrincipal{ID: p.ID, ProjectID: p.ProjectID, Environment: p.Environment, Name: p.Name, Permissions: machinePermissions(p.Permissions), Revision: p.Revision, CreatedAt: p.CreatedAt.UTC().Format(time.RFC3339Nano), RevokedAt: machineTime(p.RevokedAt)}
}

func machineCredentialWire(c access.MachineCredential) wire.MachineCredential {
	return wire.MachineCredential{ID: c.ID, PrincipalID: c.PrincipalID, CreatedAt: c.CreatedAt.UTC().Format(time.RFC3339Nano), ExpiresAt: c.ExpiresAt.UTC().Format(time.RFC3339Nano), RevokedAt: machineTime(c.RevokedAt)}
}

func machineIssuedWire(in access.MachineIssued) wire.MachineIssued {
	out := wire.MachineIssued{Principal: machinePrincipalWire(in.Principal), Credential: machineCredentialWire(in.Credential), SecretAvailable: in.SecretOnce != ""}
	if out.SecretAvailable {
		out.SecretOnce = &in.SecretOnce
	}
	return out
}
