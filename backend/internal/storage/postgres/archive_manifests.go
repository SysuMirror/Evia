package postgres

import (
	"context"
	"errors"
	"regexp"
	"strconv"
	"time"

	"github.com/howtomakeaname/Evia/backend/internal/archiving"
	"github.com/howtomakeaname/Evia/backend/internal/platform/fault"
	"github.com/jackc/pgx/v5"
)

var _ archiving.ManifestStore = (*Store)(nil)

var archiveUUIDPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

const archiveManifestColumns = `id::text,project_id::text,environment,retention_days,cutoff_at,
 window_start_at,window_end_at,row_count,artifact_path,artifact_sha256,artifact_bytes,status,
 error_code,sweep_id::text,created_at,completed_at`

func archiveInvalid(message string) error {
	return fault.New(fault.Invalid, "INVALID_ARCHIVE_MANIFEST", message)
}

func scanArchiveManifest(row queryScanner) (archiving.Manifest, error) {
	var m archiving.Manifest
	var artifactPath, artifactSHA, errorCode, sweepID *string
	var artifactBytes *int64
	e := row.Scan(&m.ManifestID, &m.ProjectID, &m.Environment, &m.RetentionDays, &m.Cutoff,
		&m.WindowStart, &m.WindowEnd, &m.RowCount, &artifactPath, &artifactSHA, &artifactBytes,
		&m.Status, &errorCode, &sweepID, &m.CreatedAt, &m.CompletedAt)
	if e != nil {
		return m, e
	}
	m.ArtifactPath = artifactPath
	m.ArtifactSHA = artifactSHA
	m.ArtifactBytes = artifactBytes
	m.ErrorCode = errorCode
	m.SweepID = sweepID
	return m, nil
}

func (s *Store) CreateManifest(ctx context.Context, m archiving.Manifest) error {
	if !archiveUUIDPattern.MatchString(m.ManifestID) || !archiveUUIDPattern.MatchString(m.ProjectID) {
		return archiveInvalid("清单或项目标识格式不正确")
	}
	if m.Status != archiving.StatusPending {
		return archiveInvalid("新清单必须处于 pending 状态")
	}
	_, e := s.Pool.Exec(ctx, `INSERT INTO archive_manifests(
 id,project_id,environment,retention_days,cutoff_at,row_count,status,sweep_id,created_at)
 VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9)`,
		m.ManifestID, m.ProjectID, m.Environment, m.RetentionDays, m.Cutoff.UTC(),
		m.RowCount, m.Status, m.SweepID, m.CreatedAt.UTC())
	return e
}

func (s *Store) MarkSucceeded(ctx context.Context, manifestID string, artifact archiving.Artifact,
	window archiving.FactWindow, rowCount int64, completedAt time.Time) error {
	command, e := s.Pool.Exec(ctx, `UPDATE archive_manifests
 SET status='materialized',artifact_path=$2,artifact_sha256=$3,artifact_bytes=$4,
 row_count=$5,window_start_at=$6,window_end_at=$7,completed_at=$8,error_code=NULL
 WHERE id=$1 AND status='pending'`,
		manifestID, artifact.Path, artifact.SHA256, artifact.Bytes, rowCount,
		window.WindowStart.UTC(), window.WindowEnd.UTC(), completedAt.UTC())
	if e != nil {
		return e
	}
	if command.RowsAffected() != 1 {
		return fault.New(fault.Conflict, "ARCHIVE_MANIFEST_NOT_PENDING", "清单状态不允许完成操作")
	}
	return nil
}

