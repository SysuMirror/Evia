package control

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"net"
	"net/http"
	"time"

	"github.com/howtomakeaname/Evia/backend/internal/access"
	"github.com/howtomakeaname/Evia/backend/internal/gen/wire"
	"github.com/howtomakeaname/Evia/backend/internal/platform/fault"
)

func remoteHost(addr string) string {
	ip, _, err := net.SplitHostPort(addr)
	if err != nil {
		return addr
	}
	return ip
}

type AccountService interface {
	Profile(context.Context, string) (access.AccountProfile, error)
	UpdateProfile(context.Context, string, string) (access.AccountProfile, error)
	ChangePassword(context.Context, access.Session, string, string) (access.Session, string, error)
}

type InvitationService interface {
	List(context.Context, string, string, *access.InvitationPosition, int) ([]access.ProjectInvitation, error)
	Create(context.Context, string, string, access.InvitationCreateInput) (access.InvitationIssued, error)
	Revoke(context.Context, string, string, string) (access.ProjectInvitation, error)
	Preview(context.Context, string, string) (access.InvitationPreview, error)
	Accept(context.Context, access.InvitationAcceptInput) (access.InvitationAcceptResult, error)
}

func (a *API) registerAccountAndInvitations(mux *http.ServeMux) {
	if a.Accounts != nil {
		mux.HandleFunc("GET /api/v1/account", a.auth(false, a.getAccountProfile))
		mux.HandleFunc("PATCH /api/v1/account/profile", a.auth(true, a.updateAccountProfile))
		mux.HandleFunc("POST /api/v1/account/password", a.auth(true, a.changeAccountPassword))
	}
	if a.Invitations != nil {
		const base = "/api/v1/projects/{project_id}"
		mux.HandleFunc("GET "+base+"/invitations", a.auth(false, a.listProjectInvitations))
		mux.HandleFunc("POST "+base+"/invitations", a.auth(true, a.createProjectInvitation))
		mux.HandleFunc("DELETE "+base+"/invitations/{invitation_id}", a.auth(true, a.revokeProjectInvitation))
		mux.HandleFunc("POST /api/v1/invitations/preview", a.Boundary.Handler(a.previewInvitation))
		mux.HandleFunc("POST /api/v1/invitations/accept", a.Boundary.Handler(a.acceptInvitation))
	}
}

func accountProfileWire(p access.AccountProfile) wire.AccountProfile {
	return wire.AccountProfile{
		UserID:      p.UserID,
		Username:    p.Username,
		DisplayName: p.DisplayName,
		CreatedAt:   governanceTime(p.CreatedAt),
	}
}

func optionalTimeWire(t *time.Time) *string {
	if t == nil {
		return nil
	}
	formatted := governanceTime(*t)
	return &formatted
}

func projectInvitationWire(inv access.ProjectInvitation) wire.ProjectInvitation {
	return wire.ProjectInvitation{
		ID:                   inv.ID,
		ProjectID:            inv.ProjectID,
		Role:                 inv.Role,
		Note:                 inv.Note,
		Status:               inv.Status,
		CreatedBy:            inv.CreatedBy,
		CreatedByDisplayName: inv.CreatedByDisplayName,
		CreatedAt:            governanceTime(inv.CreatedAt),
		ExpiresAt:            governanceTime(inv.ExpiresAt),
		AcceptedBy:           inv.AcceptedBy,
		AcceptedUsername:     inv.AcceptedUsername,
		AcceptedAt:           optionalTimeWire(inv.AcceptedAt),
		RevokedAt:            optionalTimeWire(inv.RevokedAt),
	}
}

func (a *API) getAccountProfile(w http.ResponseWriter, r *http.Request, actor authorizedActor) error {
	if len(r.URL.RawQuery) != 0 {
		return fault.New(fault.Invalid, "INVALID_QUERY", "查询参数不符合接口定义")
	}
	profile, err := a.Accounts.Profile(r.Context(), actor.ID)
	if err != nil {
		return err
	}
	return a.Boundary.JSON(w, http.StatusOK, "AccountProfile", accountProfileWire(profile))
}

