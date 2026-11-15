//go:build integration

package postgres

import (
	"context"
	"errors"
	"testing"

	"github.com/howtomakeaname/Evia/backend/internal/analysis"
	"github.com/howtomakeaname/Evia/backend/internal/platform/fault"
	"github.com/howtomakeaname/Evia/backend/internal/platform/ids"
	"github.com/howtomakeaname/Evia/backend/internal/reporting"
)

// formulaFixture provisions one published event and two published atomic
// metrics in the same project so a composite formula can pin real immutable
// versions through the authorization-aware resolver.
type formulaFixture struct {
	catalogFixture
	atomicA reporting.MetricDraft
	atomicB reporting.MetricDraft
}

func formulaDB(t *testing.T) formulaFixture {
	t.Helper()
	f := catalogDB(t)
	ctx := context.Background()
	draft := f.event(t, "lesson_opened")
	if _, e := f.svc.CreateRelease(ctx, f.owner, f.project, "formula-release", releaseInput("lesson_opened", draft.Revision)); e != nil {
		t.Fatal(e)
	}
	createAtomic := func(title string) reporting.MetricDraft {
		def := reporting.MetricDefinition{Title: title, EventVersions: []analysis.EventVersion{{EventKey: "lesson_opened", SchemaVersion: 1}}, Subject: "uid", Aggregation: analysis.Metric{Op: "count"}, Unit: "event"}
		created, e := f.s.CreateMetric(ctx, f.owner, f.project, "create-"+title, reporting.MetricCreate{Environment: "development", Definition: def})
		if e != nil {
			t.Fatal(e)
		}
		snapshot, e := f.s.MetricValidationSnapshot(ctx, f.owner, f.project, created.ID, reporting.MetricValidate{DraftRevision: created.DraftRevision, ExpectedPublishedVersion: 0})
		if e != nil {
			t.Fatal(e)
		}
		validation, e := reporting.BuildMetricValidation(snapshot)
		if e != nil {
			t.Fatal(e)
		}
		if _, e = f.s.PublishMetric(ctx, f.owner, f.project, created.ID, "publish-"+title, reporting.MetricPublish{DraftRevision: created.DraftRevision, ExpectedPublishedVersion: 0, ValidationHash: validation.ValidationHash}); e != nil {
			t.Fatal(e)
		}
		published, e := f.s.GetMetricDraft(ctx, f.owner, f.project, created.ID)
		if e != nil {
			t.Fatal(e)
		}
		return published
	}
	return formulaFixture{catalogFixture: f, atomicA: createAtomic("A"), atomicB: createAtomic("B")}
}

func formulaMetricDef(a, b reporting.MetricDraft) reporting.FormulaMetricDefinition {
	refA := reporting.MetricReference{MetricID: a.ID, Version: a.PublishedVersion}
	refB := reporting.MetricReference{MetricID: b.ID, Version: b.PublishedVersion}
	expr := reporting.FormulaExpression{
		Op:    reporting.FormulaDivide,
		Left:  &reporting.FormulaExpression{Op: reporting.FormulaMetric, Metric: &refA},
		Right: &reporting.FormulaExpression{Op: reporting.FormulaMetric, Metric: &refB},
	}
	return reporting.FormulaMetricDefinition{Title: "Lesson ratio", Description: "two atomic counts", Formula: reporting.FormulaDefinition{Expression: expr, Unit: "number"}}
}

func formulaValidate(t *testing.T, f formulaFixture, d reporting.FormulaMetricDraft) reporting.FormulaMetricValidation {
	t.Helper()
	snapshot, e := f.s.FormulaMetricValidationSnapshot(context.Background(), f.owner, f.project, d.ID, reporting.FormulaMetricValidate{DraftRevision: d.DraftRevision, ExpectedPublishedVersion: d.PublishedVersion})
	if e != nil {
		t.Fatal(e)
	}
	out, e := reporting.BuildFormulaMetricValidation(snapshot)
	if e != nil {
		t.Fatal(e)
	}
	return out
}

