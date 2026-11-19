//go:build integration

package postgres

import (
	"context"
	"testing"

	"github.com/howtomakeaname/Evia/backend/internal/analysis"
	"github.com/howtomakeaname/Evia/backend/internal/catalog"
	"github.com/howtomakeaname/Evia/backend/internal/platform/ids"
	"github.com/howtomakeaname/Evia/backend/internal/reporting"
)

// releaseEventToEnvironment activates the shared lesson_opened event in another
// environment so saved analyses and metrics can be published there.
func releaseEventToEnvironment(t *testing.T, f catalogFixture, environment string) {
	t.Helper()
	ctx := context.Background()
	draft, e := f.svc.GetDraft(ctx, f.owner, f.project, "lesson_opened")
	if e != nil {
		t.Fatal(e)
	}
	if _, e = f.svc.CreateRelease(ctx, f.owner, f.project, "release-"+environment, catalog.ReleaseInput{
		Environment: environment,
		Events:      []catalog.ReleaseEvent{{Key: "lesson_opened", DraftRevision: draft.Revision}},
	}); e != nil {
		t.Fatal(e)
	}
}

func publishedAnalysisInEnvironment(t *testing.T, f catalogFixture, environment, name string) reporting.SavedAnalysisVersion {
	t.Helper()
	draft, e := f.s.CreateSavedAnalysis(context.Background(), f.owner, f.project, name, reporting.SavedAnalysisCreate{
		Environment: environment,
		Definition:  savedRunDefinition(),
	})
	if e != nil {
		t.Fatal(e)
	}
	return savedRunPublish(t, f, draft)
}

func publishedMetricInEnvironment(t *testing.T, f catalogFixture, environment, name string) reporting.MetricDraft {
	t.Helper()
	ctx := context.Background()
	def := reporting.MetricDefinition{
		Title:         "Cross env metric " + environment,
		EventVersions: []analysis.EventVersion{{EventKey: "lesson_opened", SchemaVersion: 1}},
		Subject:       analysis.SubjectUID,
		Aggregation:   analysis.Metric{Op: analysis.OpCount},
		Unit:          "event",
	}
	created, e := f.s.CreateMetric(ctx, f.owner, f.project, name, reporting.MetricCreate{
		Environment: environment,
		Definition:  def,
	})
	if e != nil {
		t.Fatal(e)
	}
	snapshot, e := f.s.MetricValidationSnapshot(ctx, f.owner, f.project, created.ID, reporting.MetricValidate{
		DraftRevision:            created.DraftRevision,
		ExpectedPublishedVersion: 0,
	})
	if e != nil {
		t.Fatal(e)
	}
	validation, e := reporting.BuildMetricValidation(snapshot)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = f.s.PublishMetric(ctx, f.owner, f.project, created.ID, ids.New(), reporting.MetricPublish{
		DraftRevision:            created.DraftRevision,
		ExpectedPublishedVersion: 0,
		ValidationHash:           validation.ValidationHash,
	}); e != nil {
		t.Fatal(e)
	}
	published, e := f.s.GetMetricDraft(ctx, f.owner, f.project, created.ID)
	if e != nil {
		t.Fatal(e)
	}
	return published
}

