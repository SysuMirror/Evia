package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/howtomakeaname/Evia/backend/internal/access"
	"github.com/howtomakeaname/Evia/backend/internal/platform/fault"
	"github.com/howtomakeaname/Evia/backend/internal/platform/ids"
	"github.com/howtomakeaname/Evia/backend/internal/reports"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

var _ reports.Store = (*Store)(nil)

func reportMissing() error {
	return fault.New(fault.NotFound, "REPORT_NOT_FOUND", "报告不存在或不可见")
}

func reportRevisionConflict() error {
	return fault.New(fault.Conflict, "REPORT_REVISION_CONFLICT", "报告修订已变化，请重新加载")
}

// reportTx enforces human-only workspace governance: machines are rejected,
// outsiders see an invisible project, and only owners/editors may write.
func reportTx(ctx context.Context, tx pgx.Tx, actor, project string, write bool) error {
	if _, machine := access.IdentityFromContext(ctx); machine {
		return machineDenied()
	}
	var role string
	e := tx.QueryRow(ctx, `SELECT m.role FROM project_members m JOIN accounts a ON a.id=m.user_id
 WHERE m.project_id=$1 AND m.user_id=$2 AND NOT a.disabled FOR SHARE OF m,a`, project, actor).Scan(&role)
	if errors.Is(e, pgx.ErrNoRows) {
		return projectInvisible()
	}
	if e != nil {
		return e
	}
	if write && role != "owner" && role != "editor" {
		return fault.New(fault.Forbidden, "REPORT_FORBIDDEN", "只有项目所有者或编辑者可以管理报告")
	}
	return setAuditCredential(ctx, tx, "")
}

type reportRow struct {
	id, environment, title, description, createdAt, updatedAt string
	revision                                                  int64
}

const reportColumns = `report_id::text,environment,title,description,revision,created_at,updated_at`

func scanReportMeta(row pgx.Row) (reportRow, error) {
	var r reportRow
	var created, updated time.Time
	if e := row.Scan(&r.id, &r.environment, &r.title, &r.description, &r.revision, &created, &updated); e != nil {
		return reportRow{}, e
	}
	r.createdAt = created.UTC().Format(time.RFC3339Nano)
	r.updatedAt = updated.UTC().Format(time.RFC3339Nano)
	return r, nil
}

func loadSectionsTx(ctx context.Context, tx pgx.Tx, project, reportID string) ([]reports.Section, error) {
	rows, e := tx.Query(ctx, `SELECT section_id::text,reading_order,section_title,analysis_id::text,analysis_version
 FROM report_sections WHERE project_id=$1 AND report_id=$2 ORDER BY reading_order`, project, reportID)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := make([]reports.Section, 0)
	for rows.Next() {
		var section reports.Section
		if e = rows.Scan(&section.SectionID, &section.ReadingOrder, &section.SectionTitle, &section.AnalysisID, &section.AnalysisVersion); e != nil {
			return nil, e
		}
		out = append(out, section)
	}
	return out, rows.Err()
}

func (s *Store) readReportTx(ctx context.Context, tx pgx.Tx, project, id string) (reports.Report, error) {
	meta, e := scanReportMeta(tx.QueryRow(ctx, `SELECT `+reportColumns+` FROM reports
 WHERE project_id=$1 AND report_id=$2`, project, id))
	if errors.Is(e, pgx.ErrNoRows) {
		return reports.Report{}, reportMissing()
	}
	if e != nil {
		return reports.Report{}, e
	}
	sections, e := loadSectionsTx(ctx, tx, project, id)
	if e != nil {
		return reports.Report{}, e
	}
	return reports.Report{
		ID:          meta.id,
		ProjectID:   project,
		Environment: meta.environment,
		Revision:    meta.revision,
		Definition:  reports.Definition{Title: meta.title, Description: meta.description, Sections: sections},
		CreatedAt:   meta.createdAt,
		UpdatedAt:   meta.updatedAt,
	}, nil
}

func replaceSectionsTx(ctx context.Context, tx pgx.Tx, project, reportID, environment string, sections []reports.Section) error {
	if _, e := tx.Exec(ctx, `DELETE FROM report_sections WHERE project_id=$1 AND report_id=$2`, project, reportID); e != nil {
		return e
	}
	for _, section := range sections {
		sectionID := section.SectionID
		if sectionID == "" {
			sectionID = ids.New()
		}
		// The foreign key to saved_analysis_versions rejects drafts or missing
		// analyses inside this same transaction with a 23503 violation.
		if _, e := tx.Exec(ctx, `INSERT INTO report_sections(project_id,report_id,environment,section_id,reading_order,section_title,analysis_id,analysis_version)
 VALUES($1,$2,$3,$4,$5,$6,$7,$8)`, project, reportID, environment, sectionID, section.ReadingOrder, section.SectionTitle, section.AnalysisID, section.AnalysisVersion); e != nil {
			var pgErr *pgconn.PgError
			if errors.As(e, &pgErr) && pgErr.Code == "23503" {
				return fault.New(fault.Invalid, "REPORT_REFERENCE_INVALID", "报告引用了不存在或未发布的分析版本")
			}
			if errors.As(e, &pgErr) && pgErr.Code == "23505" {
				return fault.New(fault.Invalid, "REPORT_SECTION_CONFLICT", "章节顺序或标识冲突")
			}
			return e
		}
	}
	return nil
}