func (a *API) updateAccountProfile(w http.ResponseWriter, r *http.Request, actor authorizedActor) error {
	var in wire.AccountProfileUpdate
	if err := a.Boundary.Decode(w, r, "AccountProfileUpdate", &in); err != nil {
		return err
	}
	profile, err := a.Accounts.UpdateProfile(r.Context(), actor.ID, in.DisplayName)
	if err != nil {
		return err
	}
	return a.Boundary.JSON(w, http.StatusOK, "AccountProfile", accountProfileWire(profile))
}

func (a *API) changeAccountPassword(w http.ResponseWriter, r *http.Request, actor authorizedActor) error {
	var in wire.AccountPasswordChange
	if err := a.Boundary.Decode(w, r, "AccountPasswordChange", &in); err != nil {
		return err
	}
	rotated, secret, err := a.Accounts.ChangePassword(r.Context(), *actor.Session, in.CurrentPassword, in.NewPassword)
	if err != nil {
		return err
	}
	a.cookie(w, secret, 8*3600)
	return a.Boundary.JSON(w, http.StatusOK, "Session", sessionWire(rotated))
}

type invitationCursorPayload struct {
	CreatedAt string `json:"t"`
	ID        string `json:"i"`
}

func (a *API) listProjectInvitations(w http.ResponseWriter, r *http.Request, actor authorizedActor) error {
	project, err := governanceProject(r)
	if err != nil {
		return err
	}
	q, err := governanceQuery(r, "limit", "cursor")
	if err != nil {
		return err
	}
	limit, err := governanceLimit(q)
	if err != nil {
		return err
	}
	scope := "project_invitations:" + actor.ID + ":" + project
	var after *access.InvitationPosition
	if raw := q.Get("cursor"); raw != "" {
		if a.Cursor == nil {
			return governanceCursorError()
		}
		decoded, err := a.Cursor.Decode(scope, raw)
		if err != nil {
			return governanceCursorError()
		}
		var payload invitationCursorPayload
		if err = governanceDecodePosition(decoded, &payload); err != nil {
			return err
		}
		ts, err := time.Parse(time.RFC3339Nano, payload.CreatedAt)
		if err != nil || !catalogUUID.MatchString(payload.ID) {
			return governanceCursorError()
		}
		after = &access.InvitationPosition{CreatedAt: ts, ID: payload.ID}
	}
	rows, err := a.Invitations.List(r.Context(), actor.ID, project, after, limit)
	if err != nil {
		return err
	}
	out := wire.ProjectInvitationList{Items: make([]wire.ProjectInvitation, 0, min(len(rows), limit)), NextCursor: nil}
	hasMore := len(rows) > limit
	if hasMore {
		rows = rows[:limit]
	}
	for _, inv := range rows {
		out.Items = append(out.Items, projectInvitationWire(inv))
	}
	if hasMore && len(rows) > 0 && a.Cursor != nil {
		last := rows[len(rows)-1]
		data, err := json.Marshal(invitationCursorPayload{CreatedAt: governanceTime(last.CreatedAt), ID: last.ID})
		if err != nil {
			return err
		}
		token := a.Cursor.Encode(scope, string(data))
		out.NextCursor = &token
	}
	return a.Boundary.JSON(w, http.StatusOK, "ProjectInvitationList", out)
}

func (a *API) createProjectInvitation(w http.ResponseWriter, r *http.Request, actor authorizedActor) error {
	project, err := governanceProject(r)
	if err != nil {
		return err
	}
	var in wire.ProjectInvitationCreate
	if err = a.Boundary.Decode(w, r, "ProjectInvitationCreate", &in); err != nil {
		return err
	}
	note := ""
	if in.Note != nil {
		note = *in.Note
	}
	hours := 0
	if in.ExpiresInHours != nil {
		hours = int(*in.ExpiresInHours)
	}
	issued, err := a.Invitations.Create(r.Context(), actor.ID, project, access.InvitationCreateInput{
		Role:           in.Role,
		Note:           note,
		ExpiresInHours: hours,
	})
	if err != nil {
		return err
	}
	return a.Boundary.JSON(w, http.StatusCreated, "ProjectInvitationIssued", wire.ProjectInvitationIssued{
		Invitation:  projectInvitationWire(issued.Invitation),
		InviteToken: issued.InviteToken,
	})
}

