package control

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/howtomakeaname/Evia/backend/internal/access"
	"github.com/howtomakeaname/Evia/backend/internal/platform/httpapi"
)

type scopedMachineAuthentication struct {
	MachineService
	permissions []string
}

func (s *scopedMachineAuthentication) AuthenticateMachine(context.Context, string) (access.Identity, error) {
	return access.Identity{PrincipalID: machinePrincipalID, CredentialID: machineCredentialID, ProjectID: projectID, Environment: "development", Permissions: s.permissions}, nil
}

func permissionBoundaryAPI(permissions []string) *API {
	return &API{
		Boundary: &httpapi.Boundary{Logger: slog.New(slog.NewTextHandler(io.Discard, nil))},
		Access:   &fakeAccess{},
		Machines: &scopedMachineAuthentication{permissions: permissions},
		Origin:   "https://evia.example",
	}
}

func TestProjectPermissionAuthRequiresExactMachineGrant(t *testing.T) {
	legacy := []string{
		"project.read", "catalog.drafts.read", "catalog.drafts.write", "catalog.releases.read", "catalog.releases.publish", "catalog.releases.activate",
		"sources.read", "sources.write", "sources.rotate", "queries.read", "queries.run", "queries.raw.read",
		"analyses.read", "analyses.write", "analyses.publish", "analyses.run", "dashboards.read", "dashboards.write", "dashboards.publish", "dashboards.run",
	}
	metrics := []string{access.PermissionMetricsRead, access.PermissionMetricsWrite, access.PermissionMetricsPublish, access.PermissionMetricsRun}
	for _, required := range metrics {
		for _, tc := range []struct {
			name        string
			permissions []string
			status      int
		}{
			{name: "exact grant", permissions: []string{required}, status: 204},
			{name: "all existing grants", permissions: legacy, status: 403},
			{name: "no grant", status: 403},
			{name: "wildcard", permissions: []string{"metrics.*"}, status: 403},
		} {
			t.Run(required+"/"+tc.name, func(t *testing.T) {
				a := permissionBoundaryAPI(tc.permissions)
				calls := 0
				handler := a.projectPermissionAuth(true, required, func(w http.ResponseWriter, r *http.Request, actor authorizedActor) error {
					identity, ok := access.IdentityFromContext(r.Context())
					if !ok || actor.ID != machinePrincipalID || identity.CredentialID != machineCredentialID {
						t.Fatal("permission check lost credential attribution")
					}
					calls++
					w.WriteHeader(204)
					return nil
				})
				r := httptest.NewRequest("POST", "/api/v1/projects/"+projectID+"/metrics", nil)
				r.SetPathValue("project_id", projectID)
				r.Header.Set("Authorization", "Bearer "+machineToken)
				w := httptest.NewRecorder()
				handler(w, r)
				if w.Code != tc.status || (calls == 1) != (tc.status == 204) {
					t.Fatal(w.Code, calls, w.Body.String())
				}
			})
		}
		for _, granted := range metrics {
			if granted == required {
				continue
			}
			a := permissionBoundaryAPI([]string{granted})
			r := httptest.NewRequest("GET", "/", nil)
			r.Header.Set("Authorization", "Bearer "+machineToken)
			w := httptest.NewRecorder()
			a.projectPermissionAuth(false, required, func(http.ResponseWriter, *http.Request, authorizedActor) error {
				t.Fatal("different metric permission granted authority")
				return nil
			})(w, r)
			if w.Code != 403 {
				t.Fatal(w.Code, w.Body.String())
			}
		}
	}
}

func TestProjectPermissionAuthDefaultsDenyUnknownMachinePermission(t *testing.T) {
	for _, required := range []string{"", "metrics.*", "metrics.future"} {
		a := permissionBoundaryAPI([]string{required})
		r := httptest.NewRequest("POST", "/", nil)
		r.Header.Set("Authorization", "Bearer "+machineToken)
		w := httptest.NewRecorder()
		a.projectPermissionAuth(true, required, func(http.ResponseWriter, *http.Request, authorizedActor) error {
			t.Fatal("unknown permission reached endpoint")
			return nil
		})(w, r)
		if w.Code != 403 || !strings.Contains(w.Body.String(), "MACHINE_FORBIDDEN") {
			t.Fatal(w.Code, w.Body.String())
		}
	}
}

func TestProjectPermissionAuthPreservesSessionCSRF(t *testing.T) {
	for _, tc := range []struct {
		mutate bool
		csrf   string
		status int
	}{
		{status: 204},
		{mutate: true, status: 403},
		{mutate: true, csrf: "csrf", status: 204},
	} {
		a := permissionBoundaryAPI(nil)
		r := httptest.NewRequest("POST", "/", nil)
		r.AddCookie(&http.Cookie{Name: "evia_session", Value: "valid"})
		r.Header.Set("X-CSRF-Token", tc.csrf)
		w := httptest.NewRecorder()
		a.projectPermissionAuth(tc.mutate, access.PermissionMetricsWrite, func(w http.ResponseWriter, _ *http.Request, actor authorizedActor) error {
			if actor.ID != userID || actor.Session == nil {
				t.Fatal("session identity changed")
			}
			w.WriteHeader(204)
			return nil
		})(w, r)
		if w.Code != tc.status {
			t.Fatal(w.Code, w.Body.String())
		}
	}
}