func (s *Store) Create(ctx context.Context, in reports.Input) (reports.Report, error) {
	tx, e := s.Pool.Begin(ctx)
	if e != nil {
		return reports.Report{}, e
	}
	defer tx.Rollback(context.Background())
	if e = reportTx(ctx, tx, in.ActorID, in.ProjectID, true); e != nil {
		return reports.Report{}, e
	}
	if _, e = tx.Exec(ctx, `SELECT id FROM projects WHERE id=$1 FOR UPDATE`, in.ProjectID); e != nil {
		return reports.Report{}, e
	}
	var priorHash string
	var response []byte
	e = tx.QueryRow(ctx, `SELECT request_hash,response FROM report_mutations
 WHERE project_id=$1 AND actor_id=$2 AND idempotency_key=$3`, in.ProjectID, in.ActorID, in.IdempotencyKey).Scan(&priorHash, &response)
	if e == nil {
		if priorHash != in.RequestHash {
			return reports.Report{}, fault.New(fault.Conflict, "IDEMPOTENCY_CONFLICT", "幂等键已用于不同请求")
		}
		var replay reports.Report
		if e = json.Unmarshal(response, &replay); e != nil {
			return reports.Report{}, e
		}
		return replay, tx.Commit(ctx)
	}
	if !errors.Is(e, pgx.ErrNoRows) {
		return reports.Report{}, e
	}

	reportID := ids.New()
	definition, e := json.Marshal(in.Definition)
	if e != nil {
		return reports.Report{}, e
	}
	if _, e = tx.Exec(ctx, `INSERT INTO reports(project_id,report_id,environment,title,description,definition)
 VALUES($1,$2,$3,$4,$5,$6)`, in.ProjectID, reportID, in.Environment, in.Definition.Title, in.Definition.Description, definition); e != nil {
		return reports.Report{}, e
	}
	if e = replaceSectionsTx(ctx, tx, in.ProjectID, reportID, in.Environment, in.Definition.Sections); e != nil {
		return reports.Report{}, e
	}
	if _, e = tx.Exec(ctx, `INSERT INTO audit_entries(project_id,actor_id,action,resource_key)
 VALUES($1,$2,'report.created',$3)`, in.ProjectID, in.ActorID, reportID); e != nil {
		return reports.Report{}, e
	}
	out, e := s.readReportTx(ctx, tx, in.ProjectID, reportID)
	if e != nil {
		return reports.Report{}, e
	}
	if response, e = json.Marshal(out); e != nil {
		return reports.Report{}, e
	}
	if _, e = tx.Exec(ctx, `INSERT INTO report_mutations(project_id,actor_id,idempotency_key,request_hash,response)
 VALUES($1,$2,$3,$4,$5)`, in.ProjectID, in.ActorID, in.IdempotencyKey, in.RequestHash, response); e != nil {
		return reports.Report{}, e
	}
	return out, tx.Commit(ctx)
}

