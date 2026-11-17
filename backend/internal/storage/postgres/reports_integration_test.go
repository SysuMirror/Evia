//go:build integration

package postgres

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/howtomakeaname/Evia/backend/internal/access"
	"github.com/howtomakeaname/Evia/backend/internal/platform/ids"
	"github.com/howtomakeaname/Evia/backend/internal/reporting"
	"github.com/howtomakeaname/Evia/backend/internal/reports"
)

// reportFixture publishes n distinct saved analyses in development and returns
// their immutable versions. Reports can only pin published versions, so every
// report test starts from this real publication chain.
func reportFixture(t *testing.T, n int) (catalogFixture, []reporting.SavedAnalysisVersion) {
	t.Helper()
	f, first, _ := savedRunFixture(t)
	versions := []reporting.SavedAnalysisVersion{first}
	ctx := context.Background()
	for len(versions) < n {
		definition := savedRunDefinition()
		definition.Title = fmt.Sprintf("Lesson total %d", len(versions)+1)
		draft, e := f.s.CreateSavedAnalysis(ctx, f.owner, f.project, fmt.Sprintf("report-source-%d", len(versions)+1), reporting.SavedAnalysisCreate{Environment: "development", Definition: definition})
		if e != nil {
			t.Fatal(e)
		}
		versions = append(versions, savedRunPublish(t, f, draft))
	}
	return f, versions
}

func reportRequestHash(definition reports.Definition) string {
	body, e := json.Marshal(definition)
	if e != nil {
		panic(e)
	}
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:])
}

func reportInput(f catalogFixture, environment string, versions []reporting.SavedAnalysisVersion) reports.Input {
	sections := make([]reports.Section, 0, len(versions))
	for i, version := range versions {
		sections = append(sections, reports.Section{
			ReadingOrder:    i + 1,
			SectionTitle:    fmt.Sprintf("第 %d 章", i+1),
			AnalysisID:      version.AnalysisID,
			AnalysisVersion: version.Version,
		})
	}
	in := reports.Input{
		ProjectID:      f.project,
		ActorID:        f.owner,
		Environment:    environment,
		IdempotencyKey: ids.New(),
		Definition: reports.Definition{
			Title:       "周报：学习行为汇总",
			Description: "合成报告",
			Sections:    sections,
		},
	}
	in.RequestHash = reportRequestHash(in.Definition)
	return in
}

func reportAuditCount(t *testing.T, f catalogFixture, action string) int {
	t.Helper()
	var count int
	if e := f.s.Pool.QueryRow(context.Background(), `SELECT count(*) FROM audit_entries
 WHERE project_id=$1 AND action=$2`, f.project, action).Scan(&count); e != nil {
		t.Fatal(e)
	}
	return count
}

func TestReportCreatePinsPublishedVersionsAndRejectsBadReferences(t *testing.T) {
	f, versions := reportFixture(t, 2)
	ctx := context.Background()
	svc := reports.NewService(f.s)
	in := reportInput(f, "development", versions)

	created, e := svc.Create(ctx, in, reports.CreateRevision)
	if e != nil || created.Revision != 1 || created.Environment != "development" || len(created.Definition.Sections) != 2 {
		t.Fatal(created, e)
	}
	if created.Definition.Sections[0].SectionID == "" || created.Definition.Sections[0].ReadingOrder != 1 ||
		created.Definition.Sections[1].AnalysisID != versions[1].AnalysisID || created.CreatedAt == "" || created.UpdatedAt == "" {
		t.Fatal("persisted report lost section identity or timestamps", created)
	}

	// Members can read the compilation even though they cannot write one.
	visible, e := svc.Get(ctx, f.viewer, f.project, created.ID)
	if e != nil || visible.ID != created.ID || len(visible.Definition.Sections) != 2 {
		t.Fatal(visible, e)
	}

	// An identical replay returns the original snapshot and writes no new row.
	replay, e := svc.Create(ctx, in, reports.CreateRevision)
	if e != nil || replay.ID != created.ID || replay.Revision != 1 {
		t.Fatal("idempotent replay changed the report", replay, e)
	}
	var reportRows, mutationRows int
	if e = f.s.Pool.QueryRow(ctx, `SELECT count(*) FROM reports WHERE project_id=$1`, f.project).Scan(&reportRows); e != nil || reportRows != 1 {
		t.Fatalf("reports count=%d err=%v", reportRows, e)
	}
	if e = f.s.Pool.QueryRow(ctx, `SELECT count(*) FROM report_mutations WHERE project_id=$1`, f.project).Scan(&mutationRows); e != nil || mutationRows != 1 {
		t.Fatalf("mutation count=%d err=%v", mutationRows, e)
	}
	if count := reportAuditCount(t, f, "report.created"); count != 1 {
		t.Fatalf("created audit count=%d, want 1", count)
	}

	// The same key with a different body is a conflict rather than a silent overwrite.
	conflict := in
	conflict.Definition.Description = "不同请求体"
	conflict.RequestHash = reportRequestHash(conflict.Definition)
	_, e = svc.Create(ctx, conflict, reports.CreateRevision)
	faultCode(t, e, "IDEMPOTENCY_CONFLICT")

	// Drafts and missing analyses cannot be pinned: the relational foreign key
	// converts the 23503 violation into a domain fault inside the transaction.
	missing := reportInput(f, "development", []reporting.SavedAnalysisVersion{{AnalysisID: ids.New(), Version: 1}})
	_, e = svc.Create(ctx, missing, reports.CreateRevision)
	faultCode(t, e, "REPORT_REFERENCE_INVALID")

	// The composite foreign key also carries the environment, so a development
	// version cannot be smuggled into a staging report.
	wrongEnv := reportInput(f, "staging", versions)
	_, e = svc.Create(ctx, wrongEnv, reports.CreateRevision)
	faultCode(t, e, "REPORT_REFERENCE_INVALID")

	// Domain validation still rejects structurally impossible compilations.
	badShape := reportInput(f, "development", versions)
	badShape.IdempotencyKey = ids.New()
	badShape.Definition.Sections[1].ReadingOrder = badShape.Definition.Sections[0].ReadingOrder
	badShape.RequestHash = reportRequestHash(badShape.Definition)
	_, e = svc.Create(ctx, badShape, reports.CreateRevision)
	faultCode(t, e, "INVALID_REPORT_INPUT")
}

