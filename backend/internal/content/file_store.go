package content

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

type FileStore struct{ root string }

func NewFileStore(root string) (*FileStore, error) {
	if root == "" || !filepath.IsAbs(root) {
		return nil, errors.New("content artifact directory must be absolute")
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		return nil, err
	}
	if err := os.Chmod(root, 0o700); err != nil {
		return nil, err
	}
	return &FileStore{root: root}, nil
}

func (s *FileStore) Put(_ context.Context, chunk DecryptedChunk) (string, string, error) {
	identity := fmt.Sprintf("%s|%s|%s|%d|%s", chunk.ProjectID, chunk.Environment, chunk.MessageID, chunk.ChunkIndex, chunk.EventID)
	nameHash := sha256.Sum256([]byte(identity))
	objectKey := hex.EncodeToString(nameHash[:]) + ".chunk"
	finalPath := filepath.Join(s.root, objectKey)
	contentHash := sha256.Sum256(chunk.Plaintext)
	temp, err := os.CreateTemp(s.root, ".content-*")
	if err != nil {
		return "", "", err
	}
	tempName := temp.Name()
	defer os.Remove(tempName)
	if err = temp.Chmod(0o600); err == nil {
		_, err = temp.Write(chunk.Plaintext)
	}
	if closeErr := temp.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return "", "", err
	}
	if err = os.Rename(tempName, finalPath); err != nil {
		return "", "", err
	}
	return objectKey, hex.EncodeToString(contentHash[:]), nil
}

func (s *FileStore) Read(_ context.Context, objectKey string) ([]byte, error) {
	if len(objectKey) != 70 || filepath.Ext(objectKey) != ".chunk" {
		return nil, errors.New("invalid content object key")
	}
	for _, char := range objectKey[:64] {
		if !(char >= '0' && char <= '9' || char >= 'a' && char <= 'f') {
			return nil, errors.New("invalid content object key")
		}
	}
	path := filepath.Join(s.root, objectKey)
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if len(data) == 0 || len(data) > 600 {
		return nil, errors.New("content object exceeds chunk bound")
	}
	return data, nil
}
