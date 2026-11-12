package control

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/howtomakeaname/Evia/backend/internal/backfilling"
	"github.com/howtomakeaname/Evia/backend/internal/gen/wire"
	"github.com/howtomakeaname/Evia/backend/internal/platform/canonical"
	"github.com/howtomakeaname/Evia/backend/internal/platform/fault"
)

// BackfillService is the backfill lifecycle subset the HTTP surface needs.
type BackfillService interface {
	CreateJob(ctx context.Context, in backfilling.Input) (backfilling.Job, bool, error)
	GetJob(ctx context.Context, actor, project, jobID string) (backfilling.Job, error)
	ListJobs(ctx context.Context, actor, project string, filter backfilling.ListFilter) (backfilling.Page, error)
	CancelJob(ctx context.Context, actor, project, jobID string) (backfilling.Job, error)
}

// BackfillAPI is owner-only human governance: machines cannot trigger recovery
// writes, and the PostgreSQL store re-checks ownership in every transaction.
type BackfillAPI struct {
	API     *API
	Service BackfillService
}

type backfillPosition struct {
	AfterUUID string `json:"afterUUID"`
}

func (a *BackfillAPI) Register(mux *http.ServeMux) {
	if a.Service == nil {
		return
	}
	const base = "/api/v1/projects/{project_id}/backfills"
	mux.HandleFunc("GET "+base, a.API.projectAuth(false, a.list))
	mux.HandleFunc("POST "+base, a.API.projectAuth(true, a.create))
	mux.HandleFunc("GET "+base+"/{job_id}", a.API.projectAuth(false, a.get))
	mux.HandleFunc("DELETE "+base+"/{job_id}", a.API.projectAuth(true, a.cancel))
}

func backfillWire(job backfilling.Job) wire.BackfillJob {
	out := wire.BackfillJob{
		JobID:        job.JobID,
		ProjectID:    job.ProjectID,
		Environment:  job.Environment,
		ManifestID:   job.ManifestID,
		Name:         job.Name,
		State:        job.State,
		RowsExpected: job.RowsExpected,
		RowsInserted: job.RowsInserted,
		ErrorCode:    job.ErrorCode,
		CreatedAt:    job.CreatedAt.UTC().Format(time.RFC3339Nano),
		UpdatedAt:    job.UpdatedAt.UTC().Format(time.RFC3339Nano),
		FinishedAt:   nil,
	}
	if job.FinishedAt != nil {
		finished := job.FinishedAt.UTC().Format(time.RFC3339Nano)
		out.FinishedAt = &finished
	}
	return out
}

func (a *BackfillAPI) create(w http.ResponseWriter, r *http.Request, actor authorizedActor) error {
	project, err := governanceProject(r)
	if err != nil {
		return err
	}
	if _, err = governanceQuery(r); err != nil {
		return err
	}
	key, err := savedIdempotency(r)
	if err != nil {
		return err
	}
	var body wire.BackfillInput
	if err = a.API.Boundary.Decode(w, r, "BackfillInput", &body); err != nil {
		return err
	}
	hash, err := canonical.Hash(body)
	if err != nil {
		return err
	}
	job, created, err := a.Service.CreateJob(r.Context(), backfilling.Input{
		ProjectID:      project,
		Environment:    body.Environment,
		ActorID:        actor.ID,
		ManifestID:     body.ManifestID,
		Name:           body.Name,
		IdempotencyKey: key,
		RequestHash:    hash,
	})
	if err != nil {
		return err
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	return a.API.Boundary.JSON(w, status, "BackfillJob", backfillWire(job))
}

func (a *BackfillAPI) list(w http.ResponseWriter, r *http.Request, actor authorizedActor) error {
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
	filter := backfilling.ListFilter{Limit: limit}
	if raw := q.Get("cursor"); raw != "" {
		scope := "backfill_jobs:" + actor.ID + ":" + project
		decoded, err := a.API.Cursor.Decode(scope, raw)
		if err != nil {
			return governanceCursorError()
		}
		position := backfillPosition{}
		if err = json.Unmarshal([]byte(decoded), &position); err != nil || !catalogUUID.MatchString(position.AfterUUID) {
			return governanceCursorError()
		}
		filter.After = position.AfterUUID
	}
	page, err := a.Service.ListJobs(r.Context(), actor.ID, project, filter)
	if err != nil {
		return err
	}
	out := wire.BackfillList{Items: make([]wire.BackfillJob, 0, len(page.Items)), NextCursor: nil}
	for _, job := range page.Items {
		out.Items = append(out.Items, backfillWire(job))
	}
	if len(page.Items) > limit {
		data, err := json.Marshal(backfillPosition{AfterUUID: page.After})
		if err != nil {
			return err
		}
		next := a.API.Cursor.Encode("backfill_jobs:"+actor.ID+":"+project, string(data))
		out.NextCursor = &next
		out.Items = out.Items[:limit]
	}
	return a.API.Boundary.JSON(w, http.StatusOK, "BackfillList", out)
}

func (a *BackfillAPI) get(w http.ResponseWriter, r *http.Request, actor authorizedActor) error {
	project, err := governanceProject(r)
	if err != nil {
		return err
	}
	jobID := r.PathValue("job_id")
	if !catalogUUID.MatchString(jobID) {
		return fault.New(fault.Invalid, "INVALID_BACKFILL_INPUT", "回补作业标识格式不正确")
	}
	job, err := a.Service.GetJob(r.Context(), actor.ID, project, jobID)
	if err != nil {
		return err
	}
	return a.API.Boundary.JSON(w, http.StatusOK, "BackfillJob", backfillWire(job))
}

func (a *BackfillAPI) cancel(w http.ResponseWriter, r *http.Request, actor authorizedActor) error {
	project, err := governanceProject(r)
	if err != nil {
		return err
	}
	if _, err = governanceQuery(r); err != nil {
		return err
	}
	jobID := r.PathValue("job_id")
	if !catalogUUID.MatchString(jobID) {
		return fault.New(fault.Invalid, "INVALID_BACKFILL_INPUT", "回补作业标识格式不正确")
	}
	job, err := a.Service.CancelJob(r.Context(), actor.ID, project, jobID)
	if err != nil {
		return err
	}
	return a.API.Boundary.JSON(w, http.StatusOK, "BackfillJob", backfillWire(job))
}