func (a *API) revokeProjectInvitation(w http.ResponseWriter, r *http.Request, actor authorizedActor) error {
	project, err := governanceProject(r)
	if err != nil {
		return err
	}
	if err = governanceNoBody(r); err != nil {
		return err
	}
	invitationID := r.PathValue("invitation_id")
	if !catalogUUID.MatchString(invitationID) {
		return fault.New(fault.Invalid, "INVALID_INVITATION_ID", "邀请标识格式不正确")
	}
	inv, err := a.Invitations.Revoke(r.Context(), actor.ID, project, invitationID)
	if err != nil {
		return err
	}
	return a.Boundary.JSON(w, http.StatusOK, "ProjectInvitation", projectInvitationWire(inv))
}

func (a *API) previewInvitation(w http.ResponseWriter, r *http.Request) error {
	if len(r.Header.Values("Authorization")) != 0 {
		return fault.New(fault.Unauthenticated, "AUTHENTICATION_MODE_INVALID", "邀请预览接口不接受 Bearer 凭据")
	}
	if err := a.origin(r); err != nil {
		return err
	}
	var in wire.InvitationPreviewRequest
	if err := a.Boundary.Decode(w, r, "InvitationPreviewRequest", &in); err != nil {
		return err
	}
	preview, err := a.Invitations.Preview(r.Context(), in.Token, remoteHost(r.RemoteAddr))
	if err != nil {
		return err
	}
	return a.Boundary.JSON(w, http.StatusOK, "InvitationPreview", wire.InvitationPreview{
		ProjectID:   preview.ProjectID,
		ProjectName: preview.ProjectName,
		Role:        preview.Role,
		Note:        preview.Note,
		ExpiresAt:   governanceTime(preview.ExpiresAt),
	})
}

func (a *API) acceptInvitation(w http.ResponseWriter, r *http.Request) error {
	if len(r.Header.Values("Authorization")) != 0 {
		return fault.New(fault.Unauthenticated, "AUTHENTICATION_MODE_INVALID", "接受邀请接口不接受 Bearer 凭据")
	}
	if err := a.origin(r); err != nil {
		return err
	}
	var in wire.InvitationAcceptRequest
	if err := a.Boundary.Decode(w, r, "InvitationAcceptRequest", &in); err != nil {
		return err
	}
	var existingSession *access.Session
	cookies := r.CookiesNamed("evia_session")
	if len(cookies) > 1 {
		return fault.New(fault.Unauthenticated, "AUTHENTICATION_MODE_INVALID", "认证方式无效")
	}
	if len(cookies) == 1 && a.Access != nil {
		session, err := a.Access.Authenticate(r.Context(), cookies[0].Value)
		if err == nil {
			if len(r.Header.Values("X-CSRF-Token")) != 1 || subtle.ConstantTimeCompare([]byte(r.Header.Get("X-CSRF-Token")), []byte(session.CSRF)) != 1 {
				return fault.New(fault.Forbidden, "CSRF_REQUIRED", "请求验证已失效，请刷新后重试")
			}
			existingSession = &session
		}
	}
	username, displayName, password := "", "", ""
	if in.Username != nil {
		username = *in.Username
	}
	if in.DisplayName != nil {
		displayName = *in.DisplayName
	}
	if in.Password != nil {
		password = *in.Password
	}
	res, err := a.Invitations.Accept(r.Context(), access.InvitationAcceptInput{
		Token:       in.Token,
		Username:    username,
		DisplayName: displayName,
		Password:    password,
		RemoteIP:    remoteHost(r.RemoteAddr),
		Session:     existingSession,
	})
	if err != nil {
		return err
	}
	if res.SessionSecret != "" {
		a.cookie(w, res.SessionSecret, 8*3600)
	}
	return a.Boundary.JSON(w, http.StatusOK, "InvitationAcceptResponse", wire.InvitationAcceptResponse{
		Session:     sessionWire(res.Session),
		ProjectID:   res.ProjectID,
		ProjectName: res.ProjectName,
		Role:        res.Role,
	})
}