func (s *Store) MarkFailed(ctx context.Context, manifestID, code string, completedAt time.Time) error {
	if len(code) < 1 || len(code) > 128 {
		return archiveInvalid("失败码无效")
	}
	// A failed terminal is idempotent: retries of the same failure must not
	// error when the row is already terminal.
	command, e := s.Pool.Exec(ctx, `UPDATE archive_manifests
 SET status='failed',error_code=$2,completed_at=$3,artifact_path=NULL
 WHERE id=$1 AND status='pending'`, manifestID, code, completedAt.UTC())
	if e != nil {
		return e
	}
	if command.RowsAffected() != 1 {
		var status string
		if scanErr := s.Pool.QueryRow(ctx, `SELECT status FROM archive_manifests WHERE id=$1`, manifestID).Scan(&status); scanErr != nil {
			return scanErr
		}
		if status != archiving.StatusFailed {
			return fault.New(fault.Conflict, "ARCHIVE_MANIFEST_NOT_PENDING", "清单状态不允许失败操作")
		}
	}
	return nil
}

func (s *Store) ListManifests(ctx context.Context, actor, project string, filter archiving.ListFilter) (archiving.Page, error) {
	if filter.Limit < 1 || filter.Limit > 100 {
		return archiving.Page{}, archiveInvalid("分页大小超出范围")
	}
	if filter.After != "" && !archiveUUIDPattern.MatchString(filter.After) {
		return archiving.Page{}, archiveInvalid("分页游标已失效")
	}
	switch filter.Environment {
	case "", "development", "staging", "production":
	default:
		return archiving.Page{}, archiveInvalid("环境标识无效")
	}
	tx, e := s.Pool.Begin(ctx)
	if e != nil {
		return archiving.Page{}, e
	}
	defer tx.Rollback(context.Background())
	if e = memberOwnerTx(ctx, tx, actor, project); e != nil {
		return archiving.Page{}, e
	}
	query := `SELECT ` + archiveManifestColumns + ` FROM archive_manifests WHERE project_id=$1`
	args := []any{project}
	if filter.Environment != "" {
		query += ` AND environment=$2`
		args = append(args, filter.Environment)
	}
	if filter.After != "" {
		query += ` AND (created_at,id)>(SELECT created_at,id FROM archive_manifests WHERE project_id=$1 AND id=$` +
			strconv.Itoa(len(args)+1) + `)`
		args = append(args, filter.After)
	}
	query += ` ORDER BY created_at,id LIMIT $` + strconv.Itoa(len(args)+1)
	args = append(args, filter.Limit+1)
	rows, e := tx.Query(ctx, query, args...)
	if e != nil {
		return archiving.Page{}, e
	}
	defer rows.Close()
	out := archiving.Page{Items: []archiving.Manifest{}}
	for rows.Next() {
		manifest, scanErr := scanArchiveManifest(rows)
		if scanErr != nil {
			return archiving.Page{}, scanErr
		}
		out.Items = append(out.Items, manifest)
	}
	if e = rows.Err(); e != nil {
		return archiving.Page{}, e
	}
	if len(out.Items) > filter.Limit {
		out.After = out.Items[filter.Limit-1].ManifestID
		out.Items = out.Items[:filter.Limit]
	}
	return out, tx.Commit(ctx)
}

func (s *Store) GetManifest(ctx context.Context, actor, project, manifestID string) (archiving.Manifest, error) {
	if !archiveUUIDPattern.MatchString(manifestID) {
		return archiving.Manifest{}, archiveInvalid("清单标识格式不正确")
	}
	tx, e := s.Pool.Begin(ctx)
	if e != nil {
		return archiving.Manifest{}, e
	}
	defer tx.Rollback(context.Background())
	if e = memberOwnerTx(ctx, tx, actor, project); e != nil {
		return archiving.Manifest{}, e
	}
	manifest, e := scanArchiveManifest(tx.QueryRow(ctx,
		`SELECT `+archiveManifestColumns+` FROM archive_manifests WHERE id=$1 AND project_id=$2`,
		manifestID, project))
	if errors.Is(e, pgx.ErrNoRows) {
		return archiving.Manifest{}, fault.New(fault.NotFound, "ARCHIVE_MANIFEST_NOT_FOUND", "归档清单不存在或不可见")
	}
	if e != nil {
		return archiving.Manifest{}, e
	}
	return manifest, tx.Commit(ctx)
}
