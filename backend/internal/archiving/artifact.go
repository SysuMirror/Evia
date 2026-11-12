package archiving

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"hash"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/howtomakeaname/Evia/backend/internal/platform/fault"
)

var archiveUUIDPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// FileArchiveStore writes JSONL artifacts under a bounded absolute root.
// Manifest IDs are UUIDs, so the composed path cannot escape the root.
type FileArchiveStore struct {
	root string
}

func NewFileArchiveStore(root string) (*FileArchiveStore, error) {
	if root == "" {
		return nil, errors.New("archive artifact directory is required")
	}
	clean := filepath.Clean(root)
	if !filepath.IsAbs(clean) {
		return nil, errors.New("archive artifact directory must be an absolute path")
	}
	if err := os.MkdirAll(clean, 0o700); err != nil {
		return nil, err
	}
	return &FileArchiveStore{root: clean}, nil
}

// limitedWriter fails once the byte ceiling is crossed so callers cannot
// silently produce an oversize artifact, while teeing every byte into the
// hasher for the integrity fingerprint.
type limitedWriter struct {
	inner   io.Writer
	hasher  hash.Hash
	written int64
	limit   int64
}

func (w *limitedWriter) Write(p []byte) (int, error) {
	if w.written+int64(len(p)) > w.limit {
		return 0, fault.New(fault.Invalid, "ARCHIVE_ARTIFACT_TOO_LARGE", "归档产物字节数超过上限")
	}
	if _, err := w.hasher.Write(p); err != nil {
		return 0, err
	}
	n, err := w.inner.Write(p)
	w.written += int64(n)
	return n, err
}

func (s *FileArchiveStore) WriteStream(_ context.Context, manifestID string, stream func(io.Writer) (FactWindow, error)) (Artifact, error) {
	if !archiveUUIDPattern.MatchString(manifestID) {
		return Artifact{}, errors.New("invalid manifest id for artifact path")
	}
	name := manifestID + ".jsonl"
	path := filepath.Join(s.root, name)
	if !strings.HasPrefix(path, s.root+string(os.PathSeparator)) {
		return Artifact{}, errors.New("artifact path escapes the archive root")
	}
	handle, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0o600)
	if err != nil {
		return Artifact{}, err
	}
	hasher := sha256.New()
	limited := &limitedWriter{inner: handle, hasher: hasher, limit: MaxArtifactBytes}
	_, streamErr := stream(limited)
	if closeErr := handle.Close(); streamErr == nil {
		streamErr = closeErr
	}
	if streamErr != nil {
		_ = os.Remove(path)
		return Artifact{}, streamErr
	}
	if limited.written == 0 {
		_ = os.Remove(path)
		return Artifact{}, errors.New("archive stream produced an empty artifact")
	}
	info, err := os.Stat(path)
	if err != nil {
		_ = os.Remove(path)
		return Artifact{}, err
	}
	return Artifact{
		Path:   path,
		SHA256: hex.EncodeToString(hasher.Sum(nil)),
		Bytes:  info.Size(),
	}, nil
}

// Compile-time assertion that the concrete store satisfies the port.
var _ ArtifactStore = (*FileArchiveStore)(nil)

// OpenArtifact opens a previously materialized archive after verifying the
// absolute path stays inside the archive root. It satisfies the backfilling
// package's ArchiveSource port without sharing write capabilities.
func (s *FileArchiveStore) OpenArtifact(_ context.Context, artifactPath string) (io.ReadCloser, error) {
	clean := filepath.Clean(artifactPath)
	if !strings.HasPrefix(clean, s.root+string(os.PathSeparator)) {
		return nil, fault.New(fault.Forbidden, "ARCHIVE_ARTIFACT_FORBIDDEN", "归档产物路径超出允许范围")
	}
	handle, err := os.Open(clean)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, fault.New(fault.NotFound, "ARCHIVE_ARTIFACT_NOT_FOUND", "归档产物不存在")
		}
		return nil, err
	}
	return handle, nil
}
