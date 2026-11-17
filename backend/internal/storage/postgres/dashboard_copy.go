package postgres

import (
	"context"
	"encoding/json"

	"github.com/howtomakeaname/Evia/backend/internal/platform/canonical"
	"github.com/howtomakeaname/Evia/backend/internal/platform/ids"
	"github.com/howtomakeaname/Evia/backend/internal/reporting"
)

// CopyDashboard creates an independent, unpublished dashboard from an existing
// one in the same project/environment. The source keeps every immutable
// version, run and archive state; the copy starts at draft revision one with
// fresh dashboard/widget IDs, retaining pinned references, layout, parameters
// and bindings. Same-key retries replay the created copy.
func (s *Store) CopyDashboard(ctx context.Context, actor, project, id, key string, in reporting.DashboardCopy) (reporting.DashboardDraft, error) {
	var out reporting.DashboardDraft
	tx, e := s.catalogTx(ctx, actor, project, true)
	if e != nil {
		return out, e
	}
	defer tx.Rollback(context.Background())
	// The source resource must be visible and the actor needs write permission
	// in its environment to create the sibling dashboard.
	if e = s.authorizeReportingResource(ctx, tx, actor, project, id, "dashboards", "dashboard_id", "dashboards.write"); e != nil {
		return out, e
	}
	if e = savedKey(key); e != nil {
		return out, e
	}
	source, e := readDashboardDraftTx(ctx, tx, project, id)
	if e != nil {
		return out, e
	}
	if e = assertDashboardFolderTx(ctx, tx, project, source.Environment, in.FolderID); e != nil {
		return out, e
	}
	widgetIDs := make([]string, len(source.Definition.Widgets))
	for i := range widgetIDs {
		widgetIDs[i] = ids.New()
	}
	definition, e := reporting.CloneDashboardDefinition(source.Definition, in.Title, widgetIDs)
	if e != nil {
		return out, e
	}
	hash, e := canonical.Hash(struct {
		SourceID string                  `json:"source_id"`
		Input    reporting.DashboardCopy `json:"input"`
	}{id, in})
	if e != nil {
		return out, e
	}
	operation := "copy:" + id
	var prior struct {
		DashboardID string `json:"dashboard_id"`
	}
	replay, e := dashboardIdempotencyReplay(ctx, tx, actor, project, source.Environment, operation, key, hash, &prior)
	if e != nil {
		return out, e
	}
	if replay {
		if out, e = readDashboardDraftTx(ctx, tx, project, prior.DashboardID); e != nil {
			return out, e
		}
		return out, tx.Commit(ctx)
	}
	data, e := json.Marshal(definition)
	if e != nil {
		return out, e
	}
	newID := ids.New()
	if _, e = tx.Exec(ctx, `INSERT INTO dashboards(project_id,environment,dashboard_id,title,folder_id,definition) VALUES($1,$2,$3,$4,NULLIF($5,'')::uuid,$6)`, project, source.Environment, newID, definition.Title, in.FolderID, data); e != nil {
		return out, e
	}
	if _, e = tx.Exec(ctx, `INSERT INTO audit_entries(project_id,actor_id,action,resource_key) VALUES($1,$2,'dashboard.copied',$3)`, project, actor, newID); e != nil {
		return out, e
	}
	prior.DashboardID = newID
	if e = dashboardIdempotencyWrite(ctx, tx, actor, project, source.Environment, operation, key, hash, prior); e != nil {
		return out, e
	}
	if out, e = readDashboardDraftTx(ctx, tx, project, newID); e != nil {
		return out, e
	}
	return out, tx.Commit(ctx)
}
