package control

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/howtomakeaname/Evia/backend/internal/access"
	"github.com/howtomakeaname/Evia/backend/internal/analysis"
	"github.com/howtomakeaname/Evia/backend/internal/exporting"
	"github.com/howtomakeaname/Evia/backend/internal/gen/wire"
	"github.com/howtomakeaname/Evia/backend/internal/platform/canonical"
	"github.com/howtomakeaname/Evia/backend/internal/platform/fault"
)

// ExportService is the export lifecycle subset the HTTP surface needs.
type ExportService interface {
	CreateJob(ctx context.Context, in exporting.Input) (exporting.Job, bool, error)
	CreateSavedAnalysisExportJob(ctx context.Context, actor, project, analysisID string, version int64, in exporting.SavedAnalysisExportInput) (exporting.Job, bool, error)
	GetExportJob(ctx context.Context, actor, project, jobID string) (exporting.Job, error)
	ListExportJobs(ctx context.Context, actor, project string, filter exporting.ListFilter) (exporting.Page, error)
	CancelExportJob(ctx context.Context, actor, project, jobID string) (exporting.Job, error)
	MintGrant(ctx context.Context, actor, project, jobID string) (string, exporting.Grant, error)
	Redeem(ctx context.Context, token string) (exporting.Job, exporting.Grant, error)
}

// ExportAPI shares management authentication; machines need the explicit
// data.export grant and human sessions are rechecked as owners in the store.
type ExportAPI struct {
	API       *API
	Service   ExportService
	Validator analysis.Validator
	Downloads exporting.ArtifactStore
}

type exportPosition struct {
	AfterUUID string `json:"afterUUID"`
}

type exportRequestBody struct {
	Name             string          `json:"name"`
	Environment      string          `json:"environment"`
	Format           string          `json:"format,omitempty"`
	Query            json.RawMessage `json:"query"`
	ArtifactExpiryAt *string         `json:"artifact_expiry_at,omitempty"`
}

// savedAnalysisExportRequestBody binds caller-owned parameters; the bounded
// query is rebuilt server-side from the immutable version snapshot.
type savedAnalysisExportRequestBody struct {
	Name             string             `json:"name"`
	Format           string             `json:"format,omitempty"`
	TimeRange        analysis.TimeRange `json:"time_range"`
	Timezone         string             `json:"timezone"`
	ArtifactExpiryAt *string            `json:"artifact_expiry_at,omitempty"`
}

func (a *ExportAPI) Register(mux *http.ServeMux) {
	if a.Service == nil {
		return
	}
	const base = "/api/v1/projects/{project_id}/exports"
	mux.HandleFunc("GET "+base, a.API.projectPermissionAuth(false, access.PermissionDataExport, a.list))
	mux.HandleFunc("POST "+base, a.API.projectPermissionAuth(true, access.PermissionDataExport, a.create))
	mux.HandleFunc("POST /api/v1/projects/{project_id}/saved-analyses/{analysis_id}/versions/{version}/exports",
		a.API.projectPermissionAuth(true, access.PermissionDataExport, a.createFromSavedAnalysis))
	mux.HandleFunc("GET "+base+"/{job_id}", a.API.projectPermissionAuth(false, access.PermissionDataExport, a.get))
	mux.HandleFunc("DELETE "+base+"/{job_id}", a.API.projectPermissionAuth(true, access.PermissionDataExport, a.cancel))
	mux.HandleFunc("POST "+base+"/{job_id}/download-grants", a.API.projectPermissionAuth(true, access.PermissionDataExport, a.grant))
	if a.Downloads != nil {
		// Capability URL: the one-time grant token authenticates the download,
		// so this route deliberately has no session/machine wrapper.
		mux.HandleFunc("GET /api/v1/exports/downloads/{token}", a.API.Boundary.Handler(a.download))
	}
}

func exportJobWire(job exporting.Job) wire.ExportJob {
	out := wire.ExportJob{
		JobID:       job.JobID,
		ProjectID:   job.ProjectID,
		Environment: job.Environment,
		Name:        job.Name,
		Format:      job.Format,
		State:       job.State,
		CreatedAt:   job.CreatedAt.UTC().Format(time.RFC3339Nano),
		UpdatedAt:   job.UpdatedAt.UTC().Format(time.RFC3339Nano),
	}
	if job.ErrorCode != nil {
		out.ErrorCode = job.ErrorCode
	}
	if job.ResultRows != nil {
		rows := int64(*job.ResultRows)
		out.ResultRows = &rows
	}
	if job.ArtifactBytes != nil {
		out.ArtifactBytes = job.ArtifactBytes
	}
	if job.ArtifactSHA256 != nil {
		out.ArtifactSha256 = job.ArtifactSHA256
	}
	if job.ExpiresAt != nil {
		formatted := job.ExpiresAt.UTC().Format(time.RFC3339Nano)
		out.ExpiresAt = &formatted
	}
	if job.FinishedAt != nil {
		formatted := job.FinishedAt.UTC().Format(time.RFC3339Nano)
		out.FinishedAt = &formatted
	}
	if job.Source != nil {
		analysisID := job.Source.AnalysisID
		sourceVersion := job.Source.Version
		out.SourceAnalysisID = &analysisID
		out.SourceAnalysisVersion = &sourceVersion
	}
	return out
}

