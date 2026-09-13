// Package storage defines the FileStorage abstraction used to persist file
// contents, and provides a local-disk implementation of it. Handlers and
// services must only depend on the FileStorage interface, never on the
// filesystem directly, so that swapping in an S3/R2-compatible backend later
// is a config change rather than a rewrite (see REQUIREMENTS.md §5.2).
package storage

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// ErrInvalidKey is returned when a key attempts to escape the storage root
// (e.g. via "..") or is otherwise malformed.
var ErrInvalidKey = errors.New("storage: invalid key")

// FileStorage is the abstraction all file content persistence goes through.
// LocalFileStorage backs it with the local disk today; an S3/R2-compatible
// implementation can replace it later without touching callers.
type FileStorage interface {
	// Save reads all of r and stores it under key, creating or overwriting
	// as needed.
	Save(ctx context.Context, key string, r io.Reader) error
	// Open returns a reader for the content stored under key. The caller is
	// responsible for closing it.
	Open(ctx context.Context, key string) (io.ReadCloser, error)
	// Delete removes the content stored under key. It is not an error to
	// delete a key that does not exist.
	Delete(ctx context.Context, key string) error
}

// LocalFileStorage implements FileStorage on top of a directory on the local
// filesystem. Keys are relative slash-separated paths rooted at Root.
type LocalFileStorage struct {
	root string
}

// NewLocalFileStorage creates a LocalFileStorage rooted at root, creating the
// directory (and any parents) if it does not already exist.
func NewLocalFileStorage(root string) (*LocalFileStorage, error) {
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("storage: resolve root: %w", err)
	}
	if err := os.MkdirAll(absRoot, 0o755); err != nil {
		return nil, fmt.Errorf("storage: create root: %w", err)
	}
	return &LocalFileStorage{root: absRoot}, nil
}

// resolve validates key and returns the absolute on-disk path for it,
// guaranteeing the result stays within the storage root.
func (s *LocalFileStorage) resolve(key string) (string, error) {
	if key == "" || strings.Contains(key, "\x00") {
		return "", ErrInvalidKey
	}
	cleaned := filepath.Clean(filepath.Join(s.root, filepath.FromSlash(key)))
	rootWithSep := s.root + string(os.PathSeparator)
	if cleaned != s.root && !strings.HasPrefix(cleaned, rootWithSep) {
		return "", ErrInvalidKey
	}
	return cleaned, nil
}

// Save reads all of r and writes it to the file identified by key, creating
// parent directories as needed and overwriting any existing content.
func (s *LocalFileStorage) Save(ctx context.Context, key string, r io.Reader) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	path, err := s.resolve(key)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("storage: create parent dirs for %q: %w", key, err)
	}

	tmp, err := os.CreateTemp(filepath.Dir(path), ".tmp-*")
	if err != nil {
		return fmt.Errorf("storage: create temp file for %q: %w", key, err)
	}
	tmpPath := tmp.Name()
	defer func() {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
	}()

	if _, err := io.Copy(tmp, r); err != nil {
		return fmt.Errorf("storage: write %q: %w", key, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("storage: close temp file for %q: %w", key, err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("storage: finalize %q: %w", key, err)
	}
	return nil
}

// Open returns a reader for the content stored under key.
func (s *LocalFileStorage) Open(ctx context.Context, key string) (io.ReadCloser, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	path, err := s.resolve(key)
	if err != nil {
		return nil, err
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("storage: open %q: %w", key, err)
	}
	return f, nil
}

// Delete removes the content stored under key. Deleting a key that does not
// exist is not an error.
func (s *LocalFileStorage) Delete(ctx context.Context, key string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	path, err := s.resolve(key)
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("storage: delete %q: %w", key, err)
	}
	return nil
}

// compile-time check that LocalFileStorage satisfies FileStorage.
var _ FileStorage = (*LocalFileStorage)(nil)
