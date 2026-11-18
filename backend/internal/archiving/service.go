package archiving

import (
	"context"
	"io"
	"regexp"
	"time"

	"github.com/howtomakeaname/Evia/backend/internal/platform/env"
	"github.com/howtomakeaname/Evia/backend/internal/platform/fault"
	"github.com/howtomakeaname/Evia/backend/internal/platform/ids"
)

var uuidPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

const invalidCode = "INVALID_ARCHIVE_SCOPE"

func invalid(message string) error {
	return fault.New(fault.Invalid, invalidCode, message)
}

// ValidateScope repeats the HTTP/storage contract at the domain boundary so a
// bypassed caller cannot persist an unreadable manifest.
func ValidateScope(scope Scope) error {
	if !uuidPattern.MatchString(scope.ProjectID) {
		return invalid("项目标识格式不正确")
	}
	switch scope.Environment {
	case env.Development, env.Staging, env.Production:
	default:
		return invalid("环境标识无效")
	}
	if scope.RetentionDays < 30 || scope.RetentionDays > 3650 {
		return invalid("保留窗口必须在 30 到 3650 天之间")
	}
	if scope.Cutoff.IsZero() {
		return invalid("归档截止时间不能为空")
	}
	return nil
}

// Service coordinates the archive-before-delete flow. Every failure after the
// pending manifest is created is made durable as a failed manifest and returned
// to the caller, which must treat it as a hard stop: retention deletion is only
// safe after a materialized manifest exists for the whole scope.
type Service struct {
	store     ManifestStore
	reader    FactReader
	artifacts ArtifactStore
	now       func() time.Time
}

func NewService(store ManifestStore, reader FactReader, artifacts ArtifactStore) *Service {
	return &Service{
		store:     store,
		reader:    reader,
		artifacts: artifacts,
		now:       func() time.Time { return time.Now().UTC() },
	}
}

// ArchiveScope materializes every fact older than scope.Cutoff. A zero-row
// scope returns no manifest and no error because there is nothing to preserve.
func (s *Service) ArchiveScope(ctx context.Context, scope Scope) (Manifest, error) {
	if err := ValidateScope(scope); err != nil {
		return Manifest{}, err
	}
	// Count before writing any intent row: empty scopes and over-cap scopes
	// leave the manifest table untouched while the sweep records its own audit.
	count, err := s.reader.CountExpired(ctx, scope.ProjectID, scope.Environment, scope.Cutoff)
	if err != nil {
		return Manifest{}, err
	}
	if count == 0 {
		return Manifest{}, nil
	}
	if count > MaxArchiveRows {
		return Manifest{}, fault.New(fault.Invalid, "ARCHIVE_SCOPE_TOO_LARGE", "归档作用域行数超过安全上限")
	}

	now := s.now()
	manifest := Manifest{
		ManifestID:    ids.New(),
		ProjectID:     scope.ProjectID,
		Environment:   scope.Environment,
		RetentionDays: scope.RetentionDays,
		Cutoff:        scope.Cutoff.UTC(),
		RowCount:      count,
		Status:        StatusPending,
		CreatedAt:     now,
	}
	if scope.SweepID != "" {
		if !uuidPattern.MatchString(scope.SweepID) {
			return Manifest{}, invalid("清理作业关联标识格式不正确")
		}
		manifest.SweepID = &scope.SweepID
	}
	if err = s.store.CreateManifest(ctx, manifest); err != nil {
		return Manifest{}, err
	}
	fail := func(code string, cause error) (Manifest, error) {
		completed := s.now()
		_ = s.store.MarkFailed(ctx, manifest.ManifestID, code, completed)
		manifest.Status = StatusFailed
		manifest.ErrorCode = &code
		manifest.CompletedAt = &completed
		return manifest, cause
	}

	var window FactWindow
	artifact, err := s.artifacts.WriteStream(ctx, manifest.ManifestID, func(writer io.Writer) (FactWindow, error) {
		observed, streamErr := s.reader.StreamExpired(ctx, writer, scope)
		if streamErr != nil {
			return FactWindow{}, streamErr
		}
		if observed.Rows != count {
			return FactWindow{}, fault.New(fault.Unavailable, "ARCHIVE_ROW_COUNT_DRIFT", "归档流式行数与计数不一致")
		}
		return observed, nil
	})
	if err != nil {
		return fail("ARCHIVE_ARTIFACT_WRITE_FAILED", err)
	}
	completed := s.now()
	if err = s.store.MarkSucceeded(ctx, manifest.ManifestID, artifact, window, count, completed); err != nil {
		return fail("ARCHIVE_MANIFEST_PERSIST_FAILED", err)
	}
	manifest.Status = StatusMaterialized
	manifest.ArtifactPath = &artifact.Path
	manifest.ArtifactSHA = &artifact.SHA256
	manifest.ArtifactBytes = &artifact.Bytes
	manifest.WindowStart = &window.WindowStart
	manifest.WindowEnd = &window.WindowEnd
	manifest.CompletedAt = &completed
	return manifest, nil
}