func TestFormulaMetricLifecycleAndImmutableWitnesses(t *testing.T) {
	f := formulaDB(t)
	ctx := context.Background()
	created, e := f.s.CreateFormulaMetric(ctx, f.owner, f.project, "formula-create", reporting.FormulaMetricCreate{Environment: "development", Definition: formulaMetricDef(f.atomicA, f.atomicB)})
	if e != nil {
		t.Fatal(e)
	}
	if created.DraftRevision != 1 || created.PublishedVersion != 0 {
		t.Fatalf("draft state %+v", created)
	}
	// Idempotent create replay returns the same snapshot.
	replay, e := f.s.CreateFormulaMetric(ctx, f.owner, f.project, "formula-create", reporting.FormulaMetricCreate{Environment: "development", Definition: formulaMetricDef(f.atomicA, f.atomicB)})
	if e != nil || replay.ID != created.ID {
		t.Fatalf("replay id=%s err=%v", replay.ID, e)
	}
	validation := formulaValidate(t, f, created)
	if !validation.Valid || validation.ExecutionPerformed || len(validation.Dependencies.Metrics) != 2 {
		t.Fatalf("validation %+v", validation)
	}
	if validation.DefinitionHash == "" || validation.DependencyHash == "" || validation.ValidationHash == "" {
		t.Fatal("missing validation digests")
	}
	version, e := f.s.PublishFormulaMetric(ctx, f.owner, f.project, created.ID, "formula-publish", reporting.FormulaMetricPublish{DraftRevision: created.DraftRevision, ExpectedPublishedVersion: 0, ValidationHash: validation.ValidationHash})
	if e != nil {
		t.Fatal(e)
	}
	if version.Version != 1 || version.DefinitionHash != validation.DefinitionHash {
		t.Fatalf("version %+v", version)
	}
	var witnesses int
	if e = f.s.Pool.QueryRow(ctx, `SELECT count(*) FROM formula_metric_dependencies WHERE project_id=$1 AND metric_id=$2 AND version=$3`, f.project, created.ID, version.Version).Scan(&witnesses); e != nil || witnesses != 2 {
		t.Fatalf("witnesses=%d err=%v", witnesses, e)
	}
	// Immutable version cannot be updated or deleted.
	if _, e = f.s.Pool.Exec(ctx, `DELETE FROM formula_metric_versions WHERE project_id=$1 AND metric_id=$2`, f.project, created.ID); e == nil {
		t.Fatal("immutable version deletion must be rejected")
	}
	// Re-publishing after a draft change produces version 2 with stable hashing.
	updated := formulaMetricDef(f.atomicA, f.atomicB)
	updated.Description = "revised description"
	saved, e := f.s.SaveFormulaMetricDraft(ctx, f.owner, f.project, created.ID, 1, updated)
	if e != nil {
		t.Fatal(e)
	}
	if saved.DraftRevision != 2 {
		t.Fatalf("revision %d", saved.DraftRevision)
	}
	validation2 := formulaValidate(t, f, saved)
	version2, e := f.s.PublishFormulaMetric(ctx, f.owner, f.project, created.ID, "formula-publish-2", reporting.FormulaMetricPublish{DraftRevision: 2, ExpectedPublishedVersion: 1, ValidationHash: validation2.ValidationHash})
	if e != nil || version2.Version != 2 {
		t.Fatalf("second publish version=%+v err=%v", version2, e)
	}
	listed, e := f.s.ListFormulaMetricVersions(ctx, f.owner, f.project, created.ID, 0, 10)
	if e != nil || len(listed) != 2 {
		t.Fatalf("versions len=%d err=%v", len(listed), e)
	}
}

func TestFormulaMetricRejectsStaleRevision(t *testing.T) {
	f := formulaDB(t)
	ctx := context.Background()
	created, e := f.s.CreateFormulaMetric(ctx, f.owner, f.project, "formula-rev", reporting.FormulaMetricCreate{Environment: "development", Definition: formulaMetricDef(f.atomicA, f.atomicB)})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = f.s.SaveFormulaMetricDraft(ctx, f.owner, f.project, created.ID, 99, formulaMetricDef(f.atomicA, f.atomicB)); e == nil {
		t.Fatal("expected revision conflict")
	}
}