func (a *ExportAPI) create(w http.ResponseWriter, r *http.Request, actor authorizedActor) error {
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
	var body exportRequestBody
	if err = a.API.Boundary.Decode(w, r, "ExportInput", &body); err != nil {
		return err
	}
	// The embedded query must satisfy the same bounded contract as the
	// ad-hoc query API before it is durably persisted.
	request, err := analysis.Parse(body.Query, a.Validator)
	if err != nil {
		return err
	}
	var expiry *time.Time
	if body.ArtifactExpiryAt != nil && *body.ArtifactExpiryAt != "" {
		parsed, parseErr := time.Parse(time.RFC3339, *body.ArtifactExpiryAt)
		if parseErr != nil {
			return fault.New(fault.Invalid, "INVALID_EXPORT_INPUT", "产物过期时间格式无效")
		}
		expiry = &parsed
	}
	hash, err := canonical.Hash(body)
	if err != nil {
		return err
	}
	in := exporting.Input{
		ProjectID:      project,
		Environment:    body.Environment,
		ActorID:        actor.ID,
		Name:           body.Name,
		Format:         body.Format,
		Request:        request,
		IdempotencyKey: key,
		RequestHash:    hash,
		ArtifactExpiry: expiry,
	}
	job, created, err := a.Service.CreateJob(r.Context(), in)
	if err != nil {
		return err
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	return a.API.Boundary.JSON(w, status, "ExportJob", exportJobWire(job))
}

// createFromSavedAnalysis creates an export job from an exact immutable saved
// analysis version. The server rebinds the published snapshot, rechecks the
// metric pin and applies the archived guard inside one storage transaction;
// the caller only supplies the time window, timezone and artifact preferences.
func (a *ExportAPI) createFromSavedAnalysis(w http.ResponseWriter, r *http.Request, actor authorizedActor) error {
	project, err := governanceProject(r)
	if err != nil {
		return err
	}
	if _, err = governanceQuery(r); err != nil {
		return err
	}
	analysisID := r.PathValue("analysis_id")
	if !catalogUUID.MatchString(analysisID) {
		return fault.New(fault.Invalid, "INVALID_SAVED_ANALYSIS", "保存分析标识格式不正确")
	}
	version, err := savedVersion(r.PathValue("version"))
	if err != nil {
		return err
	}
	key, err := savedIdempotency(r)
	if err != nil {
		return err
	}
	var body savedAnalysisExportRequestBody
	if err = a.API.Boundary.Decode(w, r, "SavedAnalysisExportInput", &body); err != nil {
		return err
	}
	var expiry *time.Time
	if body.ArtifactExpiryAt != nil && *body.ArtifactExpiryAt != "" {
		parsed, parseErr := time.Parse(time.RFC3339, *body.ArtifactExpiryAt)
		if parseErr != nil {
			return fault.New(fault.Invalid, "INVALID_EXPORT_INPUT", "产物过期时间格式无效")
		}
		expiry = &parsed
	}
	hash, err := canonical.Hash(body)
	if err != nil {
		return err
	}
	in := exporting.SavedAnalysisExportInput{
		ProjectID:      project,
		ActorID:        actor.ID,
		Name:           body.Name,
		Format:         body.Format,
		TimeRange:      body.TimeRange,
		Timezone:       body.Timezone,
		IdempotencyKey: key,
		RequestHash:    hash,
		ArtifactExpiry: expiry,
	}
	job, created, err := a.Service.CreateSavedAnalysisExportJob(r.Context(), actor.ID, project, analysisID, version, in)
	if err != nil {
		return err
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	return a.API.Boundary.JSON(w, status, "ExportJob", exportJobWire(job))
}

func (a *ExportAPI) list(w http.ResponseWriter, r *http.Request, actor authorizedActor) error {
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
	scope := "export_jobs:" + actor.ID + ":" + project
	position := exportPosition{}
	if raw := q.Get("cursor"); raw != "" {
		decoded, err := a.API.Cursor.Decode(scope, raw)
		if err != nil {
			return governanceCursorError()
		}
		if governanceDecodePosition(decoded, &position) != nil || !catalogUUID.MatchString(position.AfterUUID) {
			return governanceCursorError()
		}
	}
	page, err := a.Service.ListExportJobs(r.Context(), actor.ID, project, exporting.ListFilter{After: position.AfterUUID, Limit: limit})
	if err != nil {
		return err
	}
	out := wire.ExportList{Items: make([]wire.ExportJob, 0, len(page.Items)), NextCursor: nil}
	for _, job := range page.Items {
		out.Items = append(out.Items, exportJobWire(job))
	}
	if len(page.Items) > limit {
		data, err := json.Marshal(exportPosition{AfterUUID: page.After})
		if err != nil {
			return err
		}
		next := a.API.Cursor.Encode(scope, string(data))
		out.NextCursor = &next
		out.Items = out.Items[:limit]
	}
	return a.API.Boundary.JSON(w, http.StatusOK, "ExportList", out)
}

func (a *ExportAPI) get(w http.ResponseWriter, r *http.Request, actor authorizedActor) error {
	project, err := governanceProject(r)
	if err != nil {
		return err
	}
	if _, err = governanceQuery(r); err != nil {
		return err
	}
	jobID := r.PathValue("job_id")
	if !catalogUUID.MatchString(jobID) {
		return fault.New(fault.Invalid, "INVALID_EXPORT_JOB", "导出作业标识格式不正确")
	}
	job, err := a.Service.GetExportJob(r.Context(), actor.ID, project, jobID)
	if err != nil {
		return err
	}
	return a.API.Boundary.JSON(w, http.StatusOK, "ExportJob", exportJobWire(job))
}

func (a *ExportAPI) cancel(w http.ResponseWriter, r *http.Request, actor authorizedActor) error {
	project, err := governanceProject(r)
	if err != nil {
		return err
	}
	if _, err = governanceQuery(r); err != nil {
		return err
	}
	jobID := r.PathValue("job_id")
	if !catalogUUID.MatchString(jobID) {
		return fault.New(fault.Invalid, "INVALID_EXPORT_JOB", "导出作业标识格式不正确")
	}
	job, err := a.Service.CancelExportJob(r.Context(), actor.ID, project, jobID)
	if err != nil {
		return err
	}
	return a.API.Boundary.JSON(w, http.StatusOK, "ExportJob", exportJobWire(job))
}

func (a *ExportAPI) grant(w http.ResponseWriter, r *http.Request, actor authorizedActor) error {
	project, err := governanceProject(r)
	if err != nil {
		return err
	}
	if _, err = governanceQuery(r); err != nil {
		return err
	}
	jobID := r.PathValue("job_id")
	if !catalogUUID.MatchString(jobID) {
		return fault.New(fault.Invalid, "INVALID_EXPORT_JOB", "导出作业标识格式不正确")
	}
	token, grant, err := a.Service.MintGrant(r.Context(), actor.ID, project, jobID)
	if err != nil {
		return err
	}
	return a.API.Boundary.JSON(w, http.StatusCreated, "ExportGrant", wire.ExportGrant{
		Token:     token,
		ExpiresAt: grant.ExpiresAt.UTC().Format(time.RFC3339Nano),
	})
}

// artifactMediaType maps a persisted export format to its streaming media
// type and filename extension. The database CHECK constrains the vocabulary;
// unknown values fall back to json rather than failing an authorized download.
func artifactMediaType(format string) (string, string) {
	switch format {
	case exporting.FormatCSV:
		return "text/csv; charset=utf-8", ".csv"
	case exporting.FormatNDJSON:
		return "application/x-ndjson; charset=utf-8", ".ndjson"
	default:
		return "application/json; charset=utf-8", ".json"
	}
}

// download redeems a one-time grant and streams the JSON artifact. The token
// is the sole credential; responses carry no-store headers and a sha256
// checksum so clients can verify the materialized output.
func (a *ExportAPI) download(w http.ResponseWriter, r *http.Request) error {
	token := r.PathValue("token")
	if len(token) < 32 || len(token) > 128 {
		return fault.New(fault.Forbidden, "GRANT_INVALID", "下载授权无效")
	}
	job, _, err := a.Service.Redeem(r.Context(), token)
	if err != nil {
		return err
	}
	if job.ArtifactPath == nil {
		return fault.New(fault.NotFound, "EXPORT_ARTIFACT_NOT_FOUND", "导出产物不存在")
	}
	handle, err := a.Downloads.Open(r.Context(), *job.ArtifactPath)
	if err != nil {
		return err
	}
	defer handle.Close()
	contentType, extension := artifactMediaType(job.Format)
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Content-Disposition", "attachment; filename=\""+job.JobID+extension+"\"")
	if job.ArtifactSHA256 != nil {
		w.Header().Set("X-Content-SHA256", *job.ArtifactSHA256)
	}
	if job.ArtifactBytes != nil {
		w.Header().Set("Content-Length", strconv.FormatInt(*job.ArtifactBytes, 10))
	}
	if _, err = io.Copy(w, handle); err != nil {
		return fault.New(fault.Internal, "EXPORT_STREAM_FAILED", "导出产物传输失败")
	}
	return nil
}
