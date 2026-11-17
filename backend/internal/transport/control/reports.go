package control

import (
	"context"
	"net/http"

	"github.com/howtomakeaname/Evia/backend/internal/analysis"
	"github.com/howtomakeaname/Evia/backend/internal/gen/wire"
	"github.com/howtomakeaname/Evia/backend/internal/platform/canonical"
	"github.com/howtomakeaname/Evia/backend/internal/platform/fault"
	"github.com/howtomakeaname/Evia/backend/internal/reports"
)

// ReportService is the domain port the reports HTTP surface depends on.
type ReportService interface {
	Create(ctx context.Context, in reports.Input, expectedRevision int64) (reports.Report, error)
	List(ctx context.Context, actor, project, environment, after string, limit int, search string) ([]reports.Report, error)
	Get(ctx context.Context, actor, project, id string) (reports.Report, error)
	Update(ctx context.Context, in reports.Input, expectedRevision int64) (reports.Report, error)
	Delete(ctx context.Context, actor, project, id string) error
}

// ReportsAPI shares management session authentication. Reports are human-only
// governance documents: the service and its storage transaction reject
// machines and non-owner/editor members, so routes use projectAuth rather than
// a machine permission grant.
type ReportsAPI struct {
	API     *API
	Service ReportService
}

func (a *ReportsAPI) Register(mux *http.ServeMux) {
	if a.Service == nil {
		return
	}
	const base = "/api/v1/projects/{project_id}/reports"
	mux.HandleFunc("GET "+base, a.API.projectAuth(false, a.list))
	mux.HandleFunc("POST "+base, a.API.projectAuth(true, a.create))
	mux.HandleFunc("GET "+base+"/{report_id}", a.API.projectAuth(false, a.get))
	mux.HandleFunc("PATCH "+base+"/{report_id}", a.API.projectAuth(true, a.update))
	mux.HandleFunc("DELETE "+base+"/{report_id}", a.API.projectAuth(true, a.delete))
}

// reportRevision parses If-Match and enforces the operation-specific meaning:
// create expects the absent revision "0", updates expect a positive revision.
func reportRevision(r *http.Request, create bool) (int64, error) {
	revision, err := retentionRevision(r)
	if err != nil {
		return 0, err
	}
	if create && revision != reports.CreateRevision {
		return 0, fault.New(fault.Invalid, "REVISION_REQUIRED", "创建报告必须提供 If-Match \"0\"")
	}
	if !create && revision < 1 {
		return 0, fault.New(fault.Invalid, "REVISION_REQUIRED", "更新报告必须提供正修订版本")
	}
	return revision, nil
}

func reportSectionWire(section reports.Section) wire.ReportSection {
	return wire.ReportSection{
		SectionID:       section.SectionID,
		ReadingOrder:    int64(section.ReadingOrder),
		SectionTitle:    section.SectionTitle,
		AnalysisID:      section.AnalysisID,
		AnalysisVersion: section.AnalysisVersion,
	}
}

func reportWire(report reports.Report) wire.Report {
	sections := make([]wire.ReportSection, 0, len(report.Definition.Sections))
	for _, section := range report.Definition.Sections {
		sections = append(sections, reportSectionWire(section))
	}
	return wire.Report{
		ReportID:    report.ID,
		ProjectID:   report.ProjectID,
		Environment: report.Environment,
		Revision:    report.Revision,
		Definition: wire.ReportDefinition{
			Title:       report.Definition.Title,
			Description: report.Definition.Description,
			Sections:    sections,
		},
		CreatedAt: report.CreatedAt,
		UpdatedAt: report.UpdatedAt,
	}
}

func reportDefinitionInput(body wire.ReportInput) (reports.Definition, error) {
	sections := make([]reports.Section, 0, len(body.Definition.Sections))
	for _, section := range body.Definition.Sections {
		sections = append(sections, reports.Section{
			ReadingOrder:    int(section.ReadingOrder),
			SectionTitle:    section.SectionTitle,
			AnalysisID:      section.AnalysisID,
			AnalysisVersion: section.AnalysisVersion,
		})
	}
	return reports.Definition{
		Title:       body.Definition.Title,
		Description: body.Definition.Description,
		Sections:    sections,
	}, nil
}

