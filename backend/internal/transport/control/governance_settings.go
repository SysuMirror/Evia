package control

import (
	"net/http"

	"github.com/howtomakeaname/Evia/backend/internal/access"
	"github.com/howtomakeaname/Evia/backend/internal/gen/wire"
)

func (a *API) getSettings(w http.ResponseWriter, r *http.Request, actor authorizedActor) error {
	project, err := governanceProject(r)
	if err != nil {
		return err
	}
	if _, err = governanceQuery(r); err != nil {
		return err
	}
	out, err := a.Settings.Get(r.Context(), actor.ID, project)
	if err != nil {
		return err
	}
	machineETag(w, out.Revision)
	return a.Boundary.JSON(w, 200, "ProjectSettings", settingsWire(out))
}

func (a *API) updateSettings(w http.ResponseWriter, r *http.Request, actor authorizedActor) error {
	project, err := governanceProject(r)
	if err != nil {
		return err
	}
	if _, err = governanceQuery(r); err != nil {
		return err
	}
	revision, key, err := governanceWriteHeaders(r)
	if err != nil {
		return err
	}
	var body wire.ProjectSettingsUpdate
	if err = a.Boundary.Decode(w, r, "ProjectSettingsUpdate", &body); err != nil {
		return err
	}
	out, err := a.Settings.Update(r.Context(), actor.ID, project, revision, key, access.SettingsUpdate{Name: body.Name, Timezone: body.Timezone})
	if err != nil {
		return err
	}
	machineETag(w, out.Revision)
	return a.Boundary.JSON(w, 200, "ProjectSettings", settingsWire(out))
}

func settingsWire(settings access.ProjectSettings) wire.ProjectSettings {
	return wire.ProjectSettings{ProjectID: settings.ProjectID, Name: settings.Name, Timezone: settings.Timezone, Revision: settings.Revision}
}
