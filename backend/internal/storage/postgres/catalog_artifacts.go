package postgres

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/howtomakeaname/Evia/backend/internal/access"
	"github.com/howtomakeaname/Evia/backend/internal/artifacts"
	"github.com/jackc/pgx/v5"
)

// storedManifest mirrors the immutable JSON document written at release time.
type storedManifest struct {
	Format      string          `json:"format"`
	Status      string          `json:"status"`
	ProjectKey  string          `json:"project_key"`
	Environment string          `json:"environment"`
	Revision    int64           `json:"release_revision"`
	Events      []manifestEvent `json:"events"`
}

// GetReleaseManifest loads the immutable manifest behind a release and maps it
// to the artifact generator input. Read authorization is re-checked inside the
// transaction with the same release-read permission as GetRelease.
func (s *Store) GetReleaseManifest(ctx context.Context, actor, project, releaseID string) (artifacts.ReleasePlan, error) {
	tx, e := s.catalogTx(ctx, actor, project, false)
	if e != nil {
		return artifacts.ReleasePlan{}, e
	}
	defer tx.Rollback(context.Background())

	var (
		revision     int64
		environment  string
		manifestHash string
		raw          []byte
	)
	e = tx.QueryRow(ctx, `SELECT r.revision,r.environment,r.manifest_hash,r.manifest
 FROM catalog_releases r WHERE r.project_id=$1 AND r.id=$2`, project, releaseID).
		Scan(&revision, &environment, &manifestHash, &raw)
	if errors.Is(e, pgx.ErrNoRows) {
		return artifacts.ReleasePlan{}, releaseMissing()
	}
	if e != nil {
		return artifacts.ReleasePlan{}, e
	}
	if e = s.authorizeOperation(ctx, tx, actor, project, environment, access.PermissionCatalogReleasesRead); e != nil {
		return artifacts.ReleasePlan{}, e
	}
	var manifest storedManifest
	if e = json.Unmarshal(raw, &manifest); e != nil {
		return artifacts.ReleasePlan{}, e
	}
	plan := artifacts.ReleasePlan{
		ReleaseID:    releaseID,
		Revision:     revision,
		Environment:  environment,
		ManifestHash: manifestHash,
		Events:       make([]artifacts.EventSchema, 0, len(manifest.Events)),
	}
	for _, event := range manifest.Events {
		plan.Events = append(plan.Events, artifacts.EventSchema{
			Key:                event.Key,
			SchemaVersion:      event.SchemaVersion,
			DisplayName:        event.DisplayName,
			Schema:             event.Schema,
			AllowedSourceKinds: event.AllowedSourceKinds,
			FactKind:           event.FactKind,
		})
	}
	return plan, tx.Commit(ctx)
}
