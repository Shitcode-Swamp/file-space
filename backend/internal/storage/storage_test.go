package storage

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// This file runs under plain `go test ./...` (no build tag, no live
// Postgres) — LocalFileStorage is already a thin wrapper over a real
// on-disk directory, so these tests exercise it against a t.TempDir()
// rather than mocking anything.

// newTestStorage creates a LocalFileStorage rooted at a fresh temp dir.
func newTestStorage(t *testing.T) *LocalFileStorage {
	t.Helper()
	s, err := NewLocalFileStorage(t.TempDir())
	if err != nil {
		t.Fatalf("NewLocalFileStorage: %v", err)
	}
	return s
}

// readAll opens key and returns its full contents.
func readAll(t *testing.T, s *LocalFileStorage, key string) []byte {
	t.Helper()
	rc, err := s.Open(context.Background(), key)
	if err != nil {
		t.Fatalf("Open(%q): %v", key, err)
	}
	defer rc.Close()
	b, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("read %q: %v", key, err)
	}
	return b
}

// rootEntries walks s's root and returns every regular file path found
// beneath it, relative to the root, sorted implicitly by walk order. Used to
// assert that a rejected traversal key wrote nothing outside (or inside)
// the storage root.
func rootEntries(t *testing.T, s *LocalFileStorage) []string {
	t.Helper()
	var files []string
	err := filepath.WalkDir(s.root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			rel, relErr := filepath.Rel(s.root, path)
			if relErr != nil {
				return relErr
			}
			files = append(files, rel)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk root: %v", err)
	}
	return files
}

func TestSaveOpen_RoundTripsExactBytes(t *testing.T) {
	s := newTestStorage(t)
	ctx := context.Background()
	want := []byte("hello, filespace!")

	if err := s.Save(ctx, "doc.txt", strings.NewReader(string(want))); err != nil {
		t.Fatalf("Save: %v", err)
	}

	got := readAll(t, s, "doc.txt")
	if string(got) != string(want) {
		t.Fatalf("Open returned %q, want %q", got, want)
	}
}

func TestDelete_ThenOpen_ReturnsError(t *testing.T) {
	s := newTestStorage(t)
	ctx := context.Background()

	if err := s.Save(ctx, "doc.txt", strings.NewReader("content")); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if err := s.Delete(ctx, "doc.txt"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := s.Open(ctx, "doc.txt"); err == nil {
		t.Fatal("Open after Delete: got nil error, want error")
	}
}

func TestDelete_NonexistentKey_NoError(t *testing.T) {
	s := newTestStorage(t)
	ctx := context.Background()

	if err := s.Delete(ctx, "never-saved.txt"); err != nil {
		t.Fatalf("Delete of a never-saved key: got error %v, want nil", err)
	}
}

// TestPathTraversal_RejectedAndRootUntouched covers keys that climb above
// the storage root (via ".." components, with or without a leading slash).
// Each must be rejected with ErrInvalidKey by all three operations, and the
// root's contents must be byte-for-byte unchanged afterward — a stray write
// outside the root, or one that silently lands inside it under an unexpected
// path, would both be a break of the key-sandboxing contract.
func TestPathTraversal_RejectedAndRootUntouched(t *testing.T) {
	s := newTestStorage(t)
	ctx := context.Background()

	keys := []string{
		"../../../etc/passwd",
		"a/../../b",
		"/../etc/passwd",
	}

	for _, key := range keys {
		t.Run(key, func(t *testing.T) {
			before := rootEntries(t, s)

			if err := s.Save(ctx, key, strings.NewReader("pwned")); !errors.Is(err, ErrInvalidKey) {
				t.Fatalf("Save(%q) error = %v, want ErrInvalidKey", key, err)
			}
			if _, err := s.Open(ctx, key); !errors.Is(err, ErrInvalidKey) {
				t.Fatalf("Open(%q) error = %v, want ErrInvalidKey", key, err)
			}
			if err := s.Delete(ctx, key); !errors.Is(err, ErrInvalidKey) {
				t.Fatalf("Delete(%q) error = %v, want ErrInvalidKey", key, err)
			}

			after := rootEntries(t, s)
			if len(before) != len(after) {
				t.Fatalf("root contents changed for key %q: before=%v after=%v", key, before, after)
			}
		})
	}
}

// TestAbsoluteLookingKey_ContainedWithinRoot documents the actual, verified
// behavior of resolve() for a key that merely looks absolute (no ".."
// component): filepath.Join treats the leading slash as just another path
// separator, so "/etc/passwd" is *not* rejected — it is silently contained
// as <root>/etc/passwd, never as the real /etc/passwd. This pins that
// containment down so a future refactor of resolve() can't quietly turn it
// into an escape.
func TestAbsoluteLookingKey_ContainedWithinRoot(t *testing.T) {
	s := newTestStorage(t)
	ctx := context.Background()

	key := "/etc/passwd"
	want := "not the real /etc/passwd"
	if err := s.Save(ctx, key, strings.NewReader(want)); err != nil {
		t.Fatalf("Save(%q): %v", key, err)
	}

	got := readAll(t, s, key)
	if string(got) != want {
		t.Fatalf("Open(%q) = %q, want %q", key, got, want)
	}

	wantPath := filepath.Join(s.root, "etc", "passwd")
	if _, err := os.Stat(wantPath); err != nil {
		t.Fatalf("expected file at %q, stat error: %v", wantPath, err)
	}
}

func TestNullByteKey_RejectedAsInvalid(t *testing.T) {
	s := newTestStorage(t)
	ctx := context.Background()

	key := "doc\x00.txt"
	if err := s.Save(ctx, key, strings.NewReader("data")); !errors.Is(err, ErrInvalidKey) {
		t.Fatalf("Save(%q) error = %v, want ErrInvalidKey", key, err)
	}
	if _, err := s.Open(ctx, key); !errors.Is(err, ErrInvalidKey) {
		t.Fatalf("Open(%q) error = %v, want ErrInvalidKey", key, err)
	}
	if err := s.Delete(ctx, key); !errors.Is(err, ErrInvalidKey) {
		t.Fatalf("Delete(%q) error = %v, want ErrInvalidKey", key, err)
	}
}

func TestSave_OverwritesExistingKey(t *testing.T) {
	s := newTestStorage(t)
	ctx := context.Background()

	if err := s.Save(ctx, "doc.txt", strings.NewReader("old content")); err != nil {
		t.Fatalf("Save (initial): %v", err)
	}
	if err := s.Save(ctx, "doc.txt", strings.NewReader("new content")); err != nil {
		t.Fatalf("Save (overwrite): %v", err)
	}

	got := readAll(t, s, "doc.txt")
	if string(got) != "new content" {
		t.Fatalf("Open after overwrite = %q, want %q", got, "new content")
	}
}

func TestSave_NestedKey_CreatesParentDirs(t *testing.T) {
	s := newTestStorage(t)
	ctx := context.Background()

	// Matches the real key format FileService.Create builds:
	// fmt.Sprintf("%d/%d-%s", uploadedBy, timestamp, base).
	key := "42/1234-file.txt"
	want := "nested file content"

	if err := s.Save(ctx, key, strings.NewReader(want)); err != nil {
		t.Fatalf("Save(%q): %v", key, err)
	}

	got := readAll(t, s, key)
	if string(got) != want {
		t.Fatalf("Open(%q) = %q, want %q", key, got, want)
	}

	if _, err := os.Stat(filepath.Join(s.root, "42")); err != nil {
		t.Fatalf("parent directory not created: %v", err)
	}
}