func TestFormulaMetricRejectsSelfReference(t *testing.T) {
	f := formulaDB(t)
	ctx := context.Background()
	created, e := f.s.CreateFormulaMetric(ctx, f.owner, f.project, "formula-self", reporting.FormulaMetricCreate{Environment: "development", Definition: formulaMetricDef(f.atomicA, f.atomicB)})
	if e != nil {
		t.Fatal(e)
	}
	selfRef := reporting.MetricReference{MetricID: created.ID, Version: 1}
	selfDef := reporting.FormulaMetricDefinition{Title: "Self", Description: "cycle", Formula: reporting.FormulaDefinition{Expression: reporting.FormulaExpression{Op: reporting.FormulaMetric, Metric: &selfRef}, Unit: "event"}}
	if _, e = f.s.SaveFormulaMetricDraft(ctx, f.owner, f.project, created.ID, 1, selfDef); e != nil {
		t.Fatal(e)
	}
	_, e = f.s.FormulaMetricValidationSnapshot(ctx, f.owner, f.project, created.ID, reporting.FormulaMetricValidate{DraftRevision: 2, ExpectedPublishedVersion: 0})
	if e == nil {
		t.Fatal("expected self reference rejection")
	}
}

func TestFormulaMetricRejectsCrossEnvironmentReference(t *testing.T) {
	f := formulaDB(t)
	ctx := context.Background()
	// The composite lives in production while its operands are development-only.
	created, e := f.s.CreateFormulaMetric(ctx, f.owner, f.project, "formula-cross", reporting.FormulaMetricCreate{Environment: "production", Definition: formulaMetricDef(f.atomicA, f.atomicB)})
	if e != nil {
		t.Fatal(e)
	}
	_, err := f.s.FormulaMetricValidationSnapshot(ctx, f.owner, f.project, created.ID, reporting.FormulaMetricValidate{DraftRevision: 1, ExpectedPublishedVersion: 0})
	var problem *fault.Error
	if !errors.As(err, &problem) || problem.Code != "INVALID_METRIC_REFERENCE" {
		t.Fatalf("expected cross environment rejection, got %v", err)
	}
}

func TestFormulaMetricRejectsUnpublishedReference(t *testing.T) {
	f := formulaDB(t)
	ctx := context.Background()
	missing := reporting.MetricReference{MetricID: f.atomicA.ID, Version: 99}
	def := reporting.FormulaMetricDefinition{Title: "Missing", Description: "unpublished", Formula: reporting.FormulaDefinition{Expression: reporting.FormulaExpression{Op: reporting.FormulaMetric, Metric: &missing}, Unit: "event"}}
	created, e := f.s.CreateFormulaMetric(ctx, f.owner, f.project, "formula-missing", reporting.FormulaMetricCreate{Environment: "development", Definition: def})
	if e != nil {
		t.Fatal(e)
	}
	if _, err := f.s.FormulaMetricValidationSnapshot(ctx, f.owner, f.project, created.ID, reporting.FormulaMetricValidate{DraftRevision: 1, ExpectedPublishedVersion: 0}); err == nil {
		t.Fatal("expected missing metric version rejection")
	}
}

func TestFormulaMetricRejectsUnitMismatch(t *testing.T) {
	f := formulaDB(t)
	ctx := context.Background()
	def := formulaMetricDef(f.atomicA, f.atomicB)
	// event/event derives dimensionless number; declaring event must fail.
	def.Formula.Unit = "event"
	created, e := f.s.CreateFormulaMetric(ctx, f.owner, f.project, "formula-unit", reporting.FormulaMetricCreate{Environment: "development", Definition: def})
	if e != nil {
		t.Fatal(e)
	}
	if _, err := f.s.FormulaMetricValidationSnapshot(ctx, f.owner, f.project, created.ID, reporting.FormulaMetricValidate{DraftRevision: 1, ExpectedPublishedVersion: 0}); err == nil {
		t.Fatal("expected unit mismatch rejection")
	}
}

