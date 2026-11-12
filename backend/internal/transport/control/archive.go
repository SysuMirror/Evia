package control

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/howtomakeaname/Evia/backend/internal/archiving"
	"github.com/howtomakeaname/Evia/backend/internal/gen/wire"
	"github.com/howtomakeaname/Evia/backend/internal/platform/fault"
)

// ArchiveService is the manifest read surface the HTTP boundary needs. The
// same PostgreSQL store satisfies it and re-checks owner authorization inside
// every transaction.
type ArchiveService interface {
	ListManifests(ctx context.Context, actor, project string, filter archiving.ListFilter) (archiving.Page, error)
	GetManifest(ctx context.Context, actor, project, manifestID string) (archiving.Manifest, error)
}

// ArchiveAPI exposes owner-only archive manifest discovery. Manifests are
// written by the retention worker; the HTTP surface is read-only.
type ArchiveAPI struct {
	API     *API
	Service ArchiveService
}

type archivePosition struct {
	AfterUUID string `json:"afterUUID"`
}

func (a *ArchiveAPI) Register(mux *http.ServeMux) {
	if a.Service == nil {
		return
	}
	const base = "/api/v1/projects/{project_id}/archive-manifests"
	mux.HandleFunc("GET "+base, a.API.projectAuth(false, a.list))
	mux.HandleFunc("GET "+base+"/{manifest_id}", a.API.projectAuth(false, a.get))
}

func archiveTime(value time.Time) string {
	return value.UTC().Format(time.RFC3339Nano)
}

func archiveManifestWire(m archiving.Manifest) wire.ArchiveManifest {
	out := wire.ArchiveManifest{
		ManifestID:    m.ManifestID,
		ProjectID:     m.ProjectID,
		Environment:   m.Environment,
		RetentionDays: int64(m.RetentionDays),
		CutoffAt:      archiveTime(m.Cutoff),
		RowCount:      m.RowCount,
		Status:        m.Status,
		CreatedAt:     archiveTime(m.CreatedAt),
		WindowStartAt: nil,
		WindowEndAt:   nil,
	}
	if m.WindowStart != nil {
		value := archiveTime(*m.WindowStart)
		out.WindowStartAt = &value
	}
	if m.WindowEnd != nil {
		value := archiveTime(*m.WindowEnd)
		out.WindowEndAt = &value
	}
	out.ArtifactPath = m.ArtifactPath
	out.ArtifactSha256 = m.ArtifactSHA
	out.ArtifactBytes = m.ArtifactBytes
	out.ErrorCode = m.ErrorCode
	out.SweepID = m.SweepID
	if m.CompletedAt != nil {
		value := archiveTime(*m.CompletedAt)
		out.CompletedAt = &value
	}
	return out
}

func (a *ArchiveAPI) list(w http.ResponseWriter, r *http.Request, actor authorizedActor) error {
	project, err := governanceProject(r)
	if err != nil {
		return err
	}
	q, err := governanceQuery(r, "environment", "limit", "cursor")
	if err != nil {
		return err
	}
	limit, err := governanceLimit(q)
	if err != nil {
		return err
	}
	filter := archiving.ListFilter{Limit: limit, Environment: q.Get("environment")}
	if raw := q.Get("cursor"); raw != "" {
		scope := "archive_manifests:" + actor.ID + ":" + project
		decoded, err := a.API.Cursor.Decode(scope, raw)
		if err != nil {
			return governanceCursorError()
		}
		position := archivePosition{}
		if err = json.Unmarshal([]byte(decoded), &position); err != nil || !catalogUUID.MatchString(position.AfterUUID) {
			return governanceCursorError()
		}
		filter.After = position.AfterUUID
	}
	page, err := a.Service.ListManifests(r.Context(), actor.ID, project, filter)
	if err != nil {
		return err
	}
	out := wire.ArchiveManifestList{Items: make([]wire.ArchiveManifest, 0, len(page.Items)), NextCursor: nil}
	for _, manifest := range page.Items {
		out.Items = append(out.Items, archiveManifestWire(manifest))
	}
	if len(page.Items) > limit {
		data, err := json.Marshal(archivePosition{AfterUUID: page.After})
		if err != nil {
			return err
		}
		next := a.API.Cursor.Encode("archive_manifests:"+actor.ID+":"+project, string(data))
		out.NextCursor = &next
		out.Items = out.Items[:limit]
	}
	return a.API.Boundary.JSON(w, http.StatusOK, "ArchiveManifestList", out)
}

func (a *ArchiveAPI) get(w http.ResponseWriter, r *http.Request, actor authorizedActor) error {
	project, err := governanceProject(r)
	if err != nil {
		return err
	}
	manifestID := r.PathValue("manifest_id")
	if !catalogUUID.MatchString(manifestID) {
		return fault.New(fault.Invalid, "INVALID_ARCHIVE_MANIFEST", "清单标识格式不正确")
	}
	manifest, err := a.Service.GetManifest(r.Context(), actor.ID, project, manifestID)
	if err != nil {
		return err
	}
	return a.API.Boundary.JSON(w, http.StatusOK, "ArchiveManifest", archiveManifestWire(manifest))
}
