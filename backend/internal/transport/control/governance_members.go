package control

import (
	"encoding/json"
	"net/http"

	"github.com/howtomakeaname/Evia/backend/internal/access"
	"github.com/howtomakeaname/Evia/backend/internal/gen/wire"
	"github.com/howtomakeaname/Evia/backend/internal/platform/fault"
)

type memberPosition struct {
	AfterUUID string `json:"afterUUID"`
	Revision  int64  `json:"revision"`
}

func (a *API) listMembers(w http.ResponseWriter, r *http.Request, actor authorizedActor) error {
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
	scope := "project_members:" + actor.ID + ":" + project
	raw, err := a.Cursor.Decode(scope, q.Get("cursor"))
	if err != nil {
		return governanceCursorError()
	}
	position := memberPosition{}
	if raw != "" {
		if governanceDecodePosition(raw, &position) != nil || !catalogUUID.MatchString(position.AfterUUID) || position.Revision < 1 || position.Revision > access.MaxMembershipRevision {
			return governanceCursorError()
		}
	}
	page, err := a.Memberships.List(r.Context(), actor.ID, project, access.MemberListFilter{After: position.AfterUUID, Revision: position.Revision, Limit: limit})
	if err != nil {
		return err
	}
	out := wire.ProjectMemberList{Items: []wire.ProjectMember{}, Revision: page.Revision}
	if len(page.Items) > limit {
		data, err := json.Marshal(memberPosition{AfterUUID: page.Items[limit-1].UserID, Revision: page.Revision})
		if err != nil {
			return err
		}
		next := a.Cursor.Encode(scope, string(data))
		out.NextCursor = &next
		page.Items = page.Items[:limit]
	}
	for _, member := range page.Items {
		out.Items = append(out.Items, memberWire(member))
	}
	machineETag(w, page.Revision)
	return a.Boundary.JSON(w, 200, "ProjectMemberList", out)
}

func (a *API) addMember(w http.ResponseWriter, r *http.Request, actor authorizedActor) error {
	return a.mutateMember(w, r, actor, "add")
}

func (a *API) updateMember(w http.ResponseWriter, r *http.Request, actor authorizedActor) error {
	return a.mutateMember(w, r, actor, "update")
}

func (a *API) removeMember(w http.ResponseWriter, r *http.Request, actor authorizedActor) error {
	return a.mutateMember(w, r, actor, "remove")
}

func (a *API) mutateMember(w http.ResponseWriter, r *http.Request, actor authorizedActor, operation string) error {
	project, err := governanceProject(r)
	if err != nil {
		return err
	}
	if _, err = governanceQuery(r); err != nil {
		return err
	}
	user := r.PathValue("user_id")
	if operation != "add" && !catalogUUID.MatchString(user) {
		return fault.New(fault.Invalid, "INVALID_MEMBER_ID", "成员标识格式不正确")
	}
	revision, key, err := governanceWriteHeaders(r)
	if err != nil {
		return err
	}
	in := access.MemberInput{Revision: revision, IdempotencyKey: key}
	var result access.MemberResult
	status := 200
	switch operation {
	case "add":
		var body wire.ProjectMemberAdd
		if err = a.Boundary.Decode(w, r, "ProjectMemberAdd", &body); err != nil {
			return err
		}
		in.Username, in.Role = body.Username, body.Role
		result, err = a.Memberships.Add(r.Context(), actor.ID, project, in)
		status = 201
	case "update":
		var body wire.ProjectMemberUpdate
		if err = a.Boundary.Decode(w, r, "ProjectMemberUpdate", &body); err != nil {
			return err
		}
		in.Role = body.Role
		result, err = a.Memberships.Update(r.Context(), actor.ID, project, user, in)
	case "remove":
		if err = governanceNoBody(r); err != nil {
			return err
		}
		result, err = a.Memberships.Remove(r.Context(), actor.ID, project, user, in)
	}
	if err != nil {
		return err
	}
	machineETag(w, result.Revision)
	return a.Boundary.JSON(w, status, "ProjectMemberResult", wire.ProjectMemberResult{Member: memberWire(result.Member), Revision: result.Revision})
}

func memberWire(member access.Member) wire.ProjectMember {
	return wire.ProjectMember{UserID: member.UserID, Username: member.Username, DisplayName: member.DisplayName, Role: member.Role, Disabled: member.Disabled}
}
