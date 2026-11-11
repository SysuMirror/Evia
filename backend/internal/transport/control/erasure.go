package control

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/howtomakeaname/Evia/backend/internal/access"
	"github.com/howtomakeaname/Evia/backend/internal/erasure"
	"github.com/howtomakeaname/Evia/backend/internal/gen/wire"
	"github.com/howtomakeaname/Evia/backend/internal/platform/canonical"
	"github.com/howtomakeaname/Evia/backend/internal/platform/fault"
)

// ErasureService is the persistence subset the erasure HTTP surface needs.
type ErasureService interface {
	CreateJob(ctx context.Context, in erasure.Input) (erasure.Job, bool, error)
	GetJob(ctx context.Context, actor, project, jobID string) (erasure.Job, error)
	ListJobs(ctx context.Context, actor, project string, filter erasure.ListFilter) (erasure.Page, error)
}

type erasurePosition struct {
	AfterUUID string `json:"afterUUID"`
}

type erasureRequestBody struct {
	Environment string  `json:"environment"`
	UID         *string `json:"uid,omitempty"`
	AnonymousID *string `json:"anonymous_id,omitempty"`
	Reason      string  `json:"reason"`
}

// ErasureAPI shares management authentication; machines need the explicit
// data.erasure grant and human sessions are rechecked as owners in the store.
type ErasureAPI struct {
	API     *API
	Service ErasureService
}

func (a *ErasureAPI) Register(mux *http.ServeMux) {
	if a.Service == nil {
		return
	}
	const base = "/api/v1/projects/{project_id}/erasure-jobs"
	mux.HandleFunc("GET "+base, a.API.projectPermissionAuth(false, access.PermissionDataErasure, a.list))
	mux.HandleFunc("POST "+base, a.API.projectPermissionAuth(true, access.PermissionDataErasure, a.create))
	mux.HandleFunc("GET "+base+"/{job_id}", a.API.projectPermissionAuth(false, access.PermissionDataErasure, a.get))
}

func erasureJobWire(job erasure.Job) wire.ErasureJob {
	out := wire.ErasureJob{JobID: job.JobID, State: job.State}
	if job.BackupExpiryAt != nil {
		formatted := job.BackupExpiryAt.UTC().Format(time.RFC3339Nano)
		out.BackupExpiryAt = &formatted
	}
	return out
}

func (a *ErasureAPI) create(w http.ResponseWriter, r *http.Request, actor authorizedActor) error {
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
	var body erasureRequestBody
	if err = a.API.Boundary.Decode(w, r, "ErasureInput", &body); err != nil {
		return err
	}
	// The OpenAPI oneOf already pins exactly one identity; repeat the rule at
	// the domain boundary so a bypassed validator cannot create an ambiguous job.
	hasUID := body.UID != nil && *body.UID != ""
	hasAnonymous := body.AnonymousID != nil && *body.AnonymousID != ""
	if hasUID == hasAnonymous {
		return fault.New(fault.Invalid, "INVALID_ERASURE_INPUT", "必须且只能提供 uid 或 anonymous_id 之一")
	}
	hash, err := canonical.Hash(body)
	if err != nil {
		return err
	}
	in := erasure.Input{
		ProjectID:      project,
		Environment:    body.Environment,
		ActorID:        actor.ID,
		Reason:         body.Reason,
		IdempotencyKey: key,
		RequestHash:    hash,
	}
	if hasUID {
		in.SubjectKind, in.SubjectValue = erasure.SubjectUID, *body.UID
	} else {
		in.SubjectKind, in.SubjectValue = erasure.SubjectAnonymousID, *body.AnonymousID
	}
	job, created, err := a.Service.CreateJob(r.Context(), in)
	if err != nil {
		return err
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	return a.API.Boundary.JSON(w, status, "ErasureJob", erasureJobWire(job))
}

func (a *ErasureAPI) list(w http.ResponseWriter, r *http.Request, actor authorizedActor) error {
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
	scope := "erasure_jobs:" + actor.ID + ":" + project
	position := erasurePosition{}
	if raw := q.Get("cursor"); raw != "" {
		decoded, err := a.API.Cursor.Decode(scope, raw)
		if err != nil {
			return governanceCursorError()
		}
		if governanceDecodePosition(decoded, &position) != nil || !catalogUUID.MatchString(position.AfterUUID) {
			return governanceCursorError()
		}
	}
	page, err := a.Service.ListJobs(r.Context(), actor.ID, project, erasure.ListFilter{After: position.AfterUUID, Limit: limit})
	if err != nil {
		return err
	}
	out := wire.ErasureList{Items: make([]wire.ErasureJob, 0, len(page.Items))}
	for _, job := range page.Items {
		out.Items = append(out.Items, erasureJobWire(job))
	}
	if len(page.Items) > limit {
		data, err := json.Marshal(erasurePosition{AfterUUID: page.After})
		if err != nil {
			return err
		}
		next := a.API.Cursor.Encode(scope, string(data))
		out.NextCursor = &next
		out.Items = out.Items[:limit]
	}
	return a.API.Boundary.JSON(w, http.StatusOK, "ErasureList", out)
}

func (a *ErasureAPI) get(w http.ResponseWriter, r *http.Request, actor authorizedActor) error {
	project, err := governanceProject(r)
	if err != nil {
		return err
	}
	if _, err = governanceQuery(r); err != nil {
		return err
	}
	jobID := r.PathValue("job_id")
	if !catalogUUID.MatchString(jobID) {
		return fault.New(fault.Invalid, "INVALID_ERASURE_JOB", "删除作业标识格式不正确")
	}
	job, err := a.Service.GetJob(r.Context(), actor.ID, project, jobID)
	if err != nil {
		return err
	}
	return a.API.Boundary.JSON(w, http.StatusOK, "ErasureJob", erasureJobWire(job))
}