func TestDashboardCrossEnvironmentCopyRebindsAnalysis(t *testing.T) {
	f, d, _ := dashboardFixture(t, 1)
	dashboardPublish(t, f, d)
	ctx := context.Background()
	releaseEventToEnvironment(t, f, "staging")
	releaseEventToEnvironment(t, f, "production")
	stagingAnalysis := publishedAnalysisInEnvironment(t, f, "staging", "staging-analysis")
	productionAnalysis := publishedAnalysisInEnvironment(t, f, "production", "production-analysis")
	sourceWidget := d.Definition.Widgets[0]

	copyTo := func(key, environment string, target reporting.SavedAnalysisVersion) (reporting.DashboardDraft, error) {
		return f.s.CopyDashboard(ctx, f.owner, f.project, d.ID, key, reporting.DashboardCopy{
			Title:             "Cross env " + environment,
			TargetEnvironment: environment,
			Rebindings: []reporting.DashboardRebinding{{
				WidgetID:        sourceWidget.ID,
				AnalysisID:      target.AnalysisID,
				AnalysisVersion: target.Version,
			}},
		})
	}

	stagingCopy, e := copyTo("cross-key", "staging", stagingAnalysis)
	if e != nil {
		t.Fatalf("cross-env copy rejected: %v", e)
	}
	if stagingCopy.Environment != "staging" || stagingCopy.ID == d.ID {
		t.Fatalf("copy must land in staging with a fresh ID: %+v", stagingCopy)
	}
	if stagingCopy.DraftRevision != 1 || stagingCopy.PublishedVersion != 0 {
		t.Fatalf("cross-env copy must be an unpublished r1 draft: %+v", stagingCopy)
	}
	rebound := stagingCopy.Definition.Widgets[0]
	if rebound.ID == sourceWidget.ID {
		t.Fatal("widget ID must be regenerated")
	}
	if rebound.AnalysisID != stagingAnalysis.AnalysisID || rebound.AnalysisVersion != stagingAnalysis.Version || rebound.MetricRef != nil {
		t.Fatalf("widget not rebound to staging analysis: %+v", rebound)
	}
	var versionRows, runRows, stagingIdempotency int
	if e = f.s.Pool.QueryRow(ctx, `SELECT count(*) FROM dashboard_versions WHERE dashboard_id=$1`, stagingCopy.ID).Scan(&versionRows); e != nil || versionRows != 0 {
		t.Fatalf("copy must stay unpublished: rows=%d err=%v", versionRows, e)
	}
	if e = f.s.Pool.QueryRow(ctx, `SELECT count(*) FROM dashboard_runs WHERE dashboard_id=$1`, stagingCopy.ID).Scan(&runRows); e != nil || runRows != 0 {
		t.Fatalf("copy must carry no runs: rows=%d err=%v", runRows, e)
	}
	if e = f.s.Pool.QueryRow(ctx, `SELECT count(*) FROM dashboard_idempotency WHERE project_id=$1 AND environment='staging' AND idempotency_key=$2`, f.project, "cross-key").Scan(&stagingIdempotency); e != nil || stagingIdempotency != 1 {
		t.Fatalf("idempotency must be recorded in the target environment: rows=%d err=%v", stagingIdempotency, e)
	}

	// Same-key replay returns the identical staging copy.
	replay, e := copyTo("cross-key", "staging", stagingAnalysis)
	if e != nil || replay.ID != stagingCopy.ID {
		t.Fatalf("same-key replay must return the same copy: %+v %v", replay, e)
	}
	// The same key against a different environment is an independent operation.
	productionCopy, e := copyTo("cross-key", "production", productionAnalysis)
	if e != nil || productionCopy.Environment != "production" || productionCopy.ID == stagingCopy.ID {
		t.Fatalf("idempotency must be scoped per target environment: %+v %v", productionCopy, e)
	}

	// A development-only reference cannot satisfy a staging target.
	_, e = copyTo(ids.New(), "staging", reporting.SavedAnalysisVersion{
		AnalysisID: sourceWidget.AnalysisID,
		Version:    sourceWidget.AnalysisVersion,
	})
	faultCode(t, e, "DASHBOARD_REBINDING_ENVIRONMENT_MISMATCH")

	// A nonexistent target version fails closed with the existing not-found code.
	_, e = f.s.CopyDashboard(ctx, f.owner, f.project, d.ID, ids.New(), reporting.DashboardCopy{
		Title: "Ghost target", TargetEnvironment: "staging",
		Rebindings: []reporting.DashboardRebinding{{
			WidgetID: sourceWidget.ID, AnalysisID: "33333333-3333-4333-8333-333333333333", AnalysisVersion: 99,
		}},
	})
	faultCode(t, e, "SAVED_ANALYSIS_VERSION_NOT_FOUND")

	// Missing rebinding coverage is rejected structurally.
	_, e = f.s.CopyDashboard(ctx, f.owner, f.project, d.ID, ids.New(), reporting.DashboardCopy{
		Title: "No rebinding", TargetEnvironment: "staging",
	})
	faultCode(t, e, "INVALID_DASHBOARD_REBINDING")

	// Targeting the source environment is an explicit no-op rejection.
	_, e = f.s.CopyDashboard(ctx, f.owner, f.project, d.ID, ids.New(), reporting.DashboardCopy{
		Title: "Same env", TargetEnvironment: "development",
		Rebindings: []reporting.DashboardRebinding{{
			WidgetID: sourceWidget.ID, AnalysisID: stagingAnalysis.AnalysisID, AnalysisVersion: stagingAnalysis.Version,
		}},
	})
	faultCode(t, e, "INVALID_ENVIRONMENT")

	// A development folder cannot anchor a staging copy.
	devFolder, e := f.s.CreateDashboardFolder(ctx, f.owner, f.project, ids.New(), reporting.DashboardFolderCreate{
		Environment: "development", Title: "Dev only",
	})
	if e != nil {
		t.Fatal(e)
	}
	_, e = f.s.CopyDashboard(ctx, f.owner, f.project, d.ID, ids.New(), reporting.DashboardCopy{
		Title: "Wrong folder", TargetEnvironment: "staging", FolderID: devFolder.ID,
		Rebindings: []reporting.DashboardRebinding{{
			WidgetID: sourceWidget.ID, AnalysisID: stagingAnalysis.AnalysisID, AnalysisVersion: stagingAnalysis.Version,
		}},
	})
	faultCode(t, e, "DASHBOARD_FOLDER_NOT_FOUND")

	// The source dashboard keeps its original environment and publication.
	if source, e := f.s.GetDashboard(ctx, f.viewer, f.project, d.ID); e != nil || source.Environment != "development" || source.PublishedVersion != 1 {
		t.Fatalf("source dashboard changed: %+v %v", source, e)
	}
}

