package control

import (
	"crypto/subtle"
	"net/http"
	"strings"

	"github.com/howtomakeaname/Evia/backend/internal/access"
	"github.com/howtomakeaname/Evia/backend/internal/platform/fault"
)

// Session-only routes include account sessions, project creation and machine
// credential administration. New routes never acquire Bearer support implicitly.
func (a *API) auth(mutate bool, next authorizedEndpoint) http.HandlerFunc {
	return a.authenticate(mutate, false, next)
}

func (a *API) projectAuth(mutate bool, next authorizedEndpoint) http.HandlerFunc {
	return a.authenticate(mutate, true, next)
}

// projectPermissionAuth requires an explicit grant at the HTTP boundary. The
// persistence operation still rechecks the current credential and resource scope
// in its own transaction; authentication metadata cannot replace that check.
func (a *API) projectPermissionAuth(mutate bool, permission string, next authorizedEndpoint) http.HandlerFunc {
	return a.projectAuth(mutate, func(w http.ResponseWriter, r *http.Request, actor authorizedActor) error {
		if identity, machine := access.IdentityFromContext(r.Context()); machine {
			if access.KnownMachinePermission(permission) {
				for _, granted := range identity.Permissions {
					if granted == permission {
						return next(w, r, actor)
					}
				}
			}
			return fault.New(fault.Forbidden, "MACHINE_FORBIDDEN", "机器主体无权执行此操作")
		}
		return next(w, r, actor)
	})
}

func (a *API) authenticate(mutate, machineAllowed bool, next authorizedEndpoint) http.HandlerFunc {
	return a.Boundary.Handler(func(w http.ResponseWriter, r *http.Request) error {
		if e := a.origin(r); e != nil {
			return e
		}
		headers := r.Header.Values("Authorization")
		cookies := r.CookiesNamed("evia_session")
		if len(headers) != 0 {
			if !machineAllowed || a.Machines == nil || len(headers) != 1 || len(cookies) != 0 {
				return fault.New(fault.Unauthenticated, "AUTHENTICATION_MODE_INVALID", "认证方式无效或混用了会话与机器凭据")
			}
			parts := strings.Split(headers[0], " ")
			if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") || parts[1] == "" || len(parts[1]) > 160 {
				return fault.New(fault.Unauthenticated, "MACHINE_CREDENTIAL_INVALID", "机器凭据无效或已失效")
			}
			identity, e := a.Machines.AuthenticateMachine(r.Context(), parts[1])
			if e != nil {
				return e
			}
			// The identity is attribution metadata. Each persistence operation
			// locks and rechecks the original credential and its exact permission.
			r = r.WithContext(access.WithIdentity(r.Context(), identity))
			if project := r.PathValue("project_id"); project != "" && project != identity.ProjectID {
				return fault.New(fault.NotFound, "PROJECT_NOT_FOUND", "项目不存在或不可见")
			}
			return next(w, r, authorizedActor{ID: identity.PrincipalID})
		}
		if len(cookies) != 1 {
			return fault.New(fault.Unauthenticated, "SESSION_REQUIRED", "请先登录")
		}
		session, e := a.Access.Authenticate(r.Context(), cookies[0].Value)
		if e != nil {
			return e
		}
		if mutate && (len(r.Header.Values("X-CSRF-Token")) != 1 || subtle.ConstantTimeCompare([]byte(r.Header.Get("X-CSRF-Token")), []byte(session.CSRF)) != 1) {
			return fault.New(fault.Forbidden, "CSRF_REQUIRED", "请求验证已失效，请刷新后重试")
		}
		return next(w, r, authorizedActor{ID: session.UserID, Session: &session})
	})
}

func (a *API) authorizeProject(r *http.Request, actor authorizedActor, project string, write bool) error {
	if identity, ok := access.IdentityFromContext(r.Context()); ok {
		if actor.ID != identity.PrincipalID || project != identity.ProjectID {
			return fault.New(fault.NotFound, "PROJECT_NOT_FOUND", "项目不存在或不可见")
		}
		return nil
	}
	return a.Access.Authorize(r.Context(), actor.ID, project, write)
}
