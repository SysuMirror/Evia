package control

import (
	"context"
	"net/http"
	"time"

	"github.com/howtomakeaname/Evia/backend/internal/experiments"
	"github.com/howtomakeaname/Evia/backend/internal/gen/wire"
)

type ExperimentService interface {
	List(ctx context.Context, actor, project, environment string) ([]experiments.Experiment, error)
	Get(ctx context.Context, actor, project, id string) (experiments.Experiment, error)
	Create(ctx context.Context, actor, project string, in experiments.CreateInput) (experiments.Experiment, error)
	Update(ctx context.Context, actor, project, id string, in experiments.UpdateInput) (experiments.Experiment, error)
	ChangeStatus(ctx context.Context, actor, project, id, status string) (experiments.Experiment, error)
	Delete(ctx context.Context, actor, project, id string) error
}

func (a *API) registerExperiments(mux *http.ServeMux) {
	if a.Experiments == nil {
		return
	}
	const base = "/api/v1/projects/{project_id}/experiments"
	mux.HandleFunc("GET "+base, a.projectAuth(false, a.listExperiments))
	mux.HandleFunc("POST "+base, a.projectAuth(true, a.createExperiment))
	mux.HandleFunc("GET "+base+"/{experiment_id}", a.projectAuth(false, a.getExperiment))
	mux.HandleFunc("PATCH "+base+"/{experiment_id}", a.projectAuth(true, a.updateExperiment))
	mux.HandleFunc("POST "+base+"/{experiment_id}/status", a.projectAuth(true, a.changeExperimentStatus))
	mux.HandleFunc("DELETE "+base+"/{experiment_id}", a.projectAuth(true, a.deleteExperiment))
}

func (a *API) experimentProject(r *http.Request, s authorizedActor, write bool) (string, error) {
	p := r.PathValue("project_id")
	id := r.PathValue("experiment_id")
	if !catalogUUID.MatchString(p) || (id != "" && !catalogUUID.MatchString(id)) {
		return "", experiments.Invalid("INVALID_RESOURCE_ID", "资源标识格式无效")
	}
	if err := a.authorizeProject(r, s, p, write); err != nil {
		return "", err
	}
	return p, nil
}

func experimentWire(item experiments.Experiment) map[string]any {
	variants := make([]map[string]any, 0, len(item.Variants))
	for _, v := range item.Variants {
		variants = append(variants, map[string]any{
			"key":        v.Key,
			"name":       v.Name,
			"weight_bps": v.WeightBps,
		})
	}
	overrides := item.Overrides
	if overrides == nil {
		overrides = map[string]string{}
	}
	var startedAt any
	if item.StartedAt != nil {
		startedAt = item.StartedAt.UTC().Format(time.RFC3339)
	}
	var endedAt any
	if item.EndedAt != nil {
		endedAt = item.EndedAt.UTC().Format(time.RFC3339)
	}
	return map[string]any{
		"experiment_id":      item.ID,
		"project_id":         item.ProjectID,
		"environment":        item.Environment,
		"experiment_key":     item.ExperimentKey,
		"name":               item.Name,
		"description":        item.Description,
		"status":             item.Status,
		"salt":               item.Salt,
		"control_variant":    item.ControlVariant,
		"exposure_event_key": item.ExposureEventKey,
		"variants":           variants,
		"overrides":          overrides,
		"version":            item.Version,
		"started_at":         startedAt,
		"ended_at":           endedAt,
		"created_by":         item.CreatedBy,
		"created_at":         item.CreatedAt.UTC().Format(time.RFC3339),
		"updated_at":         item.UpdatedAt.UTC().Format(time.RFC3339),
	}
}

func toDomainVariants(in []wire.ExperimentVariant) []experiments.Variant {
	out := make([]experiments.Variant, 0, len(in))
	for _, v := range in {
		out = append(out, experiments.Variant{Key: v.Key, Name: v.Name, WeightBps: int(v.WeightBps)})
	}
	return out
}

func toDomainOverrides(in map[string]any) map[string]string {
	out := map[string]string{}
	for k, v := range in {
		if str, ok := v.(string); ok {
			out[k] = str
		}
	}
	return out
}

