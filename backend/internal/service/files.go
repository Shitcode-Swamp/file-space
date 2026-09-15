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

	"github.com/jackc/pgx/v5/pgconn"

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
// preview/download, upload (single-shot and chunked), and deletion on top of
// a FileRepo (metadata) and a FileStorage (bytes).
type FileService struct {
	files   repo.FileRepo
	storage storage.FileStorage
	uploads *uploadSessionManager
}

// NewFileService constructs a FileService backed by files and store.
func NewFileService(files repo.FileRepo, store storage.FileStorage) *FileService {
	return &FileService{files: files, storage: store, uploads: newUploadSessionManager()}
}

// List returns one page of the caller's files matching params, plus
// whether more files exist beyond it. See repo.FileRepo.List.
func (s *FileService) List(ctx context.Context, params repo.ListParams) ([]domain.File, bool, error) {
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

// maxNameDedupAttempts bounds the "file (n).ext" retry loop in Create. Far
// above anything a real user would hit — it exists only so a persistent,
// unrelated unique-violation can't spin forever.
const maxNameDedupAttempts = 1000

// filesUploadedByNameConstraint is the unique index name from
// migrations/0003_unique_file_name_per_owner.up.sql, used to tell "this
// insert collided on (uploaded_by, name)" apart from any other constraint
// violation Postgres might report.
const filesUploadedByNameConstraint = "files_uploaded_by_name_key"

// Create reads all of r, stores it under a fresh, collision-resistant
// storage key, and inserts the corresponding metadata row with uploadedBy
// as both the uploader and (initial) editor.
//
// If filename collides with a name the same user already has, the new file
// is renamed "base (1).ext", "base (2).ext", etc. until one is free. This
// matters beyond cosmetics: REQUIREMENTS.md §6's sync protocol identifies
// files purely by name (see SyncViewModel.swift), and the desktop client
// builds a name-keyed dictionary of remote files with an assumed-unique
// key — two files sharing a name for one user would crash sync, not just
// look confusing in the web UI. A DB-level unique index on
// (uploaded_by, name) is what actually enforces this under concurrent
// uploads; the loop below is what turns that constraint into a graceful
// rename instead of a rejected upload.
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

	name := base
	for attempt := 1; ; attempt++ {
		created, err := s.files.Create(ctx, domain.File{
			Name:       name,
			StorageKey: key,
			Extension:  extension,
			Size:       counted.n,
			UploadedBy: uploadedBy,
			EditedBy:   uploadedBy,
		})
		if err == nil {
			return created, nil
		}

		if isNameConflict(err) && attempt <= maxNameDedupAttempts {
			name = dedupedName(base, attempt)
			continue
		}

		// Either a non-name-collision error, or we've exhausted the retry
		// budget: the metadata row never made it in, so the bytes we just
		// saved are orphaned; best-effort clean them up. Failure here
		// doesn't change the response — it's already an error — just log it.
		if delErr := s.storage.Delete(ctx, key); delErr != nil {
			log.Printf("service: cleanup orphaned storage key %q after failed create: %v", key, delErr)
		}
		return domain.File{}, fmt.Errorf("service: create file metadata: %w", err)
	}
}

// dedupedName inserts " (n)" before base's extension, e.g.
// dedupedName("report.pdf", 1) == "report (1).pdf", and
// dedupedName("README", 2) == "README (2)" when there's no extension.
func dedupedName(base string, n int) string {
	ext := filepath.Ext(base)
	stem := strings.TrimSuffix(base, ext)
	return fmt.Sprintf("%s (%d)%s", stem, n, ext)
}

// isNameConflict reports whether err wraps a Postgres unique_violation
// (23505) specifically on files_uploaded_by_name_key -- a collision on some
// other constraint (e.g. the storage_key uniqueness, vanishingly unlikely
// given it's timestamp-derived) must not be mistaken for a name clash and
// silently retried under a different name.
func isNameConflict(err error) bool {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.Code == pgUniqueViolation && pgErr.ConstraintName == filesUploadedByNameConstraint
	}
	return false
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