func (s *Store) Update(ctx context.Context, in reports.Input, expectedRevision int64) (reports.Report, error) {
	tx, e := s.Pool.Begin(ctx)
	if e != nil {
		return reports.Report{}, e
	}
	defer tx.Rollback(context.Background())
	if e = reportTx(ctx, tx, in.ActorID, in.ProjectID, true); e != nil {
		return reports.Report{}, e
	}
	if _, e = tx.Exec(ctx, `SELECT id FROM projects WHERE id=$1 FOR UPDATE`, in.ProjectID); e != nil {
		return reports.Report{}, e
	}
	// Updates share the create ledger: a retried PATCH returns the originally
	// accepted snapshot without a second revision bump, and a reused key with a
	// different body conflicts before any row is touched.
	var priorHash string
	var response []byte
	e = tx.QueryRow(ctx, `SELECT request_hash,response FROM report_mutations
 WHERE project_id=$1 AND actor_id=$2 AND idempotency_key=$3`, in.ProjectID, in.ActorID, in.IdempotencyKey).Scan(&priorHash, &response)
	if e == nil {
		if priorHash != in.RequestHash {
			return reports.Report{}, fault.New(fault.Conflict, "IDEMPOTENCY_CONFLICT", "幂等键已用于不同请求")
		}
		var replay reports.Report
		if e = json.Unmarshal(response, &replay); e != nil {
			return reports.Report{}, e
		}
		return replay, tx.Commit(ctx)
	}
	if !errors.Is(e, pgx.ErrNoRows) {
		return reports.Report{}, e
	}
	reportID := in.ReportID
	if reportID == "" {
		return reports.Report{}, fault.New(fault.Invalid, "INVALID_REPORT_ID", "报告标识格式不正确")
	}
	var currentRevision int64
	e = tx.QueryRow(ctx, `SELECT revision FROM reports WHERE project_id=$1 AND report_id=$2 FOR UPDATE`, in.ProjectID, reportID).Scan(&currentRevision)
	if errors.Is(e, pgx.ErrNoRows) {
		return reports.Report{}, reportMissing()
	}
	if e != nil {
		return reports.Report{}, e
	}
	if currentRevision != expectedRevision {
		return reports.Report{}, reportRevisionConflict()
	}
	definition, e := json.Marshal(in.Definition)
	if e != nil {
		return reports.Report{}, e
	}
	tag, e := tx.Exec(ctx, `UPDATE reports SET title=$3,description=$4,definition=$5,revision=revision+1,updated_at=clock_timestamp()
 WHERE project_id=$1 AND report_id=$2`, in.ProjectID, reportID, in.Definition.Title, in.Definition.Description, definition)
	if e != nil {
		return reports.Report{}, e
	}
	if tag.RowsAffected() != 1 {
		return reports.Report{}, reportMissing()
	}
	if e = replaceSectionsTx(ctx, tx, in.ProjectID, reportID, in.Environment, in.Definition.Sections); e != nil {
		return reports.Report{}, e
	}
	if _, e = tx.Exec(ctx, `INSERT INTO audit_entries(project_id,actor_id,action,resource_key)
 VALUES($1,$2,'report.updated',$3)`, in.ProjectID, in.ActorID, reportID); e != nil {
		return reports.Report{}, e
	}
	out, e := s.readReportTx(ctx, tx, in.ProjectID, reportID)
	if e != nil {
		return reports.Report{}, e
	}
	if response, e = json.Marshal(out); e != nil {
		return reports.Report{}, e
	}
	if _, e = tx.Exec(ctx, `INSERT INTO report_mutations(project_id,actor_id,idempotency_key,request_hash,response)
 VALUES($1,$2,$3,$4,$5)`, in.ProjectID, in.ActorID, in.IdempotencyKey, in.RequestHash, response); e != nil {
		return reports.Report{}, e
	}
	return out, tx.Commit(ctx)
}

func (s *Store) Get(ctx context.Context, actor, project, id string) (reports.Report, error) {
	tx, e := s.Pool.Begin(ctx)
	if e != nil {
		return reports.Report{}, e
	}
	defer tx.Rollback(context.Background())
	if e = reportTx(ctx, tx, actor, project, false); e != nil {
		return reports.Report{}, e
	}
	out, e := s.readReportTx(ctx, tx, project, id)
	if e != nil {
		return reports.Report{}, e
	}
	return out, tx.Commit(ctx)
}

func (s *Store) List(ctx context.Context, actor, project, environment, after string, limit int, search string) ([]reports.Report, error) {
	tx, e := s.Pool.Begin(ctx)
	if e != nil {
		return nil, e
	}
	defer tx.Rollback(context.Background())
	if e = reportTx(ctx, tx, actor, project, false); e != nil {
		return nil, e
	}
	// search is an escaped LIKE literal passed only as a bound parameter;
	// $5='' disables the title substring filter.
	rows, e := tx.Query(ctx, `SELECT `+reportColumns+` FROM reports
 WHERE project_id=$1 AND environment=$2 AND ($5='' OR title ILIKE '%' || $5 || '%' ESCAPE '\') AND ($3='' OR report_id>NULLIF($3,'')::uuid)
 ORDER BY report_id LIMIT $4`, project, environment, after, limit, search)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	metas := make([]reportRow, 0)
	for rows.Next() {
		meta, err := scanReportMeta(rows)
		if err != nil {
			return nil, err
		}
		metas = append(metas, meta)
	}
	if e = rows.Err(); e != nil {
		return nil, e
	}
	out := make([]reports.Report, 0, len(metas))
	for _, meta := range metas {
		sections, err := loadSectionsTx(ctx, tx, project, meta.id)
		if err != nil {
			return nil, err
		}
		out = append(out, reports.Report{
			ID:          meta.id,
			ProjectID:   project,
			Environment: meta.environment,
			Revision:    meta.revision,
			Definition:  reports.Definition{Title: meta.title, Description: meta.description, Sections: sections},
			CreatedAt:   meta.createdAt,
			UpdatedAt:   meta.updatedAt,
		})
	}
	return out, tx.Commit(ctx)
}

func (s *Store) Delete(ctx context.Context, actor, project, id string) error {
	tx, e := s.Pool.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(context.Background())
	if e = reportTx(ctx, tx, actor, project, true); e != nil {
		return e
	}
	tag, e := tx.Exec(ctx, `DELETE FROM reports WHERE project_id=$1 AND report_id=$2`, project, id)
	if e != nil {
		return e
	}
	if tag.RowsAffected() != 1 {
		return reportMissing()
	}
	if _, e = tx.Exec(ctx, `INSERT INTO audit_entries(project_id,actor_id,action,resource_key)
 VALUES($1,$2,'report.deleted',$3)`, project, actor, id); e != nil {
		return e
	}
	return tx.Commit(ctx)
}