func (a *ReportsAPI) list(w http.ResponseWriter, r *http.Request, actor authorizedActor) error {
	project, err := governanceProject(r)
	if err != nil {
		return err
	}
	q, err := governanceQuery(r, "environment", "limit", "cursor", "q")
	if err != nil {
		return err
	}
	environment := q.Get("environment")
	if environment == "" {
		return fault.New(fault.Invalid, "INVALID_QUERY", "查询参数不符合接口定义")
	}
	search, err := reports.NormalizeTitleSearch(q.Get("q"))
	if err != nil {
		return err
	}
	limit, err := governanceLimit(q)
	if err != nil {
		return err
	}
	// The cursor is bound to every list predicate, including the search term.
	scope := "reports:" + actor.ID + ":" + project + ":" + environment + ":" + search
	after, err := a.API.Cursor.Decode(scope, q.Get("cursor"))
	if err != nil || after != "" && !catalogUUID.MatchString(after) {
		return analysis.Invalid("INVALID_CURSOR", "分页游标已失效，请重新加载", "/cursor")
	}
	// Request one extra row to decide whether another page exists.
	items, err := a.Service.List(r.Context(), actor.ID, project, environment, after, limit+1, search)
	if err != nil {
		return err
	}
	out := wire.ReportList{Items: []wire.Report{}}
	if len(items) > limit {
		next := a.API.Cursor.Encode(scope, items[limit-1].ID)
		out.NextCursor = &next
		items = items[:limit]
	}
	for _, item := range items {
		out.Items = append(out.Items, reportWire(item))
	}
	return a.API.Boundary.JSON(w, http.StatusOK, "ReportList", out)
}

func (a *ReportsAPI) create(w http.ResponseWriter, r *http.Request, actor authorizedActor) error {
	project, err := governanceProject(r)
	if err != nil {
		return err
	}
	if _, err = governanceQuery(r); err != nil {
		return err
	}
	expected, err := reportRevision(r, true)
	if err != nil {
		return err
	}
	key, err := savedIdempotency(r)
	if err != nil {
		return err
	}
	var body wire.ReportInput
	if err = a.API.Boundary.Decode(w, r, "ReportInput", &body); err != nil {
		return err
	}
	hash, err := canonical.Hash(body)
	if err != nil {
		return err
	}
	definition, err := reportDefinitionInput(body)
	if err != nil {
		return err
	}
	created, err := a.Service.Create(r.Context(), reports.Input{
		ProjectID:      project,
		ActorID:        actor.ID,
		Environment:    body.Environment,
		Definition:     definition,
		IdempotencyKey: key,
		RequestHash:    hash,
	}, expected)
	if err != nil {
		return err
	}
	machineETag(w, created.Revision)
	return a.API.Boundary.JSON(w, http.StatusCreated, "Report", reportWire(created))
}

func (a *ReportsAPI) reportPath(r *http.Request) (string, error) {
	id := r.PathValue("report_id")
	if !catalogUUID.MatchString(id) {
		return "", fault.New(fault.Invalid, "INVALID_REPORT_ID", "报告标识格式不正确")
	}
	return id, nil
}

func (a *ReportsAPI) get(w http.ResponseWriter, r *http.Request, actor authorizedActor) error {
	project, err := governanceProject(r)
	if err != nil {
		return err
	}
	if _, err = governanceQuery(r); err != nil {
		return err
	}
	id, err := a.reportPath(r)
	if err != nil {
		return err
	}
	report, err := a.Service.Get(r.Context(), actor.ID, project, id)
	if err != nil {
		return err
	}
	machineETag(w, report.Revision)
	return a.API.Boundary.JSON(w, http.StatusOK, "Report", reportWire(report))
}

func (a *ReportsAPI) update(w http.ResponseWriter, r *http.Request, actor authorizedActor) error {
	project, err := governanceProject(r)
	if err != nil {
		return err
	}
	if _, err = governanceQuery(r); err != nil {
		return err
	}
	id, err := a.reportPath(r)
	if err != nil {
		return err
	}
	expected, err := reportRevision(r, false)
	if err != nil {
		return err
	}
	key, err := savedIdempotency(r)
	if err != nil {
		return err
	}
	var body wire.ReportInput
	if err = a.API.Boundary.Decode(w, r, "ReportInput", &body); err != nil {
		return err
	}
	hash, err := canonical.Hash(body)
	if err != nil {
		return err
	}
	definition, err := reportDefinitionInput(body)
	if err != nil {
		return err
	}
	updated, err := a.Service.Update(r.Context(), reports.Input{
		ProjectID:      project,
		ActorID:        actor.ID,
		ReportID:       id,
		Environment:    body.Environment,
		Definition:     definition,
		IdempotencyKey: key,
		RequestHash:    hash,
	}, expected)
	if err != nil {
		return err
	}
	machineETag(w, updated.Revision)
	return a.API.Boundary.JSON(w, http.StatusOK, "Report", reportWire(updated))
}

func (a *ReportsAPI) delete(w http.ResponseWriter, r *http.Request, actor authorizedActor) error {
	project, err := governanceProject(r)
	if err != nil {
		return err
	}
	if _, err = governanceQuery(r); err != nil {
		return err
	}
	id, err := a.reportPath(r)
	if err != nil {
		return err
	}
	if err = a.Service.Delete(r.Context(), actor.ID, project, id); err != nil {
		return err
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}