func (a *API) listExperiments(w http.ResponseWriter, r *http.Request, s authorizedActor) error {
	p, err := a.experimentProject(r, s, false)
	if err != nil {
		return err
	}
	env := r.URL.Query().Get("environment")
	rows, err := a.Experiments.List(r.Context(), s.ID, p, env)
	if err != nil {
		return err
	}
	items := make([]map[string]any, 0, len(rows))
	for _, row := range rows {
		items = append(items, experimentWire(row))
	}
	return a.Boundary.JSON(w, 200, "ExperimentList", map[string]any{"items": items})
}

func (a *API) getExperiment(w http.ResponseWriter, r *http.Request, s authorizedActor) error {
	p, err := a.experimentProject(r, s, false)
	if err != nil {
		return err
	}
	out, err := a.Experiments.Get(r.Context(), s.ID, p, r.PathValue("experiment_id"))
	if err != nil {
		return err
	}
	return a.Boundary.JSON(w, 200, "Experiment", experimentWire(out))
}

func (a *API) createExperiment(w http.ResponseWriter, r *http.Request, s authorizedActor) error {
	p, err := a.experimentProject(r, s, true)
	if err != nil {
		return err
	}
	var in wire.ExperimentCreate
	if err = a.Boundary.Decode(w, r, "ExperimentCreate", &in); err != nil {
		return err
	}
	desc, salt, expKey := "", "", ""
	if in.Description != nil {
		desc = *in.Description
	}
	if in.Salt != nil {
		salt = *in.Salt
	}
	if in.ExposureEventKey != nil {
		expKey = *in.ExposureEventKey
	}
	out, err := a.Experiments.Create(r.Context(), s.ID, p, experiments.CreateInput{
		Environment:      in.Environment,
		ExperimentKey:    in.ExperimentKey,
		Name:             in.Name,
		Description:      desc,
		Salt:             salt,
		ControlVariant:   in.ControlVariant,
		ExposureEventKey: expKey,
		Variants:         toDomainVariants(in.Variants),
		Overrides:        toDomainOverrides(in.Overrides),
	})
	if err != nil {
		return err
	}
	return a.Boundary.JSON(w, 201, "Experiment", experimentWire(out))
}

func (a *API) updateExperiment(w http.ResponseWriter, r *http.Request, s authorizedActor) error {
	p, err := a.experimentProject(r, s, true)
	if err != nil {
		return err
	}
	var in wire.ExperimentUpdate
	if err = a.Boundary.Decode(w, r, "ExperimentUpdate", &in); err != nil {
		return err
	}
	desc, salt, expKey := "", "", ""
	if in.Description != nil {
		desc = *in.Description
	}
	if in.Salt != nil {
		salt = *in.Salt
	}
	if in.ExposureEventKey != nil {
		expKey = *in.ExposureEventKey
	}
	out, err := a.Experiments.Update(r.Context(), s.ID, p, r.PathValue("experiment_id"), experiments.UpdateInput{
		Name:             in.Name,
		Description:      desc,
		Salt:             salt,
		ControlVariant:   in.ControlVariant,
		ExposureEventKey: expKey,
		Variants:         toDomainVariants(in.Variants),
		Overrides:        toDomainOverrides(in.Overrides),
	})
	if err != nil {
		return err
	}
	return a.Boundary.JSON(w, 200, "Experiment", experimentWire(out))
}

func (a *API) changeExperimentStatus(w http.ResponseWriter, r *http.Request, s authorizedActor) error {
	p, err := a.experimentProject(r, s, true)
	if err != nil {
		return err
	}
	var in wire.ExperimentStatusChange
	if err = a.Boundary.Decode(w, r, "ExperimentStatusChange", &in); err != nil {
		return err
	}
	out, err := a.Experiments.ChangeStatus(r.Context(), s.ID, p, r.PathValue("experiment_id"), in.Status)
	if err != nil {
		return err
	}
	return a.Boundary.JSON(w, 200, "Experiment", experimentWire(out))
}

func (a *API) deleteExperiment(w http.ResponseWriter, r *http.Request, s authorizedActor) error {
	p, err := a.experimentProject(r, s, true)
	if err != nil {
		return err
	}
	if err = a.Experiments.Delete(r.Context(), s.ID, p, r.PathValue("experiment_id")); err != nil {
		return err
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}
