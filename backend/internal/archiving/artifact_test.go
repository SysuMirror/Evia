package archiving

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNewFileArchiveStoreRejectsRelativeRoot(t *testing.T) {
	t.Parallel()
	if _, err := NewFileArchiveStore("relative/path"); err == nil {
		t.Fatal("relative root must be rejected")
	}
	if _, err := NewFileArchiveStore(""); err == nil {
		t.Fatal("empty root must be rejected")
	}
}

func TestWriteStreamFingerprintsAndBounds(t *testing.T) {
	t.Parallel()
	store, err := NewFileArchiveStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	id := "33333333-3333-4333-8333-333333333333"
	payload := strings.Repeat("line\n", 10)
	artifact, err := store.WriteStream(context.Background(), id, func(w io.Writer) (FactWindow, error) {
		if _, err := io.WriteString(w, payload); err != nil {
			return FactWindow{}, err
		}
		return FactWindow{Rows: 10}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte(payload))
	if artifact.SHA256 != hex.EncodeToString(sum[:]) {
		t.Fatal("sha256 mismatch")
	}
	if artifact.Bytes != int64(len(payload)) {
		t.Fatalf("byte count wrong: %d", artifact.Bytes)
	}
	if filepath.Ext(artifact.Path) != ".jsonl" || filepath.Dir(artifact.Path) != store.root {
		t.Fatalf("artifact path wrong: %s", artifact.Path)
	}
	mode, err := os.Stat(artifact.Path)
	if err != nil || mode.Mode().Perm() != 0o600 {
		t.Fatalf("artifact permission wrong: %v", mode)
	}
}

func TestWriteStreamRejectsOversizeAndEmpty(t *testing.T) {
	t.Parallel()
	store, err := NewFileArchiveStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	id := "44444444-4444-4444-8444-444444444444"
	big := make([]byte, MaxArtifactBytes+1)
	if _, err = store.WriteStream(context.Background(), id, func(w io.Writer) (FactWindow, error) {
		_, err := w.Write(big)
		return FactWindow{}, err
	}); err == nil {
		t.Fatal("oversize stream must be rejected")
	}
	if _, err = os.Stat(filepath.Join(store.root, id+".jsonl")); !os.IsNotExist(err) {
		t.Fatal("failed artifact must be removed")
	}
	if _, err = store.WriteStream(context.Background(), id, func(io.Writer) (FactWindow, error) {
		return FactWindow{Rows: 0}, nil
	}); err == nil {
		t.Fatal("empty stream must be rejected")
	}
}

func TestWriteStreamRejectsBadIDAndReplacesNothing(t *testing.T) {
	t.Parallel()
	store, err := NewFileArchiveStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.WriteStream(context.Background(), "../escape", func(io.Writer) (FactWindow, error) {
		return FactWindow{}, nil
	}); err == nil {
		t.Fatal("non-uuid id must be rejected")
	}
}
