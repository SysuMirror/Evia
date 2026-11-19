package control

import (
	"context"
	"net/http"

	"github.com/howtomakeaname/Evia/backend/internal/artifacts"
	"github.com/howtomakeaname/Evia/backend/internal/gen/wire"
	"github.com/howtomakeaname/Evia/backend/internal/platform/fault"
)

// ArtifactService renders authorized source artifacts from release manifests.
type ArtifactService interface {
	BuildTypeScript(ctx context.Context, actor, project, releaseID string) (artifacts.ReleasePlan, artifacts.Artifact, error)
}

// ArtifactAPI exposes read-only release integration artifacts.
type ArtifactAPI struct {
	API     *API
	Service ArtifactService
}

func (a *ArtifactAPI) Register(mux *http.ServeMux) {
	if a.Service == nil {
		return
	}
	mux.HandleFunc(
		"GET /api/v1/projects/{project_id}/releases/{release_id}/artifacts/typescript",
		a.API.projectAuth(false, a.getTypeScript),
	)
}

func (a *ArtifactAPI) getTypeScript(w http.ResponseWriter, r *http.Request, actor authorizedActor) error {
	project, err := governanceProject(r)
	if err != nil {
		return err
	}
	releaseID := r.PathValue("release_id")
	if !catalogUUID.MatchString(releaseID) {
		return fault.New(fault.Invalid, "INVALID_RELEASE_ID", "发布标识格式不正确")
	}
	if _, err = governanceQuery(r); err != nil {
		return err
	}
	plan, artifact, err := a.Service.BuildTypeScript(r.Context(), actor.ID, project, releaseID)
	if err != nil {
		return err
	}
	out := wire.TypeScriptArtifact{
		ReleaseID:       plan.ReleaseID,
		Environment:     plan.Environment,
		ReleaseRevision: plan.Revision,
		ManifestHash:    plan.ManifestHash,
		Language:        artifactLanguageTypeScript,
		LanguageVersion: artifacts.LanguageVersion,
		EventCount:      int64(artifact.EventCount),
		ByteSize:        int64(artifact.ByteSize),
		Checksum:        artifact.Checksum,
		Source:          artifact.Source,
		Compatibility:   []wire.TypeScriptEventCompatibility{},
	}
	for _, item := range artifact.Compatibility {
		out.Compatibility = append(out.Compatibility, wire.TypeScriptEventCompatibility{
			EventKey:              item.EventKey,
			SchemaVersion:         item.SchemaVersion,
			InterfaceName:         item.InterfaceName,
			ValidIdentifier:       item.ValidIdentifier,
			UnsupportedConstructs: item.UnsupportedConstructs,
		})
	}
	return a.API.Boundary.JSON(w, http.StatusOK, "TypeScriptArtifact", out)
}
