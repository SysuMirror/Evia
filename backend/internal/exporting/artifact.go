package exporting

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/howtomakeaname/Evia/backend/internal/analysis"
	"github.com/howtomakeaname/Evia/backend/internal/platform/fault"
)

// ArtifactStore persists export outputs. The filesystem implementation is the
// first adapter; object storage can replace it behind the same interface.
type ArtifactStore interface {
	Write(ctx context.Context, jobID, format string, result analysis.Result) (Artifact, error)
	Open(ctx context.Context, artifactPath string) (io.ReadCloser, error)
}

// FileArtifactStore writes JSON artifacts under a bounded root directory.
// Job IDs are UUIDs, so the composed path can never escape the root.
type FileArtifactStore struct {
	root string
}

func NewFileArtifactStore(root string) (*FileArtifactStore, error) {
	if root == "" {
		return nil, errors.New("export artifact directory is required")
	}
	clean := filepath.Clean(root)
	if !filepath.IsAbs(clean) {
		return nil, errors.New("export artifact directory must be an absolute path")
	}
	if err := os.MkdirAll(clean, 0o700); err != nil {
		return nil, err
	}
	return &FileArtifactStore{root: clean}, nil
}

// artifactName keeps the on-disk name deterministic per job and independent of
// user-controlled input.
func artifactName(jobID, format string) (string, error) {
	if !uuidPattern.MatchString(jobID) {
		return "", errors.New("invalid export job id for artifact path")
	}
	normalized, err := NormalizeFormat(format)
	if err != nil {
		return "", err
	}
	return jobID + "." + normalized, nil
}

func (s *FileArtifactStore) Write(_ context.Context, jobID, format string, result analysis.Result) (Artifact, error) {
	if len(result.Rows) > MaxResultRows {
		return Artifact{}, fault.New(fault.Invalid, "EXPORT_RESULT_TOO_LARGE", "导出结果行数超过上限")
	}
	normalized, err := NormalizeFormat(format)
	if err != nil {
		return Artifact{}, err
	}
	name, err := artifactName(jobID, normalized)
	if err != nil {
		return Artifact{}, err
	}
	var payload []byte
	switch normalized {
	case FormatCSV:
		payload, err = renderCSV(result)
	case FormatNDJSON:
		payload, err = renderNDJSON(result)
	default:
		payload, err = json.MarshalIndent(result, "", "  ")
	}
	if err != nil {
		return Artifact{}, err
	}
	if len(payload) > MaxArtifactBytes {
		return Artifact{}, fault.New(fault.Invalid, "EXPORT_RESULT_TOO_LARGE", "导出产物字节数超过上限")
	}
	path := filepath.Join(s.root, name)
	// Defensive containment check even though the job ID is UUID-validated.
	if !strings.HasPrefix(path, s.root+string(os.PathSeparator)) {
		return Artifact{}, errors.New("artifact path escapes the export root")
	}
	if err = os.WriteFile(path, payload, 0o600); err != nil {
		return Artifact{}, err
	}
	sum := sha256.Sum256(payload)
	return Artifact{
		Path:   path,
		SHA256: hex.EncodeToString(sum[:]),
		Bytes:  int64(len(payload)),
		Rows:   len(result.Rows),
	}, nil
}

func (s *FileArtifactStore) Open(_ context.Context, artifactPath string) (io.ReadCloser, error) {
	clean := filepath.Clean(artifactPath)
	if !strings.HasPrefix(clean, s.root+string(os.PathSeparator)) {
		return nil, fault.New(fault.Forbidden, "EXPORT_ARTIFACT_FORBIDDEN", "产物路径超出允许范围")
	}
	handle, err := os.Open(clean)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, fault.New(fault.NotFound, "EXPORT_ARTIFACT_NOT_FOUND", "导出产物不存在")
		}
		return nil, err
	}
	return handle, nil
}
