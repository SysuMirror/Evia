package postgres

import (
	"context"
	"encoding/json"

	"github.com/howtomakeaname/Evia/backend/internal/analysis"
	"github.com/howtomakeaname/Evia/backend/internal/platform/canonical"
	"github.com/howtomakeaname/Evia/backend/internal/platform/ids"
	"github.com/howtomakeaname/Evia/backend/internal/reporting"
	"github.com/jackc/pgx/v5"
)

// CopyDashboard creates an independent, unpublished dashboard from an existing
// one. A same-environment copy retains every pinned reference, layout,
// parameters and bindings with fresh dashboard/widget IDs. A cross-environment
// copy instead rebinds every executable widget to the explicitly supplied
// target versions (validated to exist, be published and belong to the target
// environment); presentation-only text cards carry over untouched. The source
// keeps every immutable version, run and archive state, and no publication
// state, runs, idempotency history or archive state is carried over.
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
	targetEnvironment := source.Environment
	crossEnvironment := false
	if in.TargetEnvironment != "" {
		if in.TargetEnvironment == source.Environment {
			return out, analysis.Invalid("INVALID_ENVIRONMENT", "目标环境与源环境相同，跨环境复制需选择不同环境", "/target_environment")
		}
		targetEnvironment = in.TargetEnvironment
		crossEnvironment = true
	}
	// The target folder must exist in the destination environment.
	if e = assertDashboardFolderTx(ctx, tx, project, targetEnvironment, in.FolderID); e != nil {
		return out, e
	}
	widgetIDs := make([]string, len(source.Definition.Widgets))
	for i := range widgetIDs {
		widgetIDs[i] = ids.New()
	}
	var definition reporting.DashboardDefinition
	if crossEnvironment {
		// Structural coverage/kind validation first, so malformed rebindings
		// fail with a precise domain error before any version lookup.
		definition, e = reporting.RebindDashboardDefinition(source.Definition, in.Title, widgetIDs, in.Rebindings)
		if e != nil {
			return out, e
		}
		// Semantic validation: every target version must exist and belong to
		// the destination environment.
		if e = s.assertRebindingTargetsTx(ctx, tx, project, targetEnvironment, source.Definition, in.Rebindings); e != nil {
			return out, e
		}
	} else {
		definition, e = reporting.CloneDashboardDefinition(source.Definition, in.Title, widgetIDs)
		if e != nil {
			return out, e
		}
	}
	hash, e := canonical.Hash(struct {
		SourceID string                  `json:"source_id"`
		Input    reporting.DashboardCopy `json:"input"`
	}{id, in})
	if e != nil {
		return out, e
	}
	// Scope the operation by destination environment so the same key can copy
	// into different environments without colliding.
	operation := "copy:" + id
	if crossEnvironment {
		operation = "copy:" + id + ":" + targetEnvironment
	}
	var prior struct {
		DashboardID string `json:"dashboard_id"`
	}
	replay, e := dashboardIdempotencyReplay(ctx, tx, actor, project, targetEnvironment, operation, key, hash, &prior)
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
	if _, e = tx.Exec(ctx, `INSERT INTO dashboards(project_id,environment,dashboard_id,title,folder_id,definition) VALUES($1,$2,$3,$4,NULLIF($5,'')::uuid,$6)`, project, targetEnvironment, newID, definition.Title, in.FolderID, data); e != nil {
		return out, e
	}
	if _, e = tx.Exec(ctx, `INSERT INTO audit_entries(project_id,actor_id,action,resource_key) VALUES($1,$2,'dashboard.copied',$3)`, project, actor, newID); e != nil {
		return out, e
	}
	prior.DashboardID = newID
	if e = dashboardIdempotencyWrite(ctx, tx, actor, project, targetEnvironment, operation, key, hash, prior); e != nil {
		return out, e
	}
	if out, e = readDashboardDraftTx(ctx, tx, project, newID); e != nil {
		return out, e
	}
	return out, tx.Commit(ctx)
}

// assertRebindingTargetsTx verifies that every rebinding points at a published
// version in the destination environment. Missing versions surface the existing
// NOT_FOUND codes; a version in another environment fails closed as an invalid
// rebinding rather than silently crossing the environment boundary.
func (s *Store) assertRebindingTargetsTx(ctx context.Context, tx pgx.Tx, project, targetEnvironment string, source reporting.DashboardDefinition, rebindings []reporting.DashboardRebinding) error {
	compositeWidgets := map[string]bool{}
	for _, widget := range source.Widgets {
		if widget.MetricRef != nil && widget.MetricRef.IsComposite() {
			compositeWidgets[widget.ID] = true
		}
	}
	for _, rebinding := range rebindings {
		if rebinding.AnalysisID != "" {
			version, e := readSavedAnalysisVersionTx(ctx, tx, project, rebinding.AnalysisID, rebinding.AnalysisVersion)
			if e != nil {
				return e
			}
			if version.Environment != targetEnvironment {
				return analysis.Invalid("DASHBOARD_REBINDING_ENVIRONMENT_MISMATCH", "重绑定的保存分析版本不属于目标环境", "/rebindings")
			}
			continue
		}
		// Metric rebindings serve both atomic and composite widgets; the widget
		// kind is preserved by RebindDashboardDefinition, so resolve against the
		// matching immutable version table in the target environment.
		if compositeWidgets[rebinding.WidgetID] {
			formula, e := readFormulaMetricVersionTx(ctx, tx, project, rebinding.MetricID, rebinding.MetricVersion)
			if e != nil {
				return e
			}
			if formula.Environment != targetEnvironment {
				return analysis.Invalid("DASHBOARD_REBINDING_ENVIRONMENT_MISMATCH", "重绑定的复合指标版本不属于目标环境", "/rebindings")
			}
			continue
		}
		version, e := readMetricVersionTx(ctx, tx, project, rebinding.MetricID, rebinding.MetricVersion)
		if e != nil {
			return e
		}
		if version.Environment != targetEnvironment {
			return analysis.Invalid("DASHBOARD_REBINDING_ENVIRONMENT_MISMATCH", "重绑定的指标版本不属于目标环境", "/rebindings")
		}
	}
	return nil
}
