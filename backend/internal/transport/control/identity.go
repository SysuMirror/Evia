package control

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"github.com/howtomakeaname/Evia/backend/internal/access"
	"github.com/howtomakeaname/Evia/backend/internal/gen/wire"
	"github.com/howtomakeaname/Evia/backend/internal/identity"
	"github.com/howtomakeaname/Evia/backend/internal/platform/fault"
)

// IdentityService is the persistence subset the identity HTTP surface needs.
type IdentityService interface {
	ListRevisions(ctx context.Context, actor, project, environment string, filter identity.ReadFilter) (identity.Page, error)
	GetRevision(ctx context.Context, actor, project, environment string, revision int64) (identity.Revision, error)
	ResolveMember(ctx context.Context, actor, project, environment string, revision int64, kind, value string) (identity.Resolution, error)
}

// IdentityAPI shares management authentication: revisions need queries.read,
// member resolution additionally requires queries.raw.read and is rechecked
// inside the store transaction.
type IdentityAPI struct {
	API     *API
	Service IdentityService
}

type identityPosition struct {
	AfterRevision int64 `json:"afterRevision"`
}

func (a *IdentityAPI) Register(mux *http.ServeMux) {
	if a.Service == nil {
		return
	}
	const base = "/api/v1/projects/{project_id}/identity-revisions"
	mux.HandleFunc("GET "+base, a.API.projectPermissionAuth(false, access.PermissionQueriesRead, a.list))
	mux.HandleFunc("GET "+base+"/{revision}", a.API.projectPermissionAuth(false, access.PermissionQueriesRead, a.get))
	mux.HandleFunc("GET "+base+"/{revision}/resolve", a.API.projectPermissionAuth(false, access.PermissionQueriesRawRead, a.resolve))
}

func identityRevisionWire(revision identity.Revision) wire.IdentityRevision {
	snapshotAvailable := revision.SnapshotAvailable
	out := wire.IdentityRevision{
		Revision:          revision.Revision,
		EdgeCount:         revision.EdgeCount,
		PersonCount:       revision.ActivePersonCount,
		MemberCount:       revision.MemberCount,
		State:             revision.State,
		SnapshotAvailable: &snapshotAvailable,
	}
	if revision.Watermark != nil {
		formatted := revision.Watermark.UTC().Format(time.RFC3339Nano)
		out.WatermarkReceivedAt = &formatted
	}
	if revision.BuiltAt != nil {
		formatted := revision.BuiltAt.UTC().Format(time.RFC3339Nano)
		out.BuiltAt = &formatted
	}
	if revision.ErrorCode != nil {
		out.ErrorCode = revision.ErrorCode
	}
	return out
}

func (a *IdentityAPI) list(w http.ResponseWriter, r *http.Request, actor authorizedActor) error {
	project, err := governanceProject(r)
	if err != nil {
		return err
	}
	q, err := governanceQuery(r, "environment", "limit", "cursor")
	if err != nil {
		return err
	}
	environment := q.Get("environment")
	limit, err := governanceLimit(q)
	if err != nil {
		return err
	}
	if environment == "" {
		return fault.New(fault.Invalid, "INVALID_IDENTITY_INPUT", "必须指定环境")
	}
	scope := "identity_revisions:" + actor.ID + ":" + project + ":" + environment
	position := identityPosition{}
	if raw := q.Get("cursor"); raw != "" {
		decoded, err := a.API.Cursor.Decode(scope, raw)
		if err != nil {
			return governanceCursorError()
		}
		if governanceDecodePosition(decoded, &position) != nil || position.AfterRevision < 0 {
			return governanceCursorError()
		}
	}
	page, err := a.Service.ListRevisions(r.Context(), actor.ID, project, environment,
		identity.ReadFilter{AfterRevision: position.AfterRevision, Limit: limit})
	if err != nil {
		return err
	}
	out := wire.IdentityRevisionList{Items: make([]wire.IdentityRevision, 0, len(page.Items))}
	for _, item := range page.Items {
		out.Items = append(out.Items, identityRevisionWire(item))
	}
	if page.NextRevision > 0 {
		data, err := json.Marshal(identityPosition{AfterRevision: page.NextRevision})
		if err != nil {
			return err
		}
		next := a.API.Cursor.Encode(scope, string(data))
		out.NextCursor = &next
	}
	return a.API.Boundary.JSON(w, http.StatusOK, "IdentityRevisionList", out)
}

func identityRevisionPathValue(r *http.Request) (int64, error) {
	raw := r.PathValue("revision")
	revision, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || revision <= 0 || revision > 9007199254740991 {
		return 0, fault.New(fault.Invalid, "INVALID_IDENTITY_INPUT", "身份修订编号无效")
	}
	return revision, nil
}

func (a *IdentityAPI) get(w http.ResponseWriter, r *http.Request, actor authorizedActor) error {
	project, err := governanceProject(r)
	if err != nil {
		return err
	}
	q, err := governanceQuery(r, "environment")
	if err != nil {
		return err
	}
	revision, err := identityRevisionPathValue(r)
	if err != nil {
		return err
	}
	if q.Get("environment") == "" {
		return fault.New(fault.Invalid, "INVALID_IDENTITY_INPUT", "必须指定环境")
	}
	item, err := a.Service.GetRevision(r.Context(), actor.ID, project, q.Get("environment"), revision)
	if err != nil {
		return err
	}
	return a.API.Boundary.JSON(w, http.StatusOK, "IdentityRevision", identityRevisionWire(item))
}

func (a *IdentityAPI) resolve(w http.ResponseWriter, r *http.Request, actor authorizedActor) error {
	project, err := governanceProject(r)
	if err != nil {
		return err
	}
	q, err := governanceQuery(r, "environment", "uid", "anonymous_id")
	if err != nil {
		return err
	}
	revision, err := identityRevisionPathValue(r)
	if err != nil {
		return err
	}
	if q.Get("environment") == "" {
		return fault.New(fault.Invalid, "INVALID_IDENTITY_INPUT", "必须指定环境")
	}
	uid, anonymous := q.Get("uid"), q.Get("anonymous_id")
	hasUID, hasAnonymous := uid != "", anonymous != ""
	if hasUID == hasAnonymous {
		return fault.New(fault.Invalid, "INVALID_IDENTITY_INPUT", "必须且只能提供 uid 或 anonymous_id 之一")
	}
	kind, value := identity.MemberUID, uid
	if hasAnonymous {
		if !catalogUUID.MatchString(anonymous) {
			return fault.New(fault.Invalid, "INVALID_IDENTITY_INPUT", "匿名标识必须是 UUID")
		}
		kind, value = identity.MemberAnonymousID, anonymous
	}
	resolution, err := a.Service.ResolveMember(r.Context(), actor.ID, project, q.Get("environment"), revision, kind, value)
	if err != nil {
		return err
	}
	out := wire.IdentityResolution{
		Revision:         resolution.Revision,
		ResolvedPersonID: resolution.ResolvedPersonID,
		Members:          make([]wire.IdentityMember, 0, len(resolution.Members)),
	}
	for _, member := range resolution.Members {
		out.Members = append(out.Members, wire.IdentityMember{
			MemberKind:     member.Kind,
			MemberValue:    member.Value,
			FirstSeenAt:    member.FirstSeenAt.UTC().Format(time.RFC3339Nano),
			JoinedRevision: member.JoinedRevision,
		})
	}
	return a.API.Boundary.JSON(w, http.StatusOK, "IdentityResolution", out)
}