func TestDashboardCrossEnvironmentCopyTextOnlyNeedsNoRebinding(t *testing.T) {
	f, d, _ := dashboardFixture(t, 1)
	ctx := context.Background()
	textOnly, e := f.s.SaveDashboardDraft(ctx, f.owner, f.project, d.ID, d.DraftRevision, textOnlyDashboard(), "")
	if e != nil {
		t.Fatal(e)
	}
	copy1, e := f.s.CopyDashboard(ctx, f.owner, f.project, d.ID, "text-xenv", reporting.DashboardCopy{
		Title: "Text only in staging", TargetEnvironment: "staging",
	})
	if e != nil {
		t.Fatalf("text-only cross-env copy must not require rebindings: %v", e)
	}
	if copy1.Environment != "staging" {
		t.Fatalf("copy must land in staging, got %q", copy1.Environment)
	}
	card := copy1.Definition.Widgets[0]
	if card.TextCard == nil || card.TextCard.Content == "" || card.ID == textOnly.Definition.Widgets[0].ID {
		t.Fatalf("text card must carry over with a fresh widget ID: %+v", card)
	}
}

func TestDashboardCrossEnvironmentCopyRebindsMetric(t *testing.T) {
	f, _, _ := dashboardFixture(t, 1)
	ctx := context.Background()
	devMetric := publishDashboardMetric(t, f)
	releaseEventToEnvironment(t, f, "staging")
	stagingMetric := publishedMetricInEnvironment(t, f, "staging", "staging-metric")

	sourceDefinition := reporting.DashboardDefinition{
		Title: "Metric board",
		Widgets: []reporting.DashboardWidget{{
			ID: "11111111-1111-4111-8111-111111111111",
			MetricRef: &reporting.MetricReference{
				MetricID: devMetric.ID,
				Version:  devMetric.PublishedVersion,
			},
			Title: "Metric card", Layout: reporting.DashboardLayout{Width: 12, Height: 4},
		}},
	}
	draft, e := f.s.CreateDashboard(ctx, f.owner, f.project, "metric-board", reporting.DashboardCreate{
		Environment: "development", Definition: sourceDefinition,
	})
	if e != nil {
		t.Fatal(e)
	}
	copy1, e := f.s.CopyDashboard(ctx, f.owner, f.project, draft.ID, "metric-xenv", reporting.DashboardCopy{
		Title: "Rebound metric board", TargetEnvironment: "staging",
		Rebindings: []reporting.DashboardRebinding{{
			WidgetID: sourceDefinition.Widgets[0].ID,
			MetricID: stagingMetric.ID, MetricVersion: stagingMetric.PublishedVersion,
		}},
	})
	if e != nil {
		t.Fatalf("metric cross-env copy rejected: %v", e)
	}
	rebound := copy1.Definition.Widgets[0]
	if rebound.MetricRef == nil || rebound.MetricRef.MetricID != stagingMetric.ID || rebound.MetricRef.Version != stagingMetric.PublishedVersion || rebound.AnalysisID != "" {
		t.Fatalf("metric widget not rebound to staging: %+v", rebound)
	}

	// A development metric cannot satisfy a staging target.
	_, e = f.s.CopyDashboard(ctx, f.owner, f.project, draft.ID, ids.New(), reporting.DashboardCopy{
		Title: "Wrong metric env", TargetEnvironment: "staging",
		Rebindings: []reporting.DashboardRebinding{{
			WidgetID: sourceDefinition.Widgets[0].ID,
			MetricID: devMetric.ID, MetricVersion: devMetric.PublishedVersion,
		}},
	})
	faultCode(t, e, "DASHBOARD_REBINDING_ENVIRONMENT_MISMATCH")

	// A metric widget cannot be rebound to an analysis version.
	_, e = f.s.CopyDashboard(ctx, f.owner, f.project, draft.ID, ids.New(), reporting.DashboardCopy{
		Title: "Kind mismatch", TargetEnvironment: "staging",
		Rebindings: []reporting.DashboardRebinding{{
			WidgetID:   sourceDefinition.Widgets[0].ID,
			AnalysisID: stagingMetric.ID, AnalysisVersion: stagingMetric.PublishedVersion,
		}},
	})
	faultCode(t, e, "INVALID_DASHBOARD_REBINDING")
}