func TestReportRevisionLifecycleSectionReplacementPaginationAndDelete(t *testing.T) {
	f, versions := reportFixture(t, 2)
	ctx := context.Background()
	svc := reports.NewService(f.s)
	first, e := svc.Create(ctx, reportInput(f, "development", versions[:1]), reports.CreateRevision)
	if e != nil {
		t.Fatal(first, e)
	}
	originalSection := first.Definition.Sections[0].SectionID

	// Replacing sections deletes the old rows and inserts the new reading order
	// while bumping the optimistic revision.
	update := reportInput(f, "development", versions)
	update.ReportID = first.ID
	update.IdempotencyKey = ids.New()
	updated, e := svc.Update(ctx, update, first.Revision)
	if e != nil || updated.Revision != 2 || len(updated.Definition.Sections) != 2 {
		t.Fatal(updated, e)
	}
	if updated.Definition.Sections[0].SectionID == originalSection {
		t.Fatal("section replacement kept the stale section row")
	}
	if count := reportAuditCount(t, f, "report.updated"); count != 1 {
		t.Fatalf("updated audit count=%d, want 1", count)
	}

	// A stale If-Match revision fails closed without touching the document.
	stale := reportInput(f, "development", versions[:1])
	stale.ReportID = first.ID
	stale.IdempotencyKey = ids.New()
	_, e = svc.Update(ctx, stale, first.Revision)
	faultCode(t, e, "REPORT_REVISION_CONFLICT")

	// A retried PATCH with the same key replays the accepted snapshot and must
	// not bump the revision a second time even though If-Match is now stale.
	replayed, e := svc.Update(ctx, update, first.Revision)
	if e != nil || replayed.Revision != 2 || replayed.ID != updated.ID {
		t.Fatal("update replay changed state", replayed, e)
	}
	updateConflict := update
	updateConflict.Definition.Description = "第二次编辑"
	updateConflict.RequestHash = reportRequestHash(updateConflict.Definition)
	_, e = svc.Update(ctx, updateConflict, updated.Revision)
	faultCode(t, e, "IDEMPOTENCY_CONFLICT")
	var mutations int
	if e = f.s.Pool.QueryRow(ctx, `SELECT count(*) FROM report_mutations WHERE project_id=$1`, f.project).Scan(&mutations); e != nil || mutations != 2 {
		t.Fatalf("mutation count=%d err=%v (expect 2: create + update)", mutations, e)
	}

	missingID := ids.New()
	missing := reportInput(f, "development", versions[:1])
	missing.ReportID = missingID
	missing.IdempotencyKey = ids.New()
	_, e = svc.Update(ctx, missing, 1)
	faultCode(t, e, "REPORT_NOT_FOUND")
	_, e = svc.Get(ctx, f.owner, f.project, missingID)
	faultCode(t, e, "REPORT_NOT_FOUND")

	// Pagination is keyset by report id and stays inside one environment.
	second, e := svc.Create(ctx, reportInput(f, "development", versions[:1]), reports.CreateRevision)
	if e != nil {
		t.Fatal(second, e)
	}
	third, e := svc.Create(ctx, reportInput(f, "development", versions[:1]), reports.CreateRevision)
	if e != nil {
		t.Fatal(third, e)
	}
	page, e := svc.List(ctx, f.viewer, f.project, "development", "", 2, "")
	if e != nil || len(page) != 2 {
		t.Fatal(page, e)
	}
	rest, e := svc.List(ctx, f.viewer, f.project, "development", page[1].ID, 2, "")
	if e != nil || len(rest) != 1 {
		t.Fatal(rest, e)
	}
	seen := map[string]bool{page[0].ID: true, page[1].ID: true, rest[0].ID: true}
	for _, wanted := range []string{first.ID, second.ID, third.ID} {
		if !seen[wanted] {
			t.Fatalf("keyset page missed report %s: %v", wanted, seen)
		}
	}

	// Deleting the report cascades to its sections and is audited.
	if e = svc.Delete(ctx, f.owner, f.project, second.ID); e != nil {
		t.Fatal(e)
	}
	_, e = svc.Get(ctx, f.owner, f.project, second.ID)
	faultCode(t, e, "REPORT_NOT_FOUND")
	var orphanSections int
	if e = f.s.Pool.QueryRow(ctx, `SELECT count(*) FROM report_sections WHERE project_id=$1 AND report_id=$2`, f.project, second.ID).Scan(&orphanSections); e != nil || orphanSections != 0 {
		t.Fatalf("orphan sections=%d err=%v", orphanSections, e)
	}
	if e = svc.Delete(ctx, f.owner, f.project, second.ID); e == nil {
		t.Fatal("deleting an absent report must fail")
	}
	if count := reportAuditCount(t, f, "report.deleted"); count != 1 {
		t.Fatalf("deleted audit count=%d, want 1", count)
	}
}