func TestFormulaMetricEnforcesWritePermission(t *testing.T) {
	f := formulaDB(t)
	ctx := context.Background()
	if _, e := f.s.CreateFormulaMetric(ctx, f.viewer, f.project, "formula-forbidden", reporting.FormulaMetricCreate{Environment: "development", Definition: formulaMetricDef(f.atomicA, f.atomicB)}); e == nil {
		t.Fatal("viewer must not create a formula metric")
	}
}

func TestFormulaMetricArchiveLifecycleAndMutationGuards(t *testing.T) {
	f := formulaDB(t)
	ctx := context.Background()
	def := formulaMetricDef(f.atomicA, f.atomicB)
	created, e := f.s.CreateFormulaMetric(ctx, f.owner, f.project, "formula-archive", reporting.FormulaMetricCreate{Environment: "development", Definition: def})
	if e != nil {
		t.Fatal(e)
	}
	validation := formulaValidate(t, f, created)
	version, e := f.s.PublishFormulaMetric(ctx, f.owner, f.project, created.ID, "formula-archive-publish", reporting.FormulaMetricPublish{DraftRevision: created.DraftRevision, ExpectedPublishedVersion: 0, ValidationHash: validation.ValidationHash})
	if e != nil {
		t.Fatal(e)
	}
	keep, e := f.s.CreateFormulaMetric(ctx, f.owner, f.project, "formula-archive-keep", reporting.FormulaMetricCreate{Environment: "development", Definition: def})
	if e != nil {
		t.Fatal(e)
	}

	// A read-only viewer cannot change lifecycle state.
	if _, e = f.s.ArchiveFormulaMetric(ctx, f.viewer, f.project, created.ID); e == nil || faultPublicCode(e) != "PROJECT_FORBIDDEN" {
		t.Fatalf("viewer archive must be forbidden: %v", e)
	}

	archived, e := f.s.ArchiveFormulaMetric(ctx, f.owner, f.project, created.ID)
	if e != nil {
		t.Fatal(e)
	}
	if archived.ArchivedAt == nil || archived.ArchivedBy != f.owner {
		t.Fatalf("archive marker missing: %+v", archived)
	}
	if _, e = f.s.ArchiveFormulaMetric(ctx, f.owner, f.project, created.ID); e == nil || faultPublicCode(e) != "FORMULA_METRIC_ALREADY_ARCHIVED" {
		t.Fatalf("re-archive must conflict: %v", e)
	}

	// Default listing hides archived formula metrics; the archived stream is opt-in.
	listed, e := f.s.ListFormulaMetrics(ctx, f.owner, f.project, "development", "", 100, false)
	if e != nil {
		t.Fatal(e)
	}
	for _, item := range listed {
		if item.ID == created.ID {
			t.Fatal("archived formula metric must leave the default listing")
		}
		if item.ArchivedAt != nil {
			t.Fatal("active formula metric must not carry an archive marker")
		}
	}
	if len(listed) != 1 || listed[0].ID != keep.ID {
		t.Fatalf("listing must retain only the active metric: %+v", listed)
	}
	archivedList, e := f.s.ListFormulaMetrics(ctx, f.owner, f.project, "development", "", 100, true)
	if e != nil {
		t.Fatal(e)
	}
	if len(archivedList) != 1 || archivedList[0].ID != created.ID || archivedList[0].ArchivedAt == nil {
		t.Fatalf("archived listing must return only the archived metric with marker: %+v", archivedList)
	}

	// Reads by ID, draft and immutable version stay available with marker.
	got, e := f.s.GetFormulaMetric(ctx, f.owner, f.project, created.ID)
	if e != nil || got.ArchivedAt == nil {
		t.Fatalf("archived metric must remain readable by ID: %+v %v", got, e)
	}
	gotDraft, e := f.s.GetFormulaMetricDraft(ctx, f.owner, f.project, created.ID)
	if e != nil || gotDraft.ArchivedAt == nil {
		t.Fatalf("archived draft must remain readable with marker: %+v %v", gotDraft, e)
	}
	if _, e = f.s.GetFormulaMetricVersion(ctx, f.owner, f.project, created.ID, version.Version); e != nil {
		t.Fatalf("immutable version must stay readable: %v", e)
	}

	// Every mutating path fails closed while archived.
	if _, e = f.s.SaveFormulaMetricDraft(ctx, f.owner, f.project, created.ID, created.DraftRevision, def); e == nil || faultPublicCode(e) != "FORMULA_METRIC_ARCHIVED" {
		t.Fatalf("draft save must reject archived metric: %v", e)
	}
	if _, e = f.s.FormulaMetricValidationSnapshot(ctx, f.owner, f.project, created.ID, reporting.FormulaMetricValidate{DraftRevision: created.DraftRevision, ExpectedPublishedVersion: version.Version}); e == nil || faultPublicCode(e) != "FORMULA_METRIC_ARCHIVED" {
		t.Fatalf("validation must reject archived metric: %v", e)
	}
	if _, e = f.s.PublishFormulaMetric(ctx, f.owner, f.project, created.ID, ids.New(), reporting.FormulaMetricPublish{DraftRevision: created.DraftRevision, ExpectedPublishedVersion: version.Version, ValidationHash: validation.ValidationHash}); e == nil || faultPublicCode(e) != "FORMULA_METRIC_ARCHIVED" {
		t.Fatalf("publish must reject archived metric: %v", e)
	}
	if _, e = f.s.EnqueueFormulaMetricVersion(ctx, f.owner, f.project, created.ID, version.Version, ids.New(), formulaExecuteInput()); e == nil || faultPublicCode(e) != "FORMULA_METRIC_ARCHIVED" {
		t.Fatalf("run enqueue must reject archived metric: %v", e)
	}

	// Unknown id resolves to not-found, not an archive conflict.
	if _, e = f.s.ArchiveFormulaMetric(ctx, f.owner, f.project, ids.New()); e == nil || faultPublicCode(e) != "FORMULA_METRIC_NOT_FOUND" {
		t.Fatalf("unknown metric must be not-found: %v", e)
	}

	// Restoring an active metric conflicts; restoring the archived one clears markers.
	if _, e = f.s.UnarchiveFormulaMetric(ctx, f.owner, f.project, keep.ID); e == nil || faultPublicCode(e) != "FORMULA_METRIC_NOT_ARCHIVED" {
		t.Fatalf("unarchiving an active metric must conflict: %v", e)
	}
	restored, e := f.s.UnarchiveFormulaMetric(ctx, f.owner, f.project, created.ID)
	if e != nil {
		t.Fatal(e)
	}
	if restored.ArchivedAt != nil || restored.ArchivedBy != "" {
		t.Fatalf("restore must clear archive markers: %+v", restored)
	}
	listedAgain, e := f.s.ListFormulaMetrics(ctx, f.owner, f.project, "development", "", 100, false)
	if e != nil || len(listedAgain) != 2 {
		t.Fatalf("restored metric must rejoin the listing: %d %v", len(listedAgain), e)
	}
	restoredArchive, e := f.s.ListFormulaMetrics(ctx, f.owner, f.project, "development", "", 100, true)
	if e != nil || len(restoredArchive) != 0 {
		t.Fatalf("restored metric must leave the archived stream: %d %v", len(restoredArchive), e)
	}

	// Both lifecycle transitions are audited after the existing create/publish entries.
	var actions []string
	rows, e := f.s.Pool.Query(ctx, `SELECT action FROM audit_entries WHERE resource_key=$1 AND action IN ('formula_metric.archived','formula_metric.unarchived') ORDER BY id`, created.ID)
	if e != nil {
		t.Fatal(e)
	}
	defer rows.Close()
	for rows.Next() {
		var action string
		if e = rows.Scan(&action); e != nil {
			t.Fatal(e)
		}
		actions = append(actions, action)
	}
	if len(actions) != 2 || actions[0] != "formula_metric.archived" || actions[1] != "formula_metric.unarchived" {
		t.Fatalf("lifecycle audit trail wrong: %v", actions)
	}
}
