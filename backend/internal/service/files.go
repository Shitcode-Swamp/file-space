package service

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"path/filepath"
	"strings"
	"time"

	"filespace/backend/internal/domain"
	"filespace/backend/internal/repo"
	"filespace/backend/internal/storage"
)

// Errors returned by FileService. Handlers map these to HTTP status codes.
var (
	// ErrPreviewNotSupported is returned by Content when the file's
	// extension is not one of the types REQUIREMENTS.md §2 requires
	// structured display for ("java", "png"). Handlers should map this to
	// 415 Unsupported Media Type; Download works for every extension.
	ErrPreviewNotSupported = errors.New("service: preview not supported for this file type")
	// ErrInvalidFilename is returned by Create when the supplied filename is
	// empty or reduces to nothing usable (e.g. a bare path separator).
	ErrInvalidFilename = errors.New("service: invalid filename")
)

// previewableContentTypes maps the (lowercased, no leading dot) extensions
// REQUIREMENTS.md §2 requires inline content display for to the Content-Type
// used when serving them.
var previewableContentTypes = map[string]string{
	"java": "text/plain; charset=utf-8",
	"png":  "image/png",
}

// ContentType returns the MIME type to serve a previewable file's content
// with. Only meaningful for extensions Content() actually accepts.
func ContentType(extension string) string {
	if ct, ok := previewableContentTypes[strings.ToLower(extension)]; ok {
		return ct
	}
	return "application/octet-stream"
}

// FileService implements file listing, metadata lookup, content
// preview/download, upload, and deletion on top of a FileRepo (metadata) and
// a FileStorage (bytes).
type FileService struct {
	files   repo.FileRepo
	storage storage.FileStorage
}

// NewFileService constructs a FileService backed by files and store.
func NewFileService(files repo.FileRepo, store storage.FileStorage) *FileService {
	return &FileService{files: files, storage: store}
}

// List returns the caller's files matching params.
func (s *FileService) List(ctx context.Context, params repo.ListParams) ([]domain.File, error) {
	return s.files.List(ctx, params)
}

// Get returns metadata for the file with the given id, scoped to
// uploadedBy so a caller can never see another user's file.
func (s *FileService) Get(ctx context.Context, uploadedBy, id int64) (domain.File, error) {
	return s.files.GetByID(ctx, uploadedBy, id)
}

// Content returns metadata plus a reader for the file's raw bytes, but only
// for extensions REQUIREMENTS.md §2 requires structured display for ("java",
// "png"); anything else yields ErrPreviewNotSupported so the handler can
// reply 415. The caller must close the returned ReadCloser.
func (s *FileService) Content(ctx context.Context, uploadedBy, id int64) (domain.File, io.ReadCloser, error) {
	f, err := s.files.GetByID(ctx, uploadedBy, id)
	if err != nil {
		return domain.File{}, nil, err
	}
	if _, ok := previewableContentTypes[strings.ToLower(f.Extension)]; !ok {
		return domain.File{}, nil, ErrPreviewNotSupported
	}

	rc, err := s.storage.Open(ctx, f.StorageKey)
	if err != nil {
		return domain.File{}, nil, fmt.Errorf("service: open file content: %w", err)
	}
	return f, rc, nil
}

// Download returns metadata plus a reader for the file's raw bytes,
// regardless of extension. The caller must close the returned ReadCloser.
func (s *FileService) Download(ctx context.Context, uploadedBy, id int64) (domain.File, io.ReadCloser, error) {
	f, err := s.files.GetByID(ctx, uploadedBy, id)
	if err != nil {
		return domain.File{}, nil, err
	}

	rc, err := s.storage.Open(ctx, f.StorageKey)
	if err != nil {
		return domain.File{}, nil, fmt.Errorf("service: open file for download: %w", err)
	}
	return f, rc, nil
}

// Create reads all of r, stores it under a fresh, collision-resistant
// storage key, and inserts the corresponding metadata row with uploadedBy
// as both the uploader and (initial) editor.
func (s *FileService) Create(ctx context.Context, uploadedBy int64, filename string, r io.Reader) (domain.File, error) {
	if filename == "" {
		return domain.File{}, ErrInvalidFilename
	}
	base := filepath.Base(filename)
	if base == "." || base == string(filepath.Separator) {
		return domain.File{}, ErrInvalidFilename
	}
	extension := strings.ToLower(strings.TrimPrefix(filepath.Ext(base), "."))

	key := fmt.Sprintf("%d/%d-%s", uploadedBy, time.Now().UnixNano(), base)

	counted := &countingReader{r: r}
	if err := s.storage.Save(ctx, key, counted); err != nil {
		return domain.File{}, fmt.Errorf("service: save file content: %w", err)
	}

	created, err := s.files.Create(ctx, domain.File{
		Name:       base,
		StorageKey: key,
		Extension:  extension,
		Size:       counted.n,
		UploadedBy: uploadedBy,
		EditedBy:   uploadedBy,
	})
	if err != nil {
		// The metadata row never made it in, so the bytes we just saved are
		// orphaned; best-effort clean them up. Failure here doesn't change
		// the response — it's already an error — just log it.
		if delErr := s.storage.Delete(ctx, key); delErr != nil {
			log.Printf("service: cleanup orphaned storage key %q after failed create: %v", key, delErr)
		}
		return domain.File{}, fmt.Errorf("service: create file metadata: %w", err)
	}
	return created, nil
}

// Delete removes the file's metadata row (scoped to uploadedBy, so it
// refuses to delete another user's file), then best-effort removes its
// bytes from storage. The DB is the source of truth for listings, so a
// storage.Delete failure after the DB row is already gone is logged rather
// than returned as a request error.
func (s *FileService) Delete(ctx context.Context, uploadedBy, id int64) error {
	f, err := s.files.GetByID(ctx, uploadedBy, id)
	if err != nil {
		return err
	}

	if err := s.files.Delete(ctx, uploadedBy, id); err != nil {
		return err
	}

	if err := s.storage.Delete(ctx, f.StorageKey); err != nil {
		log.Printf("service: delete storage key %q for file id=%d after DB delete: %v", f.StorageKey, id, err)
	}
	return nil
}

// countingReader wraps an io.Reader and tracks how many bytes have been
// read through it, so Create can learn the uploaded size without buffering
// the whole file in memory or requiring FileStorage.Save to report it.
type countingReader struct {
	r io.Reader
	n int64
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += int64(n)
	return n, err
}