func TestReportAuthorityIsHumanOnlyAndProjectScoped(t *testing.T) {
	f, versions := reportFixture(t, 1)
	ctx := context.Background()
	svc := reports.NewService(f.s)
	in := reportInput(f, "development", versions)

	// Viewers retain read access but every write path is owner/editor only.
	if _, e := svc.List(ctx, f.viewer, f.project, "development", "", 10, ""); e != nil {
		t.Fatal(e)
	}
	viewerInput := in
	viewerInput.ActorID = f.viewer
	viewerInput.IdempotencyKey = ids.New()
	viewerInput.RequestHash = reportRequestHash(viewerInput.Definition)
	_, e := svc.Create(ctx, viewerInput, reports.CreateRevision)
	faultCode(t, e, "REPORT_FORBIDDEN")

	created, e := svc.Create(ctx, in, reports.CreateRevision)
	if e != nil {
		t.Fatal(created, e)
	}
	e = svc.Delete(ctx, f.viewer, f.project, created.ID)
	faultCode(t, e, "REPORT_FORBIDDEN")
	viewerUpdate := in
	viewerUpdate.ReportID = created.ID
	viewerUpdate.ActorID = f.viewer
	viewerUpdate.IdempotencyKey = ids.New()
	viewerUpdate.RequestHash = reportRequestHash(viewerUpdate.Definition)
	_, e = svc.Update(ctx, viewerUpdate, created.Revision)
	faultCode(t, e, "REPORT_FORBIDDEN")

	// Outsiders cannot distinguish a hidden project from a missing one.
	_, e = svc.Get(ctx, f.outsider, f.project, created.ID)
	faultCode(t, e, "PROJECT_NOT_FOUND")
	outsiderInput := in
	outsiderInput.ActorID = f.outsider
	outsiderInput.IdempotencyKey = ids.New()
	outsiderInput.RequestHash = reportRequestHash(outsiderInput.Definition)
	_, e = svc.Create(ctx, outsiderInput, reports.CreateRevision)
	faultCode(t, e, "PROJECT_NOT_FOUND")

	// Machine credentials are rejected at both the service boundary and the
	// storage transaction, so a future transport change cannot bypass the rule.
	machines := access.NewMachines(f.s)
	issued, e := machines.CreatePrincipal(ctx, f.owner, f.project, "report-machine", machineInput())
	if e != nil {
		t.Fatal(e)
	}
	identity, e := machines.AuthenticateMachine(ctx, issued.SecretOnce)
	if e != nil {
		t.Fatal(e)
	}
	mctx := access.WithIdentity(ctx, identity)
	_, e = svc.Create(mctx, in, reports.CreateRevision)
	faultCode(t, e, "MACHINE_FORBIDDEN")
	_, e = f.s.Create(mctx, in)
	faultCode(t, e, "MACHINE_FORBIDDEN")
	_, e = svc.Get(mctx, f.owner, f.project, created.ID)
	faultCode(t, e, "MACHINE_FORBIDDEN")

	// Rejected writes leave no audit trail and no partial rows.
	if count := reportAuditCount(t, f, "report.created"); count != 1 {
		t.Fatalf("created audit count=%d, want 1", count)
	}
	var sectionRows int
	if e = f.s.Pool.QueryRow(ctx, `SELECT count(*) FROM report_sections WHERE project_id=$1`, f.project).Scan(&sectionRows); e != nil || sectionRows != 1 {
		t.Fatalf("section rows=%d err=%v", sectionRows, e)
	}
}
